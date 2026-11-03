package credential

import (
	"crypto"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/fileacl"
	"github.com/ericfitz/agentbus2/internal/store"
)

// maxKeyFile bounds a credential file read; a PKCS#8 key is far smaller.
const maxKeyFile = 64 * 1024

// tempName matches the temporary names keys.WriteFile leaves behind after a crash.
var tempName = regexp.MustCompile(`\.tmp-[0-9a-f]{16}$`)

// Store is one profile's credential directory: one file per identity, named
// by the identity (profile.CredentialDir(configDir, profile)).
type Store struct{ Dir string }

// checkName applies the identity file name rule (store.FileNameRule): an
// identity names its credential and token files.
func checkName(identity string) error {
	if err := store.FileNameRule(identity); err != nil {
		return fmt.Errorf("credential: identity %q: %w", identity, err)
	}
	return nil
}

// Path is the credential file of identity.
func (s Store) Path(identity string) string { return filepath.Join(s.Dir, identity) }

// List returns the identities with a credential file, sorted; none when Dir is
// absent. Directories, temporary leftovers and names failing the rule are skipped.
func (s Store) List() ([]string, error) {
	ents, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("credential: list %s: %w", s.Dir, err)
	}
	var out []string
	for _, e := range ents {
		if !e.Type().IsRegular() || tempName.MatchString(e.Name()) || checkName(e.Name()) != nil {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// Exists reports whether identity has a credential file.
func (s Store) Exists(identity string) bool {
	if checkName(identity) != nil {
		return false
	}
	fi, err := os.Lstat(s.Path(identity))
	return err == nil && fi.Mode().IsRegular()
}

// Load reads and parses identity's private key. On Unix a file whose mode is
// wider than 0600 is refused.
func (s Store) Load(identity string) (crypto.Signer, keys.Algorithm, error) {
	if err := checkName(identity); err != nil {
		return nil, "", err
	}
	return loadFile(s.Path(identity))
}

// ErrNotOwnerOnly is wrapped by the refusal of a key file other users can read
// (a Unix mode wider than 0600; on Windows, LoadFile's DACL check).
var ErrNotOwnerOnly = errors.New("credential: the key file is readable by others")

// ownerOnlyError carries its message unchanged and matches ErrNotOwnerOnly.
type ownerOnlyError string

func (e ownerOnlyError) Error() string        { return string(e) }
func (e ownerOnlyError) Is(target error) bool { return target == ErrNotOwnerOnly }

// LoadFile reads the private key at path, a file the caller names (the MCP-only
// binary's -credential): a symlink is followed, so a mounted secret works, and
// the target must be a regular file that only its owner can read (Unix mode
// 0600 or narrower; on Windows fileacl.CheckOwnerOnly, read by path before the
// the open file's check: a window acceptable for a file the user names).
func LoadFile(path string) (crypto.Signer, keys.Algorithm, error) {
	// A missing or non-regular path gets the same text on every OS, before the
	// owner-only check; only the check's policy refusals are ErrNotOwnerOnly.
	f, fi, err := openRegular(path)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			// A DACL that denies this user's read fails at the open on Windows:
			// name the ACL when it is the cause.
			if cerr := fileacl.CheckOwnerOnly(path); errors.Is(cerr, fileacl.ErrExposed) {
				return nil, "", ownerOnlyError("credential: " + cerr.Error())
			}
		}
		return nil, "", err
	}
	defer func() { _ = f.Close() }()
	if err := fileacl.CheckOwnerOnly(path); err != nil {
		if errors.Is(err, fileacl.ErrExposed) {
			return nil, "", ownerOnlyError("credential: " + err.Error())
		}
		return nil, "", fmt.Errorf("credential: check %s: %w", path, err)
	}
	return readKey(f, fi, path)
}

// openRegular opens path (following a symlink) and refuses anything that is
// not a regular file; the errors are the same on every OS. A permission error
// says what to fix.
func openRegular(path string) (*os.File, os.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return nil, nil, fmt.Errorf("credential: open %s: %w (the key file must be readable by the user running this program: fix its permissions, or its ACL on Windows)", path, err)
		}
		return nil, nil, fmt.Errorf("credential: open %s: %w", path, err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, fmt.Errorf("credential: stat %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("credential: %s is not a regular file", path)
	}
	return f, fi, nil
}

// loadFile reads and parses the private key at path, refusing a non-regular
// file, a Unix mode wider than 0600 and an oversized or unparseable file.
func loadFile(path string) (crypto.Signer, keys.Algorithm, error) {
	f, fi, err := openRegular(path)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = f.Close() }()
	return readKey(f, fi, path)
}

// readKey applies the mode check and parses the key from the open file f.
func readKey(f *os.File, fi os.FileInfo, path string) (crypto.Signer, keys.Algorithm, error) {
	if runtime.GOOS != "windows" && fi.Mode().Perm()&^0o600 != 0 {
		msg := fmt.Sprintf("credential: %s has mode %04o and must be mode 0600 (chmod 600 it)", path, fi.Mode().Perm())
		return nil, "", ownerOnlyError(msg)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxKeyFile+1))
	if err != nil {
		return nil, "", fmt.Errorf("credential: read %s: %w", path, err)
	}
	if len(b) > maxKeyFile {
		return nil, "", fmt.Errorf("credential: %s is larger than %d bytes", path, maxKeyFile)
	}
	k, alg, err := keys.ParsePrivatePEM(b)
	if err != nil {
		return nil, "", fmt.Errorf("credential: %s: %w", path, err)
	}
	return k, alg, nil
}

// Save writes identity's private key, replacing any previous file atomically
// at mode 0600 (owner-only ACL on Windows) in a directory made at mode 0700.
func (s Store) Save(identity string, k crypto.Signer) error {
	if err := checkName(identity); err != nil {
		return err
	}
	pem, err := keys.EncodePrivatePEM(k)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("credential: create %s: %w", s.Dir, err)
	}
	return writeKey(s.Dir, s.Path(identity), pem)
}

// writeKey writes pem at path, atomically, at mode 0600 (owner-only ACL on
// Windows), creating its directory and Dir at mode 0700.
func writeKey(dir, path string, pem []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("credential: create %s: %w", dir, err)
	}
	if err := keys.WriteFile(path, pem); err != nil {
		return err
	}
	if err := fileacl.OwnerOnly(path); err != nil {
		return fmt.Errorf("credential: restrict %s: %w", path, err)
	}
	return nil
}

// pendingDir holds keys generated by `register` that the server has not yet
// confirmed. It is a directory, so List skips it.
const pendingDir = ".pending"

// PendingPath is the pending key file of identity.
func (s Store) PendingPath(identity string) string {
	return filepath.Join(s.Dir, pendingDir, identity)
}

// SavePending writes identity's not-yet-registered key under the same rules as
// Save (0600, owner-only ACL, 0700 directories), replacing any earlier one.
func (s Store) SavePending(identity string, k crypto.Signer) error {
	if err := checkName(identity); err != nil {
		return err
	}
	pem, err := keys.EncodePrivatePEM(k)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("credential: create %s: %w", s.Dir, err)
	}
	return writeKey(filepath.Join(s.Dir, pendingDir), s.PendingPath(identity), pem)
}

// LoadPending reads identity's pending key; false when there is none. The mode
// check is Load's.
func (s Store) LoadPending(identity string) (crypto.Signer, keys.Algorithm, bool, error) {
	if err := checkName(identity); err != nil {
		return nil, "", false, err
	}
	if _, err := os.Lstat(s.PendingPath(identity)); errors.Is(err, os.ErrNotExist) {
		return nil, "", false, nil
	}
	k, alg, err := loadFile(s.PendingPath(identity))
	if err != nil {
		return nil, "", false, err
	}
	return k, alg, true, nil
}

// CommitPending makes k, the key that was registered, identity's credential.
// k is always saved as the credential first; the pending file is then removed
// only when it still holds k, so another run's replacement stays for that run
// (no window in which a later key is renamed onto the credential). The pending
// directory is then removed when empty (another pending key keeps it).
func (s Store) CommitPending(identity string, k crypto.Signer) error {
	if err := checkName(identity); err != nil {
		return err
	}
	want, err := keys.KID(k.Public())
	if err != nil {
		return err
	}
	if err := s.Save(identity, k); err != nil {
		return err
	}
	if p, _, ok, err := s.LoadPending(identity); err == nil && ok {
		if got, err := keys.KID(p.Public()); err == nil && got == want {
			if err := os.Remove(s.PendingPath(identity)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("credential: commit %s: %w", s.PendingPath(identity), err)
			}
		}
	}
	s.removePendingDir()
	return nil
}

// DiscardPending removes identity's pending key, if any.
func (s Store) DiscardPending(identity string) error {
	if err := checkName(identity); err != nil {
		return err
	}
	if err := os.Remove(s.PendingPath(identity)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("credential: discard %s: %w", s.PendingPath(identity), err)
	}
	s.removePendingDir()
	return nil
}

// removePendingDir removes the pending directory; os.Remove refuses a
// non-empty one, which is the case to ignore.
func (s Store) removePendingDir() { _ = os.Remove(filepath.Join(s.Dir, pendingDir)) }
