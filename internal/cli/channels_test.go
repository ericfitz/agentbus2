package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

var capsEmpty = regexp.MustCompile(`"capabilities":\s*\[\]`)

func TestChannelsLifecycle(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	run := func(stdin string, args ...string) (int, string, string) {
		var o, er bytes.Buffer
		code := RunChannels(ctx, args, e.Environ, strings.NewReader(stdin), &o, &er)
		return code, o.String(), er.String()
	}
	ok := func(args ...string) string {
		t.Helper()
		code, out, er := run("", args...)
		if code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, er)
		}
		return out
	}
	type ch struct{ Name, Kind string }
	names := func() map[string]string {
		t.Helper()
		var cs []ch
		if err := json.Unmarshal([]byte(ok("list")), &cs); err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		for _, c := range cs {
			m[c.Name] = c.Kind
		}
		return m
	}

	if _, has := names()["general"]; !has {
		t.Fatalf("list lacks general: %v", names())
	}
	if out := ok("list"); !capsEmpty.MatchString(out) || strings.Contains(out, "null") {
		t.Fatalf("list lacks capabilities []: %s", out)
	}
	if out := ok("create", "dev"); !capsEmpty.MatchString(out) {
		t.Fatalf("create lacks capabilities []: %s", out)
	}
	ok("create", "-kind", "memory", "notes")
	if m := names(); m["dev"] != "ordinary" || m["notes"] != "memory" {
		t.Fatalf("after create: %v", m)
	}
	// An unknown kind is the core's to judge (Decision 80).
	if code, _, er := run("", "create", "-kind", "bogus", "weird"); code != 1 || !strings.Contains(er, "validation") {
		t.Fatalf("bogus kind: %d %q", code, er)
	}

	var def []struct {
		Identity    string   `json:"identity"`
		Permissions []string `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(ok("acl", "dev")), &def); err != nil || len(def) == 0 {
		t.Fatalf("default acl: %v %v", def, err)
	}
	set := `[{"identity":"pat","permissions":["read","write","admin"]},{"identity":"everyone","permissions":["read","write"]},{"identity":"admins","permissions":["admin"]}]`
	ok("acl", "-set", set, "dev")
	var got []struct {
		Identity    string   `json:"identity"`
		Permissions []string `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(ok("acl", "dev")), &got); err != nil {
		t.Fatal(err)
	}
	byName := map[string][]string{}
	for _, e := range got {
		byName[e.Identity] = e.Permissions
	}
	if len(got) != 3 || !reflect.DeepEqual(byName["pat"], []string{"read", "write", "admin"}) ||
		!reflect.DeepEqual(byName["everyone"], []string{"read", "write"}) || !reflect.DeepEqual(byName["admins"], []string{"admin"}) {
		t.Fatalf("acl after set: %+v", got)
	}
	if code, _, _ := run("", "create", "-acl", "nope", "bad"); code != 2 {
		t.Fatalf("-acl nope: exit %d, want 2", code)
	}
	if code, _, _ := run("", "create", "-acl", `{"identity":"pat"}`, "bad"); code != 2 {
		t.Fatalf("-acl object: exit %d, want 2", code)
	}
	if code, _, _ := run("", "create", "-acl", `[{"who":"pat"}]`, "bad"); code != 2 {
		t.Fatalf("-acl unknown member: exit %d, want 2", code)
	}
	if code, _, _ := run("", "acl", "-set", "nope", "dev"); code != 2 {
		t.Fatalf("-set nope: exit %d, want 2", code)
	}
	// "[]" is an empty list, not the default: the creator keeps no right on the
	// channel (the implicit creator entry is retired, accesscheck spec 3.13).
	ok("create", "-acl", "[]", "closed")
	if code, _, errOut := run("", "acl", "closed"); code != 1 || !strings.Contains(errOut, "forbidden") {
		t.Fatalf("empty acl: exit %d, stderr %q; want exit 1, forbidden", code, errOut)
	}

	ok("rename", "dev", "dev2")
	if m := names(); m["dev2"] == "" || m["dev"] != "" {
		t.Fatalf("after rename: %v", m)
	}
	ok("subscribe", "-from", "oldest", "dev2")
	ok("unsubscribe", "dev2")

	ok("tags", "-subscribe", "deploy, env:*")
	var sets [][]string
	if err := json.Unmarshal([]byte(ok("tags")), &sets); err != nil || !reflect.DeepEqual(sets, [][]string{{"deploy", "env:*"}}) {
		t.Fatalf("tag sets: %v %v", sets, err)
	}
	ok("tags", "-unsubscribe", "deploy,env:*")
	if out := strings.TrimSpace(ok("tags")); out != "[]" {
		t.Fatalf("tag sets after unsubscribe: %q", out)
	}

	ok("delete", "-y", "dev2")
	if _, has := names()["dev2"]; has {
		t.Fatal("dev2 survived delete -y")
	}

	// The prompt names the channel; anything but y/yes cancels.
	code, out, er := run("n\n", "delete", "notes")
	if code != 1 || !strings.Contains(er, "delete cancelled") || !strings.Contains(out, `"notes"`) {
		t.Fatalf("declined: %d out %q err %q", code, out, er)
	}
	if _, has := names()["notes"]; !has {
		t.Fatal("notes deleted despite n")
	}
	if code, _, er := run("", "delete", "notes"); code != 1 || !strings.Contains(er, "delete cancelled") {
		t.Fatalf("empty stdin must cancel: %d %q", code, er)
	}
	if code, _, er := run("y\n", "delete", "notes"); code != 0 {
		t.Fatalf("confirmed: %d %q", code, er)
	}
	if _, has := names()["notes"]; has {
		t.Fatal("notes survived y")
	}
	// A channel the list lacks (missing, or one the caller holds no entry on)
	// still prompts, without a kind; the Bus answers not_found after the y.
	if code, out, er := run("y\n", "delete", "nosuch"); code != 1 || !strings.Contains(er, "not_found") || !strings.Contains(out, `Channel "nosuch" and everything`) {
		t.Fatalf("missing channel: %d out %q err %q", code, out, er)
	}
	// A retried keyed delete replays: the channel is gone, so the pre-check
	// leaves the answer to the bus, which replays the first result.
	ok("create", "gone")
	if code, _, er := run("y\n", "delete", "-key", "dk", "gone"); code != 0 {
		t.Fatalf("keyed delete: %d %q", code, er)
	}
	if code, out, er := run("", "delete", "-key", "dk", "gone"); code != 0 || out != "" {
		t.Fatalf("keyed delete retried: %d out %q err %q, want the replay", code, out, er)
	}
	// A key never used on a channel that never existed is still not_found.
	if code, _, er := run("", "delete", "-key", "other", "nosuch"); code != 1 || !strings.Contains(er, "not_found") {
		t.Fatalf("keyed delete of a missing channel: %d %q", code, er)
	}
}

func TestChannelsUsage(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	run := func(args ...string) int {
		var o, er bytes.Buffer
		return RunChannels(ctx, args, e.Environ, strings.NewReader(""), &o, &er)
	}
	for _, args := range [][]string{
		nil,
		{"bogus"},
		{"list", "extra"},
		{"list", "-filter", "x"},
		{"create"},
		{"create", "a", "b"},
		{"create", "-nope", "a"},
		{"delete"},
		{"rename", "a"},
		{"acl"},
		{"acl", "-set"},
		{"subscribe"},
		{"unsubscribe"},
		{"tags", "extra"},
		{"tags", "-subscribe", "a", "-unsubscribe", "b"},
	} {
		if code := run(args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	for _, args := range [][]string{{"-h"}, {"list", "-h"}, {"create", "-h"}, {"tags", "-h"}} {
		if code := run(args...); code != 0 {
			t.Errorf("%v: exit %d, want 0", args, code)
		}
	}
	// A bad ACL never reached the bus: nothing was created.
	c := e.conn(t, "pat")
	cs, err := c.Bus.ListChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range cs {
		if x.Name != "general" {
			t.Errorf("unexpected channel %q after usage errors", x.Name)
		}
	}
}

func TestChannelsACLGrammar(t *testing.T) {
	for _, s := range []string{"", "nope", "null", `{"identity":"a"}`, `[1]`, `[{"identity":"a","extra":1}]`, `[] []`, `[{"identity":"a","permissions":"read"}]`} {
		if _, err := opParseACL(s); err == nil {
			t.Errorf("opParseACL(%q) accepted", s)
		}
	}
	got, err := opParseACL(`[]`)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("[]: %v %v", got, err)
	}
}
