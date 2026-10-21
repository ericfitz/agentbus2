package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// opTaskStatus rejects a status other than the three the core defines.
func opTaskStatus(s string) error {
	switch s {
	case "pending", "in_progress", "completed":
		return nil
	}
	return fmt.Errorf("-status %q: want pending, in_progress or completed", s)
}

// RunTasks implements `agentbus2 tasks create|list|get|update|claim|release`.
func RunTasks(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	subs := map[string]opRunFunc{}
	for _, sub := range []string{"create", "list", "get", "update", "claim", "release"} {
		subs[sub] = func(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
			return runTasks(ctx, sub, args, environ, stdout, stderr)
		}
	}
	return opDispatch(ctx, "tasks", subs, args, environ, stdin, stdout, stderr)
}

func runTasks(ctx context.Context, sub string, args []string, environ []string, stdout, stderr io.Writer) int {
	c := newOpCmd("tasks "+sub, stderr)
	fs := c.fs
	var (
		description, status, owner, subject, key string
		parent, leasedUntil, before, after       int64
		clearOwner, del, force                   bool
		blockedBy, addBlocked, removeBlocked     opList
		meta                                     opMeta
	)
	switch sub {
	case "create":
		fs.StringVar(&description, "description", "", "description")
		fs.Int64Var(&parent, "parent", 0, "parent task id")
		fs.Int64Var(&before, "before", 0, "sibling id to order before")
		fs.Int64Var(&after, "after", 0, "sibling id to order after")
		fs.Var(&blockedBy, "blocked-by", "blocking task id (repeatable)")
		fs.Var(&meta, "metadata", "metadata k=v (repeatable)")
		fs.StringVar(&key, "key", "", "idempotency key")
	case "list":
		fs.StringVar(&status, "status", "", "filter by status")
		fs.StringVar(&owner, "owner", "", "filter by owner")
	case "update":
		fs.StringVar(&status, "status", "", "new status")
		fs.StringVar(&owner, "owner", "", "new owner (absent keeps it)")
		fs.BoolVar(&clearOwner, "clear-owner", false, "clear the owner")
		fs.StringVar(&subject, "subject", "", "new subject")
		fs.StringVar(&description, "description", "", "new description")
		fs.Int64Var(&parent, "parent", 0, "new parent task id (0 detaches)")
		fs.Int64Var(&before, "before", 0, "sibling id to order before")
		fs.Int64Var(&after, "after", 0, "sibling id to order after")
		fs.Var(&addBlocked, "add-blocked-by", "blocker id to add (repeatable)")
		fs.Var(&removeBlocked, "remove-blocked-by", "blocker id to remove (repeatable)")
		fs.Int64Var(&leasedUntil, "leased-until", 0, "lease expiry, Unix milliseconds")
		fs.Var(&meta, "metadata", "metadata k=v (repeatable)")
		fs.BoolVar(&del, "delete", false, "delete the task")
		fs.BoolVar(&force, "force", false, "force")
		fs.StringVar(&key, "key", "", "idempotency key")
	case "claim":
		fs.Int64Var(&leasedUntil, "leased-until", 0, "lease expiry, Unix milliseconds")
		fs.StringVar(&key, "key", "", "idempotency key")
	case "release":
		fs.StringVar(&key, "key", "", "idempotency key")
	}
	want := 1 // ID, or CHANNEL for list
	if sub == "create" {
		want = 2 // CHANNEL SUBJECT
	}
	pos, code, ok := c.parse(args, want, want)
	if !ok {
		return code
	}
	if c.seen("status") {
		if err := opTaskStatus(status); err != nil {
			return c.usageErr("%v", err)
		}
	}
	if c.seen("leased-until") && leasedUntil < 0 {
		return c.usageErr("-leased-until %d: want a non-negative Unix millisecond time", leasedUntil)
	}
	if c.seen("before") && c.seen("after") {
		return c.usageErr("-before and -after are mutually exclusive")
	}
	if c.seen("owner") && clearOwner {
		return c.usageErr("-owner and -clear-owner are mutually exclusive")
	}
	if c.seen("parent") && parent < 0 {
		return c.usageErr("-parent %d: want a task id", parent)
	}
	for _, f := range []struct {
		name string
		id   int64
	}{{"before", before}, {"after", after}} {
		if c.seen(f.name) && f.id <= 0 {
			return c.usageErr("-%s %d: want a positive task id", f.name, f.id)
		}
	}

	var id int64
	var err error
	if sub != "create" && sub != "list" {
		if id, err = opID(pos[0]); err != nil {
			return c.usageErr("%v", err)
		}
	}
	var blockers, adds, removes []int64
	if blockers, err = opIDs("blocked-by", blockedBy); err != nil {
		return c.usageErr("%v", err)
	}
	if adds, err = opIDs("add-blocked-by", addBlocked); err != nil {
		return c.usageErr("%v", err)
	}
	if removes, err = opIDs("remove-blocked-by", removeBlocked); err != nil {
		return c.usageErr("%v", err)
	}

	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()

	switch sub {
	case "create":
		t, err := conn.Bus.Create(ctx, bus.TaskCreateInput{
			Channel: pos[0], Subject: pos[1], Description: description, Parent: parent,
			Before: before, After: after, BlockedBy: blockers, Metadata: meta, IdempotencyKey: key,
		})
		if err != nil {
			return fail(stderr, err)
		}
		return opPrint(stdout, stderr, t)
	case "list":
		rows, err := conn.Bus.List(ctx, bus.TaskListInput{Channel: pos[0], Status: status, Owner: owner})
		if err != nil {
			return fail(stderr, err)
		}
		if rows == nil {
			rows = []bus.TaskSummary{}
		}
		return opPrint(stdout, stderr, rows)
	case "get":
		t, err := conn.Bus.GetTask(ctx, id)
		if err != nil {
			return fail(stderr, err)
		}
		return opPrint(stdout, stderr, t)
	case "claim":
		r, err := conn.Bus.Claim(ctx, bus.TaskClaimInput{ID: id, LeasedUntil: leasedUntil, IdempotencyKey: key})
		if err != nil {
			return fail(stderr, err)
		}
		return opPrint(stdout, stderr, r)
	case "release":
		r, err := conn.Bus.Release(ctx, bus.TaskReleaseInput{ID: id, IdempotencyKey: key})
		if err != nil {
			return fail(stderr, err)
		}
		return opPrint(stdout, stderr, r)
	}
	// update
	p := bus.TaskPatch{
		ID: id, Before: before, After: after, AddBlockedBy: adds, RemoveBlockedBy: removes,
		Metadata: meta, Delete: del, Force: force, IdempotencyKey: key,
	}
	if c.seen("status") {
		p.Status = &status
	}
	switch {
	case clearOwner:
		empty := ""
		p.Owner = &empty
	case c.seen("owner"):
		p.Owner = &owner
	}
	if c.seen("subject") {
		p.Subject = &subject
	}
	if c.seen("description") {
		p.Description = &description
	}
	if c.seen("parent") {
		p.Parent = &parent
	}
	if c.seen("leased-until") {
		p.LeasedUntil = &leasedUntil
	}
	r, err := conn.Bus.Update(ctx, p)
	if err != nil {
		return fail(stderr, err)
	}
	return opPrint(stdout, stderr, r)
}
