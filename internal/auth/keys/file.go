package keys

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile writes pem to path with replace semantics (Decision 22): it
// creates "<path>.tmp-<random>" in the same directory with O_EXCL at mode 0600,
// writes, syncs, closes and renames over path. No temp file survives an error.
func WriteFile(path string, pem []byte) error {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return fmt.Errorf("keys: random temp name: %w", err)
	}
	tmp := filepath.Clean(path) + ".tmp-" + hex.EncodeToString(rnd[:])
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("keys: create temp file: %w", err)
	}
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err := f.Write(pem); err != nil {
		return fail(fmt.Errorf("keys: write: %w", err))
	}
	if err := f.Sync(); err != nil {
		return fail(fmt.Errorf("keys: sync: %w", err))
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("keys: close: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("keys: rename: %w", err)
	}
	return nil
}
