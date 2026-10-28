package cli

import (
	"context"
	"io"

	"github.com/ericfitz/agentbus2/internal/bus"
)

type opSigningKeyOut struct {
	KID         string `json:"kid"`
	State       string `json:"state"`
	Provider    string `json:"provider"`
	ProviderRef string `json:"provider_ref"`
	Algorithm   string `json:"algorithm"`
	CreatedAt   int64  `json:"created_at"`
	RetiredAt   int64  `json:"retired_at"`
	RemoveAfter int64  `json:"remove_after"`
}

type opSigningKeyRotationOut struct {
	Active   opSigningKeyOut  `json:"active"`
	Previous *opSigningKeyOut `json:"previous,omitempty"`
	Revoked  bool             `json:"revoked"`
}

func opSigningKey(k bus.SigningKey) opSigningKeyOut {
	return opSigningKeyOut{KID: k.KID, State: k.State, Provider: k.Provider, ProviderRef: k.ProviderRef,
		Algorithm: k.Algorithm, CreatedAt: k.CreatedAt, RetiredAt: k.RetiredAt, RemoveAfter: k.RemoveAfter}
}

// RunSigningKeys implements `agentbus2 signing-keys list|rotate`
// (administrator only), the client form of the server's token signing key
// set. Where the server has no signing keys (a local profile) every action is
// unavailable (not retryable).
func RunSigningKeys(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return opDispatch(ctx, "signing-keys", map[string]opRunFunc{
		"list":   runSigningKeysList,
		"rotate": runSigningKeysRotate,
	}, args, environ, stdin, stdout, stderr)
}

func runSigningKeysList(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("signing-keys list", stderr)
	if _, code, ok := c.parse(args, 0, 0); !ok {
		return code
	}
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()
	ks, err := conn.Bus.ListSigningKeys(ctx)
	if err != nil {
		return fail(stderr, err)
	}
	out := make([]opSigningKeyOut, 0, len(ks))
	for _, k := range ks {
		out = append(out, opSigningKey(k))
	}
	return opPrint(stdout, stderr, out)
}

func runSigningKeysRotate(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("signing-keys rotate", stderr)
	revoke := c.fs.Bool("revoke-previous", false, "delete the previous active key at once instead of retiring it (skips the rotation cooldown)")
	idem := c.fs.String("key", "", "idempotency key: a retry with the same key and flags returns the first result")
	if _, code, ok := c.parse(args, 0, 0); !ok {
		return code
	}
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()
	res, err := conn.Bus.RotateSigningKey(ctx, bus.RotateSigningKeyInput{RevokePrevious: *revoke, IdempotencyKey: *idem})
	if err != nil {
		return fail(stderr, err)
	}
	out := opSigningKeyRotationOut{Active: opSigningKey(res.Active), Revoked: res.Revoked}
	if res.Previous.KID != "" {
		p := opSigningKey(res.Previous)
		out.Previous = &p
	}
	return opPrint(stdout, stderr, out)
}
