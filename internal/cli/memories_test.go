package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

func TestMemoriesLifecycle(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	pat := e.conn(t, "pat")
	if _, err := pat.Bus.CreateChannel(ctx, bus.CreateChannelInput{Name: "notes", Kind: bus.KindMemory}); err != nil {
		t.Fatal(err)
	}
	sent, err := pat.Bus.Send(ctx, bus.SendInput{Channel: "notes", Subject: "first", Content: "v1"})
	if err != nil || sent.MemoryID == nil {
		t.Fatalf("send: %+v, %v", sent, err)
	}
	id := strconv.FormatInt(*sent.MemoryID, 10)
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := RunMemories(ctx, append(args, "-identity", "pat"), e.Environ, strings.NewReader(""), &out, &errb)
		return code, out.String(), errb.String()
	}

	code, out, errs := run("get", id)
	if code != 0 || !strings.Contains(out, `"content": "v1"`) {
		t.Fatalf("get: %d %q %q", code, out, errs)
	}

	code, out1, errs := run("edit", id, "v2", "-tag", "kind:gotcha", "-key", "k")
	if code != 0 {
		t.Fatalf("edit: %d %q", code, errs)
	}
	var r struct{ Revision, Replaced int64 }
	if err := json.Unmarshal([]byte(out1), &r); err != nil || r.Revision == 0 || r.Replaced == 0 {
		t.Fatalf("edit output %q: %+v, %v", out1, r, err)
	}
	if code, out2, _ := run("edit", id, "v2", "-tag", "kind:gotcha", "-key", "k"); code != 0 || out2 != out1 {
		t.Fatalf("second identical edit: %d %q, want %q", code, out2, out1)
	}
	// An edit that names no subject keeps it; -subject replaces it.
	if m, err := pat.Bus.GetMemory(ctx, *sent.MemoryID); err != nil || m.Subject != "first" {
		t.Fatalf("subject after an edit without -subject: %+v, %v", m, err)
	}
	if code, _, errs = run("edit", "-subject", "renamed", id); code != 0 {
		t.Fatalf("edit -subject: %d %q", code, errs)
	}
	if m, err := pat.Bus.GetMemory(ctx, *sent.MemoryID); err != nil || m.Subject != "renamed" || m.Content != "v2" {
		t.Fatalf("after edit -subject: %+v, %v", m, err)
	}
	code, out, _ = run("revisions", id)
	var revs []bus.Message
	if err := json.Unmarshal([]byte(out), &revs); code != 0 || err != nil || len(revs) != 3 {
		t.Fatalf("revisions: %d %q %v", code, out, err)
	}

	// -clear-subject with no content keeps the content and clears the subject.
	if code, _, errs = run("edit", "-clear-subject", id); code != 0 {
		t.Fatalf("clear-subject: %d %q", code, errs)
	}
	m, err := pat.Bus.GetMemory(ctx, *sent.MemoryID)
	if err != nil || m.Subject != "" || m.Content != "v2" || len(m.Tags) != 1 {
		t.Fatalf("after clear-subject: %+v, %v", m, err)
	}
	// -clear-tags empties the tag set.
	if code, _, errs = run("edit", "-clear-tags", id); code != 0 {
		t.Fatalf("clear-tags: %d %q", code, errs)
	}
	if m, _ = pat.Bus.GetMemory(ctx, *sent.MemoryID); len(m.Tags) != 0 {
		t.Fatalf("tags after clear-tags: %v", m.Tags)
	}
	// Content from stdin.
	var out3, err3 bytes.Buffer
	if c := RunMemories(ctx, []string{"edit", "-identity", "pat", id, "-"}, e.Environ, strings.NewReader("from stdin\n"), &out3, &err3); c != 0 {
		t.Fatalf("stdin edit: %d %q", c, err3.String())
	}
	if m, _ = pat.Bus.GetMemory(ctx, *sent.MemoryID); m.Content != "from stdin\n" {
		t.Fatalf("content %q", m.Content)
	}

	// Omitted -type/-metadata keep; -type '' and -clear-metadata clear; the
	// content is never re-read for them; -if-revision guards.
	if code, _, errs = run("edit", "-type", "note", "-metadata", "k=v", id); code != 0 {
		t.Fatalf("edit -type -metadata: %d %q", code, errs)
	}
	if code, _, errs = run("edit", "-subject", "again", id); code != 0 {
		t.Fatalf("edit -subject: %d %q", code, errs)
	}
	if m, _ = pat.Bus.GetMemory(ctx, *sent.MemoryID); m.Type != "note" || m.Metadata["k"] != "v" || m.Content != "from stdin\n" {
		t.Fatalf("omitted -type/-metadata did not keep: %+v", m)
	}
	if code, _, errs = run("edit", "-type", "", id); code != 0 {
		t.Fatalf("edit -type '': %d %q", code, errs)
	}
	if m, _ = pat.Bus.GetMemory(ctx, *sent.MemoryID); m.Type != "" || m.Metadata["k"] != "v" {
		t.Fatalf("-type '' did not clear only the type: %+v", m)
	}
	if code, _, errs = run("edit", "-clear-metadata", id); code != 0 {
		t.Fatalf("edit -clear-metadata: %d %q", code, errs)
	}
	if m, _ = pat.Bus.GetMemory(ctx, *sent.MemoryID); len(m.Metadata) != 0 || m.Content != "from stdin\n" {
		t.Fatalf("-clear-metadata: %+v", m)
	}
	if code, _, errs = run("edit", "-if-revision", "1", id, "stale"); code != 1 || !strings.Contains(errs, "conflict") {
		t.Fatalf("stale -if-revision: %d %q", code, errs)
	}
	m, _ = pat.Bus.GetMemory(ctx, *sent.MemoryID)
	if code, _, errs = run("edit", "-if-revision", strconv.FormatInt(*m.Revision, 10), id, "fresh"); code != 0 {
		t.Fatalf("matching -if-revision: %d %q", code, errs)
	}
	if code, _, errs = run("edit", id); code != 1 || !strings.Contains(errs, "validation") {
		t.Fatalf("edit with nothing to change: %d %q", code, errs)
	}

	if code, _, errs = run("delete", id); code != 0 {
		t.Fatalf("delete: %d %q", code, errs)
	}
	if code, _, errs = run("get", id); code != 1 || !strings.Contains(errs, "not_found") {
		t.Fatalf("get after delete: %d %q", code, errs)
	}
}

func TestMemoriesRejectsBadInput(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	for _, args := range [][]string{
		nil,
		{"frobnicate"},
		{"get"},
		{"get", "0"},
		{"get", "abc"},
		{"get", "1", "2"},
		{"get", "-nosuchflag", "1"},
		{"edit", "-nosuchflag", "1", "x"},
		{"edit", "1", "x", "y"},
		{"edit", "-subject", "s", "-clear-subject", "1", "x"},
		{"edit", "-tag", "a", "-clear-tags", "1", "x"},
		{"edit", "-metadata", "novalue", "1", "x"},
		{"edit", "-metadata", "a=1", "-metadata", "a=2", "1", "x"},
		{"edit", "-metadata", "a=1", "-clear-metadata", "1", "x"},
		{"edit", "-if-revision", "-3", "1", "x"},
		{"delete", "-tag", "x", "1"},
	} {
		var out, errb bytes.Buffer
		if code := RunMemories(ctx, args, e.Environ, strings.NewReader(""), &out, &errb); code != 2 {
			t.Errorf("%v: exit %d (stderr %q), want 2", args, code, errb.String())
		}
	}
	var out, errb bytes.Buffer
	if code := RunMemories(ctx, []string{"-h"}, e.Environ, strings.NewReader(""), &out, &errb); code != 0 {
		t.Errorf("-h: exit %d", code)
	}
}
