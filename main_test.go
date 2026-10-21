package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/version"
)

func TestRunVersion(t *testing.T) {
	for _, arg := range []string{"version", "-v", "--version"} {
		var out, errb bytes.Buffer
		if code := run([]string{arg}, &out, &errb); code != 0 {
			t.Fatalf("%s: exit %d, stderr %q", arg, code, errb.String())
		}
		if got := strings.TrimSpace(out.String()); got != version.Version {
			t.Fatalf("%s: printed %q, want %q", arg, got, version.Version)
		}
	}
}

func TestRunHelp(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		var out, errb bytes.Buffer
		if code := run([]string{arg}, &out, &errb); code != 0 {
			t.Fatalf("%s: exit %d", arg, code)
		}
		if !strings.HasPrefix(out.String(), "usage: agentbus2 <command>") || errb.Len() != 0 {
			t.Fatalf("%s: stdout %q stderr %q", arg, out.String(), errb.String())
		}
	}
}

func TestRunRejectsUnknownAndEmpty(t *testing.T) {
	for _, args := range [][]string{nil, {"frobnicate"}} {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != 2 {
			t.Fatalf("%v: exit %d, want 2", args, code)
		}
		if out.Len() != 0 || !strings.Contains(errb.String(), "usage: agentbus2") {
			t.Fatalf("%v: stdout %q stderr %q", args, out.String(), errb.String())
		}
	}
	var errb bytes.Buffer
	run([]string{"frobnicate"}, io.Discard, &errb)
	if !strings.Contains(errb.String(), `unknown command "frobnicate"`) {
		t.Fatalf("stderr %q lacks the unknown-command line", errb.String())
	}
}

func TestUsageListsServer(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"help"}, &out, io.Discard); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "  server ") {
		t.Fatalf("usage %q lacks the server command", out.String())
	}
}

func TestRunServerRejectsMissingFlags(t *testing.T) {
	var errb bytes.Buffer
	if code := run([]string{"server"}, io.Discard, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "-listen is required") {
		t.Fatalf("stderr %q", errb.String())
	}
}
