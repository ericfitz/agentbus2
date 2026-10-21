package cmd

import (
	"net"
	"os"
	"path/filepath"
	"testing"
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
