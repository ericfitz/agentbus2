package bus

// SigningKeysNotConfigured is the fixed text of the unavailable error (not
// retryable) that the signing key operations answer where the server has no
// signing keys: local mode, or a server built without a key service.
const SigningKeysNotConfigured = "this server has no signing keys (local mode)"

// SigningKey is one key of the server's token signing key set: public facts
// only, never a private key. State is "active" or "retiring"; Provider is one
// of store, awskms, gcpkms, azurekms or hashivault; ProviderRef is the key URI
// inside an external provider ("" for the store provider); Algorithm is EdDSA
// or ES256. CreatedAt, RetiredAt and RemoveAfter are Unix milliseconds, the
// last two 0 while the key is active.
type SigningKey struct {
	KID, State, Provider, ProviderRef, Algorithm string
	CreatedAt, RetiredAt, RemoveAfter            int64
}

// RotateSigningKeyInput selects the kind of rotation. RevokePrevious deletes
// the previous active key instead of retiring it and skips the rotation
// cooldown.
type RotateSigningKeyInput struct {
	RevokePrevious bool
	IdempotencyKey string
}

// SigningKeyRotation reports a rotation: the new active key, the key that was
// active before (as it was; the zero SigningKey when the set had none) and
// whether that key was deleted.
type SigningKeyRotation struct {
	Active, Previous SigningKey
	Revoked          bool
}
