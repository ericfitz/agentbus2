package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
)

const tasksUsage = "usage: agentbus2 tasks <create|list|get|update|claim|release> [flags] ..."

// opTaskStatus rejects a status other than the three the core defines.
func opTaskStatus(s string) error {
	switch s {
	case "pending", "in_progress", "completed":
		return nil
	}
	return fmt.Errorf("-status %q: want pending, in_progress or completed", s)
}

// RunTasks implements `agentbus2 tasks`.
func RunTasks(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	_ = stdin
	if len(args) == 0 {
		return opUsage(stderr, "%s", tasksUsage)
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "create", "list", "get", "update", "claim", "release":
	case "-h", "-help", "--help":
		_, _ = fmt.Fprintln(stderr, tasksUsage)
		return 0
	default:
		return opUsage(stderr, "unknown tasks subcommand %q\n%s", sub, tasksUsage)
	}

	fs := newFlagSet("tasks "+sub, stderr)
	var o client.Options
	o.Environ = environ
	client.Flags(fs, &o)
	var (
		description, status, owner, subject, key string
		parent, leasedUntil, before, after       int64
		clearOwner, del, force                   bool
		blockedBy, addBlocked, removeBlocked     opStrings
		metaVals                                 opStrings
	)
	switch sub {
	case "create":
		fs.StringVar(&description, "description", "", "description")
		fs.Int64Var(&parent, "parent", 0, "parent task id")
		fs.Int64Var(&before, "before", 0, "sibling id to order before")
		fs.Int64Var(&after, "after", 0, "sibling id to order after")
		fs.Var(&blockedBy, "blocked-by", "blocking task id (repeatable)")
		fs.Var(&metaVals, "metadata", "metadata k=v (repeatable)")
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
		fs.Var(&metaVals, "metadata", "metadata k=v (repeatable)")
		fs.BoolVar(&del, "delete", false, "delete the task")
		fs.BoolVar(&force, "force", false, "force")
		fs.StringVar(&key, "key", "", "idempotency key")
	case "claim":
		fs.Int64Var(&leasedUntil, "leased-until", 0, "lease expiry, Unix milliseconds")
		fs.StringVar(&key, "key", "", "idempotency key")
	case "release":
		fs.StringVar(&key, "key", "", "idempotency key")
	}
	pos, err := opParse(fs, args)
	if err != nil {
		return parseExit(err)
	}
	set := opFlagsSet(fs)

	want := 1
	if sub == "create" {
		want = 2
	}
	if len(pos) != want {
		return opUsage(stderr, "tasks %s: want %d argument(s), got %d", sub, want, len(pos))
	}
	if set["status"] {
		if err := opTaskStatus(status); err != nil {
			return opUsage(stderr, "%v", err)
		}
	}
	if set["leased-until"] && leasedUntil < 0 {
		return opUsage(stderr, "-leased-until %d: want a non-negative Unix millisecond time", leasedUntil)
	}
	if set["before"] && set["after"] {
		return opUsage(stderr, "-before and -after are mutually exclusive")
	}
	if set["owner"] && clearOwner {
		return opUsage(stderr, "-owner and -clear-owner are mutually exclusive")
	}
	meta, err := opMetadata(metaVals)
	if err != nil {
		return opUsage(stderr, "%v", err)
	}
	if set["parent"] && parent < 0 {
		return opUsage(stderr, "-parent %d: want a task id", parent)
	}
	for _, f := range []struct {
		name string
		id   int64
	}{{"before", before}, {"after", after}} {
		if set[f.name] && f.id <= 0 {
			return opUsage(stderr, "-%s %d: want a positive task id", f.name, f.id)
		}
	}

	var id int64
	if sub != "create" && sub != "list" {
		if id, err = opID(pos[0]); err != nil {
			return opUsage(stderr, "%v", err)
		}
	}
	var blockers, adds, removes []int64
	if blockers, err = opIDs("blocked-by", blockedBy); err != nil {
		return opUsage(stderr, "%v", err)
	}
	if adds, err = opIDs("add-blocked-by", addBlocked); err != nil {
		return opUsage(stderr, "%v", err)
	}
	if removes, err = opIDs("remove-blocked-by", removeBlocked); err != nil {
		return opUsage(stderr, "%v", err)
	}

	c, code := open(ctx, o, stderr)
	if c == nil {
		return code
	}
	defer func() { _ = c.Close() }()

	switch sub {
	case "create":
		t, err := c.Bus.Create(ctx, bus.TaskCreateInput{
			Channel: pos[0], Subject: pos[1], Description: description, Parent: parent,
			Before: before, After: after, BlockedBy: blockers, Metadata: meta, IdempotencyKey: key,
		})
		if err != nil {
			return fail(stderr, err)
		}
		return opPrint(stdout, stderr, t)
	case "list":
		rows, err := c.Bus.List(ctx, bus.TaskListInput{Channel: pos[0], Status: status, Owner: owner})
		if err != nil {
			return fail(stderr, err)
		}
		if rows == nil {
			rows = []bus.TaskSummary{}
		}
		return opPrint(stdout, stderr, rows)
	case "get":
		t, err := c.Bus.GetTask(ctx, id)
		if err != nil {
			return fail(stderr, err)
		}
		return opPrint(stdout, stderr, t)
	case "claim":
		r, err := c.Bus.Claim(ctx, bus.TaskClaimInput{ID: id, LeasedUntil: leasedUntil, IdempotencyKey: key})
		if err != nil {
			return fail(stderr, err)
		}
		return opPrint(stdout, stderr, r)
	case "release":
		r, err := c.Bus.Release(ctx, bus.TaskReleaseInput{ID: id, IdempotencyKey: key})
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
	if set["status"] {
		p.Status = &status
	}
	switch {
	case clearOwner:
		empty := ""
		p.Owner = &empty
	case set["owner"]:
		p.Owner = &owner
	}
	if set["subject"] {
		p.Subject = &subject
	}
	if set["description"] {
		p.Description = &description
	}
	if set["parent"] {
		p.Parent = &parent
	}
	if set["leased-until"] {
		p.LeasedUntil = &leasedUntil
	}
	r, err := c.Bus.Update(ctx, p)
	if err != nil {
		return fail(stderr, err)
	}
	return opPrint(stdout, stderr, r)
}
