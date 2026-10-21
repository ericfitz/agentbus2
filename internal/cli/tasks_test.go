package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

func TestTasksLifecycle(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	sam := e.conn(t, "sam")
	// pat holds a live session: a task owned by an identity with none is
	// reclaimed as abandoned, so the second claim below conflicts only then.
	if _, err := e.conn(t, "pat").Bus.Open(ctx, bus.OpenSessionInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := sam.Bus.CreateChannel(ctx, bus.CreateChannelInput{Name: "todo", Kind: bus.KindTasks, ACL: []bus.ACLEntry{{Identity: "everyone", Permissions: []bus.Permission{bus.PermRead, bus.PermWrite}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := sam.Bus.CreateChannel(ctx, bus.CreateChannelInput{Name: "chat"}); err != nil {
		t.Fatal(err)
	}
	run := func(as string, args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := RunTasks(ctx, append(args, "-identity", as), e.Environ, strings.NewReader(""), &out, &errb)
		return code, out.String(), errb.String()
	}

	if code, out, errs := run("sam", "create", "todo", "Add tags"); code != 0 || !strings.Contains(out, `"id": 1`) {
		t.Fatalf("create: %d %q %q", code, out, errs)
	}
	if code, _, errs := run("sam", "create", "-parent", "1", "todo", "Child"); code != 0 {
		t.Fatalf("create child: %d %q", code, errs)
	}
	code, out, errs := run("sam", "list", "todo")
	var rows []bus.TaskSummary
	if err := json.Unmarshal([]byte(out), &rows); code != 0 || err != nil || len(rows) != 2 {
		t.Fatalf("list: %d %q %q %v", code, out, errs, err)
	}
	if rows[0].Depth != 0 || rows[1].Depth != 1 || rows[1].Parent != 1 {
		t.Fatalf("depths: %+v", rows)
	}
	if code, _, errs = run("pat", "claim", "2"); code != 0 {
		t.Fatalf("claim: %d %q", code, errs)
	}
	if code, out, errs = run("sam", "claim", "2"); code != 1 || !strings.Contains(errs, "conflict") {
		t.Fatalf("second claim: %d %q %q", code, errs, out)
	}
	if code, out, errs = run("pat", "list", "-owner", "pat", "todo"); code != 0 || !strings.Contains(out, `"id": 2`) || strings.Contains(out, `"id": 1,`) {
		t.Fatalf("list -owner: %d %q %q", code, out, errs)
	}
	if code, _, errs = run("pat", "update", "-status", "completed", "2"); code != 0 {
		t.Fatalf("update: %d %q", code, errs)
	}
	if code, _, errs = run("sam", "release", "1"); code != 0 {
		t.Fatalf("release: %d %q", code, errs)
	}
	// The core refuses to delete a task that has subtasks.
	if code, _, errs = run("sam", "update", "-delete", "1"); code != 1 || !strings.Contains(errs, "subtasks") {
		t.Fatalf("delete with a subtask: %d %q", code, errs)
	}
	if code, _, errs = run("sam", "update", "-delete", "-force", "2"); code != 0 {
		t.Fatalf("delete child: %d %q", code, errs)
	}
	if code, _, errs = run("sam", "update", "-delete", "1"); code != 0 {
		t.Fatalf("delete: %d %q", code, errs)
	}
	if code, _, errs = run("sam", "get", "1"); code != 1 || !strings.Contains(errs, "not_found") {
		t.Fatalf("get after delete: %d %q", code, errs)
	}
	if code, _, errs = run("sam", "create", "chat", "nope"); code != 1 || !strings.Contains(errs, "validation") {
		t.Fatalf("create on an ordinary channel: %d %q", code, errs)
	}
}

func TestTasksClearOwnerAndSubject(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	pat := e.conn(t, "pat")
	if _, err := pat.Bus.CreateChannel(ctx, bus.CreateChannelInput{Name: "todo", Kind: bus.KindTasks}); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := RunTasks(ctx, append(args, "-identity", "pat"), e.Environ, strings.NewReader(""), &out, &errb)
		return code, out.String(), errb.String()
	}
	if code, _, errs := run("create", "todo", "one"); code != 0 {
		t.Fatalf("create: %d %q", code, errs)
	}
	if code, _, errs := run("update", "-owner", "pat", "1"); code != 0 {
		t.Fatalf("set owner: %d %q", code, errs)
	}
	if tk, _ := pat.Bus.GetTask(ctx, 1); tk.Owner != "pat" {
		t.Fatalf("owner %q", tk.Owner)
	}
	// An update that names no owner keeps it; -clear-owner sends a pointer to "".
	if code, _, errs := run("update", "-subject", "renamed", "1"); code != 0 {
		t.Fatalf("rename: %d %q", code, errs)
	}
	if tk, _ := pat.Bus.GetTask(ctx, 1); tk.Owner != "pat" || tk.Subject != "renamed" {
		t.Fatalf("after rename: %+v", tk)
	}
	if code, _, errs := run("update", "-clear-owner", "1"); code != 0 {
		t.Fatalf("clear owner: %d %q", code, errs)
	}
	if tk, _ := pat.Bus.GetTask(ctx, 1); tk.Owner != "" {
		t.Fatalf("owner after clear: %q", tk.Owner)
	}
}

func TestTasksReparent(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	pat := e.conn(t, "pat")
	if _, err := pat.Bus.CreateChannel(ctx, bus.CreateChannelInput{Name: "todo", Kind: bus.KindTasks}); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := RunTasks(ctx, append(args, "-identity", "pat"), e.Environ, strings.NewReader(""), &out, &errb)
		return code, errb.String()
	}
	for _, s := range []string{"one", "two"} {
		if code, errs := run("create", "todo", s); code != 0 {
			t.Fatalf("create %s: %d %q", s, code, errs)
		}
	}
	// -parent moves the task under another; an update without it keeps the
	// parent; -parent 0 detaches it to the root.
	if code, errs := run("update", "-parent", "1", "2"); code != 0 {
		t.Fatalf("reparent: %d %q", code, errs)
	}
	if tk, _ := pat.Bus.GetTask(ctx, 2); tk.Parent != 1 {
		t.Fatalf("parent after -parent 1: %d", tk.Parent)
	}
	if code, errs := run("update", "-description", "d", "2"); code != 0 {
		t.Fatalf("describe: %d %q", code, errs)
	}
	if tk, _ := pat.Bus.GetTask(ctx, 2); tk.Parent != 1 || tk.Description != "d" {
		t.Fatalf("after an update without -parent: %+v", tk)
	}
	if code, errs := run("update", "-parent", "0", "2"); code != 0 {
		t.Fatalf("detach: %d %q", code, errs)
	}
	if tk, _ := pat.Bus.GetTask(ctx, 2); tk.Parent != 0 {
		t.Fatalf("parent after -parent 0: %d", tk.Parent)
	}
}

func TestTasksRejectsBadInput(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	for _, args := range [][]string{
		nil,
		{"frobnicate"},
		{"create", "todo"},
		{"list"},
		{"list", "-status", "done", "todo"},
		{"list", "-nosuchflag", "todo"},
		{"update", "-status", "finished", "1"},
		{"update", "-owner", "a", "-clear-owner", "1"},
		{"update", "-before", "2", "-after", "3", "1"},
		{"update", "-leased-until", "-5", "1"},
		{"update", "-add-blocked-by", "x", "1"},
		{"update", "-metadata", "bad", "1"},
		{"update", "-metadata", "a=1", "-metadata", "a=2", "1"},
		{"create", "todo", "s", "--", "-x"},
		{"create", "-blocked-by", "0", "todo", "s"},
		{"get", "0"},
		{"get", "x"},
		{"claim", "1", "2"},
		{"release", "-leased-until", "1", "1"},
	} {
		var out, errb bytes.Buffer
		if code := RunTasks(ctx, args, e.Environ, strings.NewReader(""), &out, &errb); code != 2 {
			t.Errorf("%v: exit %d (stderr %q), want 2", args, code, errb.String())
		}
	}
}
