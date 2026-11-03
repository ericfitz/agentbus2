//go:build !windows

package credential

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
)

// TestLoadFileFollowsSymlinkAndChecksMode: a Kubernetes-style secret mount is a
// symlink to a regular file; the target's mode decides (Review Focus 3).
func TestLoadFileFollowsSymlinkAndChecksMode(t *testing.T) {
	dir := t.TempDir()
	k, err := keys.Generate(keys.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
	pemb, err := keys.EncodePrivatePEM(k)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, b []byte, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	link := func(name, target string) string {
		p := filepath.Join(dir, name)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("0400 target through a symlink loads", func(t *testing.T) {
		got, alg, err := LoadFile(link("ok.link", write("ok.pem", pemb, 0o400)))
		if err != nil || alg != keys.EdDSA {
			t.Fatalf("LoadFile = %v, %v", alg, err)
		}
		a, _ := keys.KID(k.Public())
		b, _ := keys.KID(got.Public())
		if a != b {
			t.Fatal("loaded a different key")
		}
	})
	t.Run("0644 target is refused", func(t *testing.T) {
		p := link("wide.link", write("wide.pem", pemb, 0o644))
		_, _, err := LoadFile(p)
		if !errors.Is(err, ErrNotOwnerOnly) {
			t.Fatalf("err %v, want ErrNotOwnerOnly", err)
		}
		if !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "must be mode 0600") {
			t.Fatalf("err %q does not name the path and the mode rule", err)
		}
	})
	pubDER, err := x509.MarshalPKIXPublicKey(k.Public())
	if err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{
		"directory":  dir,
		"empty":      write("empty.pem", nil, 0o600),
		"public key": write("pub.pem", pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}), 0o600),
		"oversized":  write("big.pem", bytes.Repeat([]byte("a"), 64*1024+1), 0o600),
		"missing":    filepath.Join(dir, "absent"),
	} {
		t.Run(name+" is refused", func(t *testing.T) {
			_, _, err := LoadFile(p)
			if err == nil || !strings.Contains(err.Error(), p) {
				t.Fatalf("err %v, want an error naming %s", err, p)
			}
			if errors.Is(err, ErrNotOwnerOnly) {
				t.Fatalf("err %v wraps ErrNotOwnerOnly", err)
			}
		})
	}
}

// TestLoadFileUnreadableSaysWhatToFix: a key file this user cannot read fails
// at the open with an error naming the path and the fix (on Windows a
// read-denied DACL fails the same way).
func TestLoadFileUnreadableSaysWhatToFix(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k.pem")
	if err := os.WriteFile(p, []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	if f, err := os.Open(p); err == nil { // running as root
		_ = f.Close()
		t.Skip("the file is readable here")
	}
	_, _, err := LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "must be readable by the user") {
		t.Fatalf("err = %v", err)
	}
}
