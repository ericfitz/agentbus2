package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/server"
)

// syncBuf is a bytes.Buffer safe for a writer goroutine and a polling reader.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var listeningRE = regexp.MustCompile(`agentbus2 server listening on (\S+) \((sqlite|postgres)\)`)

// runServer starts Run in a goroutine and returns the bound address, the
// cancel function and a channel carrying the exit code.
func runServer(t *testing.T, args ...string) (addr string, cancel context.CancelFunc, done <-chan int, stdout *syncBuf) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stdout = &syncBuf{}
	stderr := &syncBuf{}
	ch := make(chan int, 1)
	go func() { ch <- Run(ctx, args, stdout, stderr) }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if m := listeningRE.FindStringSubmatch(stdout.String()); m != nil {
			return m[1], cancel, ch, stdout
		}
		select {
		case code := <-ch:
			t.Fatalf("Run exited %d before listening: stderr %q", code, stderr.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("no listening line; stdout %q stderr %q", stdout.String(), stderr.String())
	return
}

// pending Eric: Milestone 4 item 3 (interim flags until Decision 85).
func TestRunFlags(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "ab2.db")
	cases := []struct {
		name string
		args []string
		code int
		errs []string
	}{
		{"none", nil, 2, []string{"-listen is required, for example 127.0.0.1:0", "exactly one of -sqlite or -postgres is required"}},
		{"both stores", []string{"-listen", "127.0.0.1:0", "-sqlite", tmp, "-postgres", "postgres://x"}, 2, []string{"exactly one of -sqlite or -postgres is required"}},
		{"no store", []string{"-listen", "127.0.0.1:0"}, 2, []string{"exactly one of -sqlite or -postgres is required"}},
		{"no listen", []string{"-sqlite", tmp}, 2, []string{"-listen is required"}},
		{"unknown flag", []string{"-frob"}, 2, nil},
		{"stray argument", []string{"-listen", "127.0.0.1:0", "-sqlite", tmp, "extra"}, 2, []string{`unexpected argument "extra"`}},
	}
	for _, tc := range cases {
		var out, errb bytes.Buffer
		if got := Run(context.Background(), tc.args, &out, &errb); got != tc.code {
			t.Fatalf("%s: exit %d, want %d (stderr %q)", tc.name, got, tc.code, errb.String())
		}
		for _, want := range tc.errs {
			if !strings.Contains(errb.String(), want) {
				t.Errorf("%s: stderr %q lacks %q", tc.name, errb.String(), want)
			}
		}
	}

	var out, errb bytes.Buffer
	if got := Run(context.Background(), []string{"-h"}, &out, &errb); got != 0 {
		t.Fatalf("-h: exit %d", got)
	}
	if !strings.Contains(out.String(), "-listen") || !strings.Contains(out.String(), "-sqlite") {
		t.Fatalf("-h usage %q", out.String())
	}

	addr, cancel, done, stdout := runServer(t, "-listen", "127.0.0.1:0", "-sqlite", tmp)
	if addr == "" || !strings.Contains(stdout.String(), "(sqlite)") {
		t.Fatalf("stdout %q", stdout.String())
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("clean shutdown exit %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestRunRejectsUnopenableStore(t *testing.T) {
	var out, errb bytes.Buffer
	dir := t.TempDir() // a directory is not a database file
	if got := Run(context.Background(), []string{"-listen", "127.0.0.1:0", "-sqlite", dir}, &out, &errb); got != 1 {
		t.Fatalf("exit %d, want 1 (stderr %q)", got, errb.String())
	}
	if !strings.Contains(errb.String(), "opening the sqlite store") {
		t.Fatalf("stderr %q", errb.String())
	}
}

// The counts fail when a Milestone 5 task declares an operation without
// updating this test.
func TestRunRefusesEveryProtectedOperation(t *testing.T) {
	addr, _, _, _ := runServer(t, "-listen", "127.0.0.1:0", "-sqlite", filepath.Join(t.TempDir(), "ab2.db"))
	doc := server.Spec()
	subst := strings.NewReplacer("{channel}", "dev", "{session}", "s", "{id}", "1", "{patterns}", "deploy")
	protected, open := 0, 0
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			sec := doc.Security
			if op.Security != nil {
				sec = *op.Security
			}
			isProtected := len(sec) > 0
			url := "http://" + addr + subst.Replace(path)
			if strings.Contains(url, "{") {
				t.Fatalf("%s %s: unsubstituted template parameter", method, path)
			}
			var body io.Reader
			if op.RequestBody != nil {
				body = strings.NewReader("{}")
			}
			req, err := http.NewRequest(method, url, body)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer x")
			req.Header.Set("X-Agentbus2-Test-Identity", "pat")
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if isProtected {
				protected++
				if resp.StatusCode != http.StatusUnauthorized {
					t.Errorf("%s %s: status %d, want 401: %s", method, path, resp.StatusCode, raw)
					continue
				}
				if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
					t.Errorf("%s %s: content type %q", method, path, ct)
				}
				var p struct {
					Code string `json:"code"`
				}
				if err := json.Unmarshal(raw, &p); err != nil || p.Code != "unauthenticated" {
					t.Errorf("%s %s: body %s", method, path, raw)
				}
			} else {
				open++
				if resp.StatusCode != http.StatusOK {
					t.Errorf("%s %s: status %d, want 200: %s", method, path, resp.StatusCode, raw)
				}
			}
		}
	}
	if protected != 30 || open != 3 || protected+open != 33 {
		t.Fatalf("operations: %d protected, %d open; want 30 and 3", protected, open)
	}
}
