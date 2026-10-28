# Signing keys

The server signs the bearers it issues (`/v1/auth/token`, the human sign-in) with a key from its signing key set: rows in the `signing_keys` table, one active and any number retiring. The active key signs; every key in the set is published at `/v1/auth/jwks` and verifies until it is removed. The `provider` column says where the private key lives.

## Providers

`signing_key_uri` (`configuration.md`) picks the provider for this server:

| Value | Provider | Private key |
|---|---|---|
| unset | Store row | In the `signing_keys` row, PKCS#8 PEM in clear, EdDSA. The server generates it. |
| `awskms://...`, `gcpkms://...`, `azurekms://...`, `hashivault://...` | External | In the key service. The row holds the public key and the key reference (`provider_ref`); the server asks the service to sign. ECDSA P-256 (ES256) only: any other key type is refused when the URI is resolved. |

A scheme whose backend is not compiled into the binary stops the start with `the <scheme> backend is not in this build (build with -tags <scheme>)` (exit 2). Each backend is behind a build tag of the same name, so the default binary links no cloud SDK. No backend is in this build yet; the sections below are added with each backend.

## Trust boundary

- Store-row provider: whoever can read the database (or a backup of it) can mint a token for any identity. The database is protected like the administrator credential (`deploy.md`, `backup.md`).
- External provider: the database holds no private key. Reading it does not let anyone mint. The boundary moves to the key service: whoever may call its sign operation with the server's credentials can mint, so grant the server's identity sign and read-public-key on that one key and nothing else. The server never creates, deletes or schedules deletion of an external key.

## Rotation

`agentbus2 server admin signing-key rotate` (or `POST /v1/admin/signing-keys/rotate`) asks the configured provider for its next key and makes it the active key; the previous active key becomes retiring and stays published until its `remove_after` (the reload interval, the longest bearer lifetime and the verification leeway after it stopped signing). `-revoke-previous` deletes it at once instead. `signing-key list` shows every row with its `remove_after`.

- Store row: the next key is a freshly generated one. `signing_key_rotation_hours` rotates it on schedule.
- External: the next key is the one the key URI resolves to now. The operator changes what the URI names (repoints the alias, adds a version, or points the setting at a new key and restarts), then runs `rotate`. Rotating while the URI still resolves to the active key answers a `conflict` (`the key URI resolves to the active signing key ...`) and changes nothing. While `signing_key_rotation_hours` is above 0 the `signing key rotation` job resolves the URI on every maintenance tick and adopts a repointed key within one tick; it cannot rotate the external key itself, so a key that is older than the interval and still named raises the job error shown in `health.md`. 0 turns scheduled rotation off for every provider.
- Removing a retiring row never touches the key service. After `remove_after` the operator retires the old key there.

## When the key service is down

- A start with an active row makes no call to the key service and serves: verification and the JWKS work, minting answers 503 `temporarily_unavailable` until the service returns (the next mint after it returns works with no restart).
- A start with no row calls the service once to adopt a key; if it cannot, the server exits 1 (a configuration error, such as a bad URI or a missing backend, exits 2). A start that failed this way left nothing behind and the next start retries.
- `rotate` and the scheduled job answer `unavailable` (retryable) and change nothing.

On a first start the server prints `adopted signing key <kid> (<provider> <key name>)`, where the key name is the URI without user info, query or fragment; the URI itself is never printed.
