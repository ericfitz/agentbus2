package signingkeys

import (
	"context"
	"sync"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/auth/token"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Keyring is the issuer's token.KeySource over the signing_keys rows: the
// published set is every row, and the signer is the active row's, built
// through the configured provider and cached by kid.
type Keyring struct {
	s Service

	mu      sync.Mutex
	signers map[string]keyprovider.Signer
	warned  string // the mismatch last logged, so a reload loop logs it once
}

// NewKeyring builds a keyring over s (its Core and Provider).
func NewKeyring(s Service) *Keyring {
	return &Keyring{s: s, signers: map[string]keyprovider.Signer{}}
}

// LoadKeys reads the rows in one Read transaction and returns the set. The
// active row (first) is the set's Active key. When another replica rotated to
// a provider this keyring is not configured for, or the active row's signer
// cannot be built, the signer is the newest key of the configured provider
// that can still sign (reported as Active, as token.KeySet defines), and nil
// once no such row remains; a warning names both providers. A row whose
// columns disagree fails the load as corrupt: the issuer then keeps the set it
// has.
func (k *Keyring) LoadKeys(ctx context.Context) (token.KeySet, error) {
	var rows []store.SigningKey
	err := k.s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		var e error
		rows, e = tx.ListSigningKeys(ctx)
		return e
	})
	if err != nil {
		return token.KeySet{}, core.FromStore(err)
	}
	if len(rows) == 0 {
		return token.KeySet{}, core.E(bus.CodeNotFound, "the signing key set is empty")
	}
	set := token.KeySet{Keys: make([]token.Key, 0, len(rows))}
	byKID := make(map[string]token.Key, len(rows))
	for _, r := range rows {
		if err := validateRow(r); err != nil {
			return token.KeySet{}, err
		}
		pub, alg, _ := keys.ParsePublic(r.PublicKey)
		tk := token.Key{KID: r.KID, Algorithm: alg, Public: pub}
		set.Keys = append(set.Keys, tk)
		byKID[r.KID] = tk
	}
	set.Active = set.Keys[0]

	k.mu.Lock()
	defer k.mu.Unlock()
	live := make(map[string]keyprovider.Signer, len(rows))
	var usable *store.SigningKey
	for i := range rows {
		r := rows[i]
		if r.Provider != k.s.Provider.Name() {
			continue
		}
		sg := k.signers[r.KID]
		if sg == nil {
			built, err := k.s.Provider.Signer(ctx, keyprovider.Key{
				KID: r.KID, Algorithm: byKID[r.KID].Algorithm, Public: byKID[r.KID].Public,
				Ref: r.ProviderRef, PrivatePEM: []byte(r.PrivateKeyPEM),
			})
			if err != nil {
				k.s.Log.Warn("signing key unusable", "kid", r.KID, "provider", r.Provider, "error", err)
				continue
			}
			sg = built
		}
		live[r.KID] = sg
		if usable == nil {
			usable = &rows[i]
		}
	}
	k.signers = live
	if usable != nil {
		set.Active = byKID[usable.KID]
		set.Signer = live[usable.KID]
	}
	active := rows[0]
	if usable == nil || usable.KID != active.KID {
		msg := active.KID + "/" + active.Provider + "/" + k.s.Provider.Name()
		if k.warned != msg {
			k.warned = msg
			k.s.Log.Warn("the active signing key cannot sign here: bearers are signed by the newest usable key, or not at all",
				"active_kid", active.KID, "active_provider", active.Provider, "configured_provider", k.s.Provider.Name(), "signing", usable != nil)
		}
	} else {
		k.warned = ""
	}
	return set, nil
}
