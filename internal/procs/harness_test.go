package procs

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestFindHarness(t *testing.T) {
	cases := []struct {
		name string
		tbl  fakeTable
		want Ref
		err  error
	}{
		{"wait via zsh to claude", fakeTable{10: {20, "agentbus", 1}, 20: {30, "/bin/zsh", 2}, 30: {40, "claude", 3}, 40: {1, "-zsh", 4}}, Ref{30, 3}, nil},
		{"wait direct to claude", fakeTable{10: {30, "agentbus", 1}, 30: {40, "claude", 3}}, Ref{30, 3}, nil},
		{"orphaned shell", fakeTable{10: {20, "agentbus", 1}, 20: {1, "zsh", 2}}, Ref{}, ErrGone},
		{"login shell then harness", fakeTable{10: {20, "agentbus", 1}, 20: {30, "-zsh", 2}, 30: {40, "login", 3}}, Ref{30, 3}, nil},
		{"nested shells", fakeTable{10: {20, "agentbus", 1}, 20: {25, "bash", 2}, 25: {30, "sh", 3}, 30: {1, "codex", 7}}, Ref{30, 7}, nil},
		{"windows hook via git bash", fakeTable{10: {20, "agentbus.exe", 1}, 20: {30, "bash.exe", 2}, 30: {40, "claude.exe", 3}, 40: {1, "explorer.exe", 4}}, Ref{30, 3}, nil},
		{"windows cmd and powershell", fakeTable{10: {20, "agentbus.exe", 1}, 20: {25, "cmd.exe", 2}, 25: {30, "PowerShell.EXE", 3}, 30: {35, "pwsh.exe", 4}, 35: {1, "codex.exe", 5}}, Ref{35, 5}, nil},
		{"exe suffix is not a shell marker", fakeTable{10: {20, "agentbus.exe", 1}, 20: {1, "claude.exe", 2}}, Ref{20, 2}, nil},
		{"parent vanished", fakeTable{10: {20, "agentbus", 1}}, Ref{}, ErrGone},
	}
	for _, c := range cases {
		got, err := FindHarness(c.tbl, 10)
		if !errors.Is(err, c.err) || got != c.want {
			t.Errorf("%s: FindHarness = %v, %v; want %v, %v", c.name, got, err, c.want, c.err)
		}
	}
}

func TestIsShell(t *testing.T) {
	for name, want := range map[string]bool{
		"zsh": true, "-zsh": true, "/bin/bash": true, "fish": true,
		"cmd": true, "cmd.exe": true, "CMD.EXE": true, "powershell.exe": true, "PowerShell.EXE": true, "pwsh": true, "pwsh.exe": true, "bash.exe": true,
		"claude": false, "claude.exe": false, "codex": false, "cmdx": false, "cmd.exe.exe": false, "": false,
	} {
		if got := isShell(name); got != want {
			t.Errorf("isShell(%q) = %v, want %v", name, got, want)
		}
	}
}

// flipTable answers like the wrapped table for the first StartTime call, then
// reports every pid gone.
type flipTable struct {
	fakeTable
	calls *atomic.Int32
}

func (f flipTable) StartTime(pid int) (int64, error) {
	if f.calls.Add(1) > 1 {
		return 0, ErrGone
	}
	return f.fakeTable.StartTime(pid)
}

func TestWatchHarnessCancelsWithCause(t *testing.T) {
	old := HarnessPollInterval
	HarnessPollInterval = 10 * time.Millisecond
	defer func() { HarnessPollInterval = old }()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	tbl := flipTable{fakeTable{30: {40, "claude", 3}}, new(atomic.Int32)}
	go WatchHarness(ctx, tbl, Ref{30, 3}, cancel)
	select {
	case <-ctx.Done():
		if !errors.Is(context.Cause(ctx), ErrHarnessGone) {
			t.Fatalf("cause = %v, want ErrHarnessGone", context.Cause(ctx))
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("not canceled within 200ms")
	}
}

func TestWatchHarnessReturnsWhenContextDone(t *testing.T) {
	old := HarnessPollInterval
	HarnessPollInterval = 10 * time.Millisecond
	defer func() { HarnessPollInterval = old }()
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan struct{})
	go func() {
		WatchHarness(ctx, fakeTable{30: {40, "claude", 3}}, Ref{30, 3}, cancel)
		close(done)
	}()
	cancel(nil)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WatchHarness did not return")
	}
	if context.Cause(ctx) != context.Canceled {
		t.Fatalf("cause = %v", context.Cause(ctx))
	}
}
