package credential

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	for _, alg := range []keys.Algorithm{keys.EdDSA, keys.ES256} {
		t.Run(string(alg), func(t *testing.T) {
			s := Store{Dir: filepath.Join(t.TempDir(), "credentials", "lab")}
			k, err := keys.Generate(alg)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Save("pat", k); err != nil {
				t.Fatal(err)
			}
			got, galg, err := s.Load("pat")
			if err != nil {
				t.Fatal(err)
			}
			if galg != alg {
				t.Errorf("algorithm %s, want %s", galg, alg)
			}
			a, _ := keys.KID(k.Public())
			b, _ := keys.KID(got.Public())
			if a != b {
				t.Errorf("loaded a different key")
			}
			ents, _ := os.ReadDir(s.Dir)
			if len(ents) != 1 || ents[0].Name() != "pat" {
				t.Errorf("directory holds %v, want only pat", ents)
			}
			if runtime.GOOS != "windows" {
				fi, _ := os.Stat(s.Path("pat"))
				if fi.Mode().Perm() != 0o600 {
					t.Errorf("file mode %v", fi.Mode().Perm())
				}
				di, _ := os.Stat(s.Dir)
				if di.Mode().Perm() != 0o700 {
					t.Errorf("dir mode %v", di.Mode().Perm())
				}
			}
			if !s.Exists("pat") || s.Exists("sam") {
				t.Errorf("Exists wrong")
			}
		})
	}
}

func TestSaveReplacesWhole(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	k1, _ := keys.Generate(keys.EdDSA)
	k2, _ := keys.Generate(keys.ES256)
	if err := s.Save("pat", k1); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("pat", k2); err != nil {
		t.Fatal(err)
	}
	_, alg, err := s.Load("pat")
	if err != nil || alg != keys.ES256 {
		t.Fatalf("alg %v err %v", alg, err)
	}
}

func TestLoadRefusesWideMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix modes")
	}
	s := Store{Dir: t.TempDir()}
	k, _ := keys.Generate(keys.EdDSA)
	if err := s.Save("pat", k); err != nil {
		t.Fatal(err)
	}
	for _, m := range []os.FileMode{0o640, 0o604, 0o660, 0o644, 0o700} {
		if err := os.Chmod(s.Path("pat"), m); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Load("pat"); err == nil {
			t.Errorf("mode %o accepted", m)
		}
	}
	for _, m := range []os.FileMode{0o600, 0o400} {
		if err := os.Chmod(s.Path("pat"), m); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Load("pat"); err != nil {
			t.Errorf("mode %o refused: %v", m, err)
		}
	}
}

func TestLoadRejectsName(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	k, _ := keys.Generate(keys.EdDSA)
	for _, name := range []string{"a/b", "", ".", "..", `a\b`, "a\x00b", "a:b", "con", "NUL.txt", "com1"} {
		if _, _, err := s.Load(name); err == nil {
			t.Errorf("Load(%q) accepted", name)
		}
		if err := s.Save(name, k); err == nil {
			t.Errorf("Save(%q) accepted", name)
		}
		if s.Exists(name) {
			t.Errorf("Exists(%q)", name)
		}
	}
}

func TestLoadRejectsBadFiles(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if _, _, err := s.Load("nobody"); err == nil {
		t.Error("absent file accepted")
	}
	if err := os.WriteFile(s.Path("junk"), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Load("junk"); err == nil {
		t.Error("junk accepted")
	}
}

func TestListSorted(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "absent")}
	if got, err := s.List(); err != nil || got != nil {
		t.Fatalf("absent dir: %v %v", got, err)
	}
	s = Store{Dir: t.TempDir()}
	k, _ := keys.Generate(keys.EdDSA)
	for _, n := range []string{"sam", "kim", "pat"} {
		if err := s.Save(n, k); err != nil {
			t.Fatal(err)
		}
	}
	// A crashed write's leftover and a subdirectory are not credentials.
	if err := os.WriteFile(filepath.Join(s.Dir, "pat.tmp-0123456789abcdef"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(s.Dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"kim", "pat", "sam"}; !reflect.DeepEqual(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}
}

func TestPendingLifecycle(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "credentials", "lab")}
	if k, alg, ok, err := s.LoadPending("pat"); k != nil || alg != "" || ok || err != nil {
		t.Fatalf("absent pending = %v %q %v %v", k, alg, ok, err)
	}
	k, _ := keys.Generate(keys.ES256)
	if err := s.SavePending("pat", k); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(s.Dir, ".pending", "pat"); s.PendingPath("pat") != want {
		t.Fatalf("PendingPath = %s", s.PendingPath("pat"))
	}
	got, alg, ok, err := s.LoadPending("pat")
	if err != nil || !ok || alg != keys.ES256 {
		t.Fatalf("LoadPending: %v %v %s", err, ok, alg)
	}
	if a, b := mustKID(t, k), mustKID(t, got); a != b {
		t.Fatal("loaded a different key")
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(s.PendingPath("pat"))
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("file mode %v", fi.Mode().Perm())
		}
		di, _ := os.Stat(filepath.Dir(s.PendingPath("pat")))
		if di.Mode().Perm() != 0o700 {
			t.Errorf("dir mode %v", di.Mode().Perm())
		}
	}
	if ids, _ := s.List(); len(ids) != 0 || s.Exists("pat") {
		t.Fatalf("pending is visible: %v", ids)
	}
	if err := s.SavePending("kim", k); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitPending("pat", k); err != nil {
		t.Fatal(err)
	}
	if !s.Exists("pat") {
		t.Fatal("Exists is false after CommitPending")
	}
	if _, _, ok, _ := s.LoadPending("pat"); ok {
		t.Fatal("pending remains after CommitPending")
	}
	// kim's pending key keeps the directory; committing it removes the directory.
	if _, err := os.Stat(filepath.Join(s.Dir, ".pending")); err != nil {
		t.Fatalf("pending dir removed while in use: %v", err)
	}
	if err := s.CommitPending("kim", k); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, ".pending")); !os.IsNotExist(err) {
		t.Fatalf("empty pending dir remains: %v", err)
	}
	// With no pending file the registered key is saved as the credential.
	if err := s.CommitPending("zed", k); err != nil || !s.Exists("zed") {
		t.Fatalf("CommitPending of nothing: %v", err)
	}
	for _, bad := range []string{"", "..", "a/b"} {
		if s.SavePending(bad, k) == nil {
			t.Errorf("SavePending accepted %q", bad)
		}
		if _, _, _, err := s.LoadPending(bad); err == nil {
			t.Errorf("LoadPending accepted %q", bad)
		}
	}
}

func TestLoadPendingRefusesWideMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes are Unix only")
	}
	s := Store{Dir: t.TempDir()}
	k, _ := keys.Generate(keys.EdDSA)
	if err := s.SavePending("pat", k); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.PendingPath("pat"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.LoadPending("pat"); err == nil {
		t.Fatal("a wide-mode pending file was accepted")
	}
}

// Overlap: run A saves kA pending, run B replaces it with kB, A's 201 for kA
// commits kA (not kB) and leaves B's pending file.
func TestCommitPendingOtherKey(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	kA, _ := keys.Generate(keys.EdDSA)
	kB, _ := keys.Generate(keys.EdDSA)
	if err := s.SavePending("pat", kA); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePending("pat", kB); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitPending("pat", kA); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.Load("pat")
	if err != nil || mustKID(t, got) != mustKID(t, kA) {
		t.Fatalf("credential is not kA: %v", err)
	}
	p, _, ok, err := s.LoadPending("pat")
	if err != nil || !ok || mustKID(t, p) != mustKID(t, kB) {
		t.Fatalf("B's pending key was lost: %v %v", ok, err)
	}
	// The pending file is B's: committing kA again leaves it, and the
	// credential stays kA.
	if err := s.CommitPending("pat", kA); err != nil {
		t.Fatal(err)
	}
	if p, _, ok, err := s.LoadPending("pat"); err != nil || !ok || mustKID(t, p) != mustKID(t, kB) {
		t.Fatalf("B's pending key did not survive a second commit: %v %v", ok, err)
	}
	if err := s.DiscardPending("pat"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, _ := s.LoadPending("pat"); ok {
		t.Fatal("pending remains after DiscardPending")
	}
	if err := s.DiscardPending("pat"); err != nil {
		t.Fatalf("second DiscardPending: %v", err)
	}
}
