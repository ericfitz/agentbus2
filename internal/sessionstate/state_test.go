package sessionstate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/procs"
)

type fakeTable map[int]int64 // pid -> start time; absent = gone

func (f fakeTable) Parent(pid int) (int, error)  { return 1, nil }
func (f fakeTable) Comm(pid int) (string, error) { return "x", nil }
func (f fakeTable) StartTime(pid int) (int64, error) {
	s, ok := f[pid]
	if !ok {
		return 0, procs.ErrGone
	}
	return s, nil
}

func sample(session string) File {
	return File{
		Session: session, Identity: "alice", Profile: "default", DisplayName: "Alice",
		ResumeToken: "tok-" + session, HarnessPID: 10, HarnessStart: 5,
		HarnessClient: "claude-code", HarnessClientVersion: "1.2.3", MCPPID: 20,
		Primary: true, OpenedAt: 100,
	}
}

func TestStateFileHoldsResumeToken(t *testing.T) {
	dir := t.TempDir()
	want := sample("s1")
	if err := Write(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := Read(dir, "s1")
	if err != nil || got != want {
		t.Fatalf("Read = %+v, %v; want %+v", got, err, want)
	}
	if got.ResumeToken != "tok-s1" {
		t.Fatalf("resume token = %q", got.ResumeToken)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(Dir(dir), "s1.json"))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("file mode = %v, %v", fi.Mode(), err)
		}
		di, err := os.Stat(Dir(dir))
		if err != nil || di.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode = %v, %v", di.Mode(), err)
		}
	}
	ents, _ := os.ReadDir(Dir(dir))
	if len(ents) != 1 || ents[0].Name() != "s1.json" {
		t.Fatalf("entries = %v", ents)
	}
}

func TestWriteRequiresSession(t *testing.T) {
	dir := t.TempDir()
	for _, s := range []string{"", ".", "..", "a/b", `a\b`, "../x", "a\x00b"} {
		f := sample("x")
		f.Session = s
		if err := Write(dir, f); err == nil {
			t.Errorf("Write(session %q) succeeded", s)
		}
		if _, err := Read(dir, s); err == nil {
			t.Errorf("Read(session %q) succeeded", s)
		}
		if err := Remove(dir, s); err == nil {
			t.Errorf("Remove(session %q) succeeded", s)
		}
	}
	if ents, err := os.ReadDir(Dir(dir)); err == nil && len(ents) != 0 {
		t.Fatalf("entries left: %v", ents)
	}
}

func TestReadStrict(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"unknown":  `{"session":"unknown","bogus":1}`,
		"trailing": `{"session":"trailing"} {}`,
		"garbage":  `not json`,
		"mismatch": `{"session":"other"}`,
		"noname":   `{}`,
	} {
		if err := os.WriteFile(filepath.Join(Dir(dir), name+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(dir, name); err == nil {
			t.Errorf("Read(%s) accepted %s", name, body)
		}
	}
	if _, err := Read(dir, "absent"); err == nil {
		t.Error("Read of an absent file succeeded")
	}
}

func TestForHarnessPicksPrimary(t *testing.T) {
	mk := func(session string, pid int, start int64, primary bool, opened int64) File {
		f := sample(session)
		f.HarnessPID, f.HarnessStart, f.Primary, f.OpenedAt = pid, start, primary, opened
		return f
	}
	h := procs.Ref{Pid: 10, Start: 5}
	cases := []struct {
		name  string
		files []File
		h     procs.Ref
		want  string
	}{
		{"later one primary", []File{mk("a", 10, 5, false, 1), mk("b", 10, 5, false, 2), mk("c", 10, 5, true, 3)}, h, "c"},
		{"two primary, earliest", []File{mk("z", 10, 5, true, 9), mk("a", 10, 5, true, 4), mk("m", 10, 5, false, 1)}, h, "a"},
		{"two primary, same opened, session order", []File{mk("b", 10, 5, true, 4), mk("a", 10, 5, true, 4)}, h, "a"},
		{"none primary", []File{mk("b", 10, 5, false, 7), mk("a", 10, 5, false, 3), mk("c", 10, 5, false, 3)}, h, "a"},
		{"other harness ignored", []File{mk("x", 11, 5, true, 1), mk("y", 10, 6, true, 1), mk("w", 10, 5, false, 8)}, h, "w"},
		{"zero start matches any", []File{mk("a", 10, 5, true, 1)}, procs.Ref{Pid: 10}, "a"},
		{"zero stored start matches any", []File{mk("a", 10, 0, true, 1)}, h, "a"},
	}
	for _, c := range cases {
		dir := t.TempDir()
		for _, f := range c.files {
			if err := Write(dir, f); err != nil {
				t.Fatal(err)
			}
		}
		got, ok, err := ForHarness(dir, c.h)
		if err != nil || !ok || got.Session != c.want {
			t.Errorf("%s: ForHarness = %q, %v, %v; want %q", c.name, got.Session, ok, err, c.want)
		}
	}
	dir := t.TempDir()
	if err := Write(dir, mk("a", 11, 5, true, 1)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ForHarness(dir, h); ok || err != nil {
		t.Errorf("no match: ok=%v err=%v", ok, err)
	}
	if _, ok, err := ForHarness(dir, procs.Ref{}); ok || err != nil {
		t.Errorf("zero ref: ok=%v err=%v", ok, err)
	}
	if _, ok, err := ForHarness(t.TempDir(), h); ok || err != nil {
		t.Errorf("absent dir: ok=%v err=%v", ok, err)
	}
}

func TestSweep(t *testing.T) {
	dir := t.TempDir()
	mk := func(session string, harness, mcp int) {
		f := sample(session)
		f.HarnessPID, f.HarnessStart, f.MCPPID = harness, 5, mcp
		if err := Write(dir, f); err != nil {
			t.Fatal(err)
		}
	}
	mk("bothgone", 10, 20)
	mk("restart", 11, 21) // harness alive, MCP dead
	mk("mcplive", 12, 22) // harness dead, MCP alive
	mk("allalive", 13, 23)
	mk("reused", 14, 24) // harness pid reused (start differs), MCP dead
	tbl := fakeTable{11: 5, 22: 1, 13: 5, 23: 1, 14: 99}
	n, err := Sweep(dir, tbl)
	if err != nil || n != 2 {
		t.Fatalf("Sweep = %d, %v; want 2", n, err)
	}
	for s, want := range map[string]bool{"bothgone": false, "restart": true, "mcplive": true, "allalive": true, "reused": false} {
		_, err := Read(dir, s)
		if (err == nil) != want {
			t.Errorf("%s: present=%v want %v", s, err == nil, want)
		}
	}
	n, err = Sweep(dir, tbl) // second run removes nothing
	if err != nil || n != 0 {
		t.Fatalf("second Sweep = %d, %v", n, err)
	}
	if n, err := Sweep(t.TempDir(), tbl); n != 0 || err != nil {
		t.Fatalf("absent dir Sweep = %d, %v", n, err)
	}
}

func TestListSkipsMalformed(t *testing.T) {
	dir := t.TempDir()
	if got, err := List(dir); err != nil || len(got) != 0 {
		t.Fatalf("absent dir: %v, %v", got, err)
	}
	b, a, c := sample("b"), sample("a"), sample("c")
	b.OpenedAt, a.OpenedAt, c.OpenedAt = 1, 2, 2
	for _, f := range []File{a, b, c} {
		if err := Write(dir, f); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(Dir(dir), "bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(Dir(dir), "note.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := List(dir)
	if err != nil || len(got) != 3 || got[0].Session != "b" || got[1].Session != "a" || got[2].Session != "c" {
		t.Fatalf("List = %+v, %v", got, err)
	}
	// Nothing parsed: the error is reported.
	dir2 := t.TempDir()
	if err := os.MkdirAll(Dir(dir2), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(Dir(dir2), "bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := List(dir2); err == nil || len(got) != 0 {
		t.Fatalf("only malformed: %v, %v", got, err)
	}
}

func TestRemoveAbsentIsNil(t *testing.T) {
	dir := t.TempDir()
	if err := Remove(dir, "nope"); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, sample("s")); err != nil {
		t.Fatal(err)
	}
	if err := Remove(dir, "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir, "s"); err == nil {
		t.Fatal("file still readable")
	}
	if err := Remove(dir, "s"); err != nil {
		t.Fatal(err)
	}
}

func TestWriteAtomicReplacesWhole(t *testing.T) {
	dir := t.TempDir()
	first := sample("s")
	if err := Write(dir, first); err != nil {
		t.Fatal(err)
	}
	old, err := os.Open(filepath.Join(Dir(dir), "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Close() }()
	second := first
	second.ResumeToken = "second"
	second.MCPPID = 99
	if err := Write(dir, second); err != nil {
		t.Fatal(err)
	}
	if got, err := Read(dir, "s"); err != nil || got != second {
		t.Fatalf("Read = %+v, %v", got, err)
	}
	if runtime.GOOS != "windows" { // the open handle still sees the old document
		buf := make([]byte, 4096)
		n, _ := old.Read(buf)
		if !strings.Contains(string(buf[:n]), "tok-s") || strings.Contains(string(buf[:n]), "second") {
			t.Fatalf("old reader saw %q", buf[:n])
		}
	}
	ents, _ := os.ReadDir(Dir(dir))
	if len(ents) != 1 || ents[0].Name() != "s.json" {
		t.Fatalf("entries = %v", ents)
	}
}

func TestWriteFailureLeavesPreviousFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a read-only directory that is enforced")
	}
	dir := t.TempDir()
	first := sample("s")
	if err := Write(dir, first); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(Dir(dir), 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(Dir(dir), 0o700) }()
	second := first
	second.ResumeToken = "second"
	if err := Write(dir, second); err == nil {
		t.Fatal("Write into a read-only directory succeeded")
	}
	_ = os.Chmod(Dir(dir), 0o700)
	if got, err := Read(dir, "s"); err != nil || got != first {
		t.Fatalf("previous file changed: %+v, %v", got, err)
	}
	ents, _ := os.ReadDir(Dir(dir))
	if len(ents) != 1 || ents[0].Name() != "s.json" {
		t.Fatalf("entries = %v", ents)
	}
}
