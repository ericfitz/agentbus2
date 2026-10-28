// Package kmsfake is an in-memory external key provider for tests: P-256 keys
// behind one alias, a switch that makes the "service" fail, and a count of the
// calls a real KMS would have received. It imports the standard library and the
// keyprovider port only.
package kmsfake

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"sync"

	"github.com/ericfitz/agentbus2/internal/keyprovider"
)

// DefaultName is the provider name New("") uses.
const DefaultName = "awskms"

// Fake implements keyprovider.Provider over keys it holds in memory.
type Fake struct {
	name string

	mu    sync.Mutex
	keys  []*ecdsa.PrivateKey // every version ever behind the alias; the last is current
	down  bool
	calls int
}

var _ keyprovider.Provider = (*Fake)(nil)

// New returns a fake named name (DefaultName when empty) with one key behind
// its alias.
func New(name string) *Fake {
	if name == "" {
		name = DefaultName
	}
	f := &Fake{name: name}
	f.add()
	return f
}

func (f *Fake) add() {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err) // the system random source failed
	}
	f.keys = append(f.keys, k)
}

func (f *Fake) ref(i int) string { return fmt.Sprintf("%s://kmsfake/version-%d", f.name, i+1) }

func (f *Fake) key(i int) keyprovider.Key {
	k, err := keyprovider.ExternalKey(f.keys[i].Public(), f.ref(i))
	if err != nil {
		panic(err) // a P-256 key is always accepted
	}
	return k
}

// Name implements keyprovider.Provider.
func (f *Fake) Name() string { return f.name }

// SetDown makes every Next and SignContext fail with keyprovider.ErrUnavailable
// (or restores them).
func (f *Fake) SetDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

// Calls is the number of network-equivalent calls so far: Next and SignContext.
func (f *Fake) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Repoint puts a new key behind the alias and returns it; earlier versions stay
// signable by their pinned ref.
func (f *Fake) Repoint() keyprovider.Key {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.add()
	return f.key(len(f.keys) - 1)
}

// Next resolves the alias to its current version.
func (f *Fake) Next(ctx context.Context) (keyprovider.Key, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.down {
		return keyprovider.Key{}, fmt.Errorf("kmsfake: %w", keyprovider.ErrUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return keyprovider.Key{}, err
	}
	return f.key(len(f.keys) - 1), nil
}

// Signer opens the version k.Ref pins. It makes no call and counts nothing; a
// ref the fake does not hold is an error.
func (f *Fake) Signer(_ context.Context, k keyprovider.Key) (keyprovider.Signer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.keys {
		if f.ref(i) == k.Ref {
			return &signer{f: f, key: f.keys[i]}, nil
		}
	}
	return nil, fmt.Errorf("kmsfake: no key %q", k.Ref)
}

type signer struct {
	f   *Fake
	key *ecdsa.PrivateKey
}

func (s *signer) Public() crypto.PublicKey { return s.key.Public() }

// SignContext returns an ASN.1 DER ECDSA signature of digest, as a KMS does.
func (s *signer) SignContext(ctx context.Context, digest []byte, _ crypto.SignerOpts) ([]byte, error) {
	s.f.mu.Lock()
	s.f.calls++
	down := s.f.down
	s.f.mu.Unlock()
	if down {
		return nil, fmt.Errorf("kmsfake: %w", keyprovider.ErrUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return ecdsa.SignASN1(rand.Reader, s.key, digest)
}
