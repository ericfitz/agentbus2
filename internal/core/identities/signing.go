package identities

import (
	"context"
	"crypto"
	"errors"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/store"
)

// LoadOrCreateSigningKey loads the server's token signing key from the
// Store, creating it when none exists (Eric, 2026-10-21, Milestone 5 item
// 2). The key is generated before the transaction (Decision 26) and
// discarded when a row exists.
//
// State (Definition of Done D): a second start loads the active row; two starters
// at once end with one active row (the partial unique index; the loser reruns and
// loads the winner's); a crash before the insert leaves nothing.
func (s Service) LoadOrCreateSigningKey(ctx context.Context) (crypto.Signer, keys.Algorithm, bool, error) {
	gen, err := keys.Generate(keys.EdDSA)
	if err != nil {
		return nil, "", false, bus.Internal(err)
	}
	pemBytes, err := keys.EncodePrivatePEM(gen)
	if err != nil {
		return nil, "", false, bus.Internal(err)
	}
	genKID, err := keys.KID(gen.Public())
	if err != nil {
		return nil, "", false, bus.Internal(err)
	}
	genPub, err := keys.MarshalPublic(gen.Public())
	if err != nil {
		return nil, "", false, bus.Internal(err)
	}
	var (
		signer  crypto.Signer
		alg     keys.Algorithm
		created bool
	)
	for attempt := 1; ; attempt++ {
		err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			signer, alg, created = nil, "", false
			rows, err := tx.ListSigningKeys(ctx)
			if err != nil {
				return err
			}
			if len(rows) == 0 || rows[0].State != store.SigningKeyActive {
				if err := tx.PutSigningKey(ctx, store.SigningKey{
					KID: genKID, State: store.SigningKeyActive, Provider: store.SigningProviderStore,
					Algorithm: string(keys.EdDSA), PublicKey: genPub, PrivateKeyPEM: string(pemBytes), CreatedAt: s.NowMs(),
				}); err != nil {
					return err
				}
				signer, alg, created = gen, keys.EdDSA, true
				return nil
			}
			row := rows[0] // the active row is listed first
			if row.Provider != store.SigningProviderStore {
				return core.E(bus.CodeInternal, "signing key row %q is held by provider %q, which this build does not load", row.KID, row.Provider)
			}
			sg, a, perr := keys.ParsePrivatePEM([]byte(row.PrivateKeyPEM))
			if perr != nil {
				return core.E(bus.CodeInternal, "signing key row %q is corrupt", row.KID)
			}
			// The algorithm column must name the parsed key's algorithm.
			if kid, kerr := keys.KID(sg.Public()); kerr != nil || kid != row.KID || a != keys.Algorithm(row.Algorithm) {
				return core.E(bus.CodeInternal, "signing key row %q is corrupt", row.KID)
			}
			signer, alg = sg, a
			return nil
		})
		if errors.Is(err, store.ErrConflict) && attempt < 2 {
			continue
		}
		break
	}
	if err != nil {
		return nil, "", false, core.FromStore(err)
	}
	return signer, alg, created, nil
}
