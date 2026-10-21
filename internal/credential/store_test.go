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
