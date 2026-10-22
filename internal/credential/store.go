package credential

import (
	"crypto"
	"errors"
	"fmt"
	"io"
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
	path := s.Path(identity)
	f, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("credential: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, "", fmt.Errorf("credential: stat %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, "", fmt.Errorf("credential: %s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&^0o600 != 0 {
		return nil, "", fmt.Errorf("credential: %s has mode %04o and must be mode 0600 (chmod 600 it)", path, fi.Mode().Perm())
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
	path := s.Path(identity)
	if err := keys.WriteFile(path, pem); err != nil {
		return err
	}
	if err := fileacl.OwnerOnly(path); err != nil {
		return fmt.Errorf("credential: restrict %s: %w", path, err)
	}
	return nil
}
