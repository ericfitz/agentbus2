package cmd

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClassifyListen(t *testing.T) {
	cases := []struct {
		listen  string
		local   bool
		wantErr bool
	}{
		{"127.0.0.1:0", true, false},
		{"127.1.2.3:80", true, false},
		{"[::1]:0", true, false},
		{"[::ffff:127.0.0.1]:0", true, false},
		{"localhost:0", true, false},
		{"unix:/tmp/x.sock", true, false},
		{"0.0.0.0:0", false, false},
		{":0", false, false},
		{"[::]:0", false, false},
		{"192.0.2.1:0", false, false},
		{"example.invalid:0", false, true},
		{"nonsense", false, true},
		{"unix:", false, true},
		{"", false, true},
		{"127.0.0.1", false, true},
	}
	for _, c := range cases {
		t.Run(c.listen, func(t *testing.T) {
			local, err := ClassifyListen(c.listen)
			if (err != nil) != c.wantErr {
				t.Fatalf("ClassifyListen(%q) error = %v, want error %v", c.listen, err, c.wantErr)
			}
			if local != c.local {
				t.Fatalf("ClassifyListen(%q) = %v, want %v", c.listen, local, c.local)
			}
		})
	}
}

func TestListenUnixAndStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bus.sock")
	l, err := Listen("unix:" + path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, want 0600", fi.Mode().Perm())
	}
	accepted := make(chan struct{})
	go func() {
		if c, err := l.Accept(); err == nil {
			_ = c.Close()
		}
		close(accepted)
	}()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	<-accepted

	// A live socket is not taken over.
	if l2, err := Listen("unix:" + path); err == nil {
		_ = l2.Close()
		t.Fatal("second Listen on a live socket succeeded")
	}
	if _, err := net.Dial("unix", path); err != nil {
		t.Fatalf("live socket was disturbed: %v", err)
	}

	// Closing a unix listener removes the file; recreate a stale one.
	if ul, ok := l.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	_ = l.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stale socket file missing: %v", err)
	}
	l3, err := Listen("unix:" + path)
	if err != nil {
		t.Fatalf("Listen over a stale socket: %v", err)
	}
	_ = l3.Close()
}

func TestListenRefusesNonSocketFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if l, err := Listen("unix:" + path); err == nil {
		_ = l.Close()
		t.Fatal("Listen replaced a regular file")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("regular file was removed")
	}
}

func TestListenTCPAndEmptyUnix(t *testing.T) {
	l, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	if l, err := Listen("unix:"); err == nil {
		_ = l.Close()
		t.Fatal("empty unix path accepted")
	}
}

// Two servers starting together on one stale socket: without serialization
// both probe the stale file as refused, the first removes it and listens, and
// the second then removes the first's live socket and listens too, orphaning
// the first. Exactly one may succeed, and the survivor must be reachable.
func TestListenConcurrentStaleTakeover(t *testing.T) {
	// A short directory: the test name would push t.TempDir past the
	// sun_path limit (104 bytes on macOS).
	dir, err := os.MkdirTemp("", "ab")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "bus.sock")
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = stale.Close()

	type result struct {
		l   net.Listener
		err error
	}
	second := make(chan result, 1)
	var started atomic.Bool
	beforeStaleRemove = func() {
		if !started.CompareAndSwap(false, true) {
			return
		}
		go func() {
			l, err := Listen("unix:" + path)
			second <- result{l, err}
		}()
		// Give the second Listen time to run to completion when nothing
		// holds it back; with the lock it waits for this one.
		select {
		case r := <-second:
			second <- r
		case <-time.After(300 * time.Millisecond):
		}
	}
	t.Cleanup(func() { beforeStaleRemove = func() {} })

	first, err1 := Listen("unix:" + path)
	r := <-second
	for _, l := range []net.Listener{first, r.l} {
		if l != nil {
			t.Cleanup(func() { _ = l.Close() })
		}
	}
	if (err1 == nil) == (r.err == nil) {
		t.Fatalf("exactly one Listen must succeed: first err %v, second err %v", err1, r.err)
	}
	if lost := errors.Join(err1, r.err); !strings.Contains(lost.Error(), "in use") {
		t.Fatalf("the losing Listen failed for another reason: %v", lost)
	}
	live := first
	if live == nil {
		live = r.l
	}
	go func() {
		if c, err := live.Accept(); err == nil {
			_ = c.Close()
		}
	}()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("the surviving listener is unreachable: %v", err)
	}
	_ = c.Close()
}

// A hostname is local only when every address it resolves to is loopback.
func TestClassifyListenHostnameResolution(t *testing.T) {
	ip := func(s string) net.IPAddr { return net.IPAddr{IP: net.ParseIP(s)} }
	answers := map[string][]net.IPAddr{
		"loop.test":   {ip("127.0.0.1"), ip("::1")},
		"mapped.test": {ip("::ffff:127.0.0.1")},
		"public.test": {ip("192.0.2.1")},
		"mixed.test":  {ip("127.0.0.1"), ip("192.0.2.1")},
		"empty.test":  {},
	}
	lookupIPAddr = func(_ context.Context, host string) ([]net.IPAddr, error) {
		a, ok := answers[host]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		return a, nil
	}
	t.Cleanup(func() { lookupIPAddr = net.DefaultResolver.LookupIPAddr })
	cases := []struct {
		host           string
		local, wantErr bool
	}{
		{"loop.test", true, false},
		{"mapped.test", true, false},
		{"public.test", false, false},
		{"mixed.test", false, false},
		{"empty.test", false, true},
		{"missing.test", false, true},
	}
	for _, c := range cases {
		local, err := ClassifyListen(c.host + ":0")
		if (err != nil) != c.wantErr || local != c.local {
			t.Errorf("ClassifyListen(%s:0) = %v, %v; want %v, error %v", c.host, local, err, c.local, c.wantErr)
		}
	}
}
