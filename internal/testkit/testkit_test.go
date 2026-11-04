package testkit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLockedBufferConcurrent(t *testing.T) {
	var b LockedBuffer
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 1000 {
				if _, err := b.Write([]byte("x")); err != nil {
					t.Error(err)
					return
				}
				_ = b.String() // a read racing the writers (run with -race)
			}
		}()
	}
	wg.Wait()
	if n := len(b.String()); n != 8000 {
		t.Fatalf("len(String()) = %d, want 8000", n)
	}
}

func TestProfileEnvLayout(t *testing.T) {
	e := NewProfileEnv(t, "FOO=bar", "BAZ=qux")
	root := filepath.Dir(e.Home)
	for _, d := range []string{e.Home, e.Config, e.Data} {
		if filepath.Dir(d) != root {
			t.Errorf("%s is not under %s", d, root)
		}
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("%s: not a directory (%v)", d, err)
		}
	}
	for _, want := range []string{"HOME=" + e.Home, "AGENTMESH_CONFIG_DIR=" + e.Config, "AGENTMESH_DATA_DIR=" + e.Data, "FOO=bar", "BAZ=qux"} {
		found := false
		for _, kv := range e.Environ {
			if kv == want {
				found = true
			}
		}
		if !found {
			t.Errorf("Environ lacks %q: %v", want, e.Environ)
		}
	}
}

func TestProfileEnvSave(t *testing.T) {
	e := NewProfileEnv(t)
	e.Save(t)
	if _, err := os.Stat(filepath.Join(e.Config, "config.json")); err != nil {
		t.Fatal(err)
	}
}

// keyProviderTags is the build-tag set of the four external key provider
// backends (M7_TAGS in scripts/m7-exit-checks.sh).
const keyProviderTags = "awskms,gcpkms,azurekms,hashivault"

// testkitImporters runs `go list` in dir with the given tags and GOOS (empty
// means the default) and returns one "<package> imports <path>" line for
// every non-test import of an internal/testkit package. A go list error
// fails the test.
func testkitImporters(t *testing.T, dir, tags, goos string) []string {
	t.Helper()
	args := []string{"list"}
	if tags != "" {
		args = append(args, "-tags="+tags)
	}
	args = append(args, "-f", "{{.ImportPath}} {{join .Imports \" \"}}", "./...")
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	// The caller's GOFLAGS, GOOS, GOARCH and GOWORK never reach the scan:
	// only the arguments pick the build.
	for _, kv := range os.Environ() {
		switch k, _, _ := strings.Cut(kv, "="); k {
		case "GOFLAGS", "GOOS", "GOARCH", "GOWORK":
		default:
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "GOWORK=off")
	if goos != "" {
		cmd.Env = append(cmd.Env, "GOOS="+goos)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list (tags %q, GOOS %q) in %s: %v\n%s", tags, goos, dir, err, stderr.String())
	}
	var found []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		for _, imp := range f[1:] {
			if strings.HasSuffix(imp, "/internal/testkit") {
				found = append(found, f[0]+" imports "+imp)
			}
		}
	}
	return found
}

// TestTestkitTestOnly fails when non-test code imports internal/testkit, in
// the default build, with the key provider tags and with them for windows.
func TestTestkitTestOnly(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, c := range []struct{ name, tags, goos string }{
		{"default", "", ""},
		{"tags", keyProviderTags, ""},
		{"tags-windows", keyProviderTags, "windows"},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, f := range testkitImporters(t, root, c.tags, c.goos) {
				t.Errorf("non-test code: %s", f)
			}
		})
	}
}

// TestTestkitScanSeesTaggedImport proves the scan: in a fixture module a
// tagged non-test file imports testkit; the untagged scan is clean and the
// tagged scan reports it.
func TestTestkitScanSeesTaggedImport(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":                             "module fixture\n\ngo 1.21\n",
		"internal/testkit/testkit.go":        "package testkit\n",
		"clean/clean.go":                     "package clean\n",
		"internal/server/cmd/backend_aws.go": "//go:build awskms\n\npackage cmd\n\nimport _ \"fixture/internal/testkit\"\n",
	}
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A caller's environment that would tag the build or break go list
	// must not reach the scan.
	t.Setenv("GOFLAGS", "-tags="+keyProviderTags)
	t.Setenv("GOOS", "nosuchos")
	if got := testkitImporters(t, dir, "", ""); len(got) != 0 {
		t.Fatalf("untagged scan reported %v, want none", got)
	}
	want := "fixture/internal/server/cmd imports fixture/internal/testkit"
	for _, goos := range []string{"", "windows"} {
		got := testkitImporters(t, dir, keyProviderTags, goos)
		if len(got) != 1 || got[0] != want {
			t.Fatalf("tagged scan (GOOS %q) = %v, want [%s]", goos, got, want)
		}
	}
}
