# Signing keys

The server signs the bearers it issues (`/v1/auth/token`, the human sign-in) with a key from its signing key set: rows in the `signing_keys` table, one active and any number retiring. The active key signs; every key in the set is published at `/v1/auth/jwks` and verifies until it is removed. The `provider` column says where the private key lives.

## Providers

`signing_key_uri` (`configuration.md`) picks the provider for this server:

| Value | Provider | Private key |
|---|---|---|
| unset | Store row | In the `signing_keys` row, PKCS#8 PEM in clear, EdDSA. The server generates it. |
| `awskms://...`, `gcpkms://...`, `azurekms://...`, `hashivault://...` | External | In the key service. The row holds the public key and the key reference (`provider_ref`); the server asks the service to sign. ECDSA P-256 (ES256) only: any other key type is refused when the URI is resolved. |

A scheme whose backend is not compiled into the binary stops the start with `the <scheme> backend is not in this build (build with -tags <scheme>)` (exit 2). Each backend is behind a build tag of the same name, so the default binary links no cloud SDK. The `awskms` and `hashivault` backends are described below; the other two have no backend in this build yet and their sections are added with each one.

## awskms

Build: `go build -tags awskms .` (the default binary and the release archives link no AWS SDK; the Docker image's `Dockerfile` builds with no tag at present).

- Key URI: `awskms://[endpoint]/{key-id|key-ARN|alias/NAME|alias-ARN}`, for example `awskms:///alias/agentbus2` or `awskms:///arn:aws:kms:us-east-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab`. The optional `endpoint` is `host[:port]` and is always reached over https (for a VPC endpoint or a compatible service); leave it empty for the regional AWS endpoint. A URI that does not match is refused at start (exit 2).
- The key: an AWS KMS asymmetric key with key spec `ECC_NIST_P256`, usage `SIGN_VERIFY`, state `Enabled`. Another key spec, a disabled key or another usage is refused when the URI is resolved.
- Credentials and region come from the AWS SDK default chain (environment, shared config, container or instance role); set `AWS_REGION` or a profile region. The server stores none.
- What the server calls: `kms:DescribeKey` and `kms:GetPublicKey` when it resolves the URI (a rotation, the first start with no row, each scheduled-rotation tick) and again, at most every five minutes, before it signs (sigstore's signer re-reads them), and `kms:Sign` per bearer. Grant exactly those three actions on the key. Nothing else: the server never creates a key or an alias and never schedules a key's deletion.
- `AWS_TLS_INSECURE_SKIP_VERIFY` is not supported: sigstore honors it when signing but the key lookup does not, so the two would disagree; use a trusted certificate on the endpoint.
- Pinning: `rotate` resolves the key id or alias to the key's ARN and stores `awskms://[endpoint]/<key ARN>` as the row's key reference, so a bearer is always signed by the key whose public half the row holds, even after the alias moves.
- Rotating: create a new key and either repoint the alias (`aws kms update-alias --alias-name alias/agentbus2 --target-key-id <new key>`) or change `signing_key_uri` to the new key and restart, then run `agentbus2 server admin signing-key rotate`. The previous key stays in the set, and signable by its pinned ARN, until its `remove-after`; the operator retires it in KMS after that. Removal never deletes the external key.
- Failures: an unreachable endpoint, a timeout, throttling or an AWS 5xx is the service's own failure: `rotate` answers `unavailable` (retryable) and the token endpoint answers 503 `temporarily_unavailable`. Bad credentials, a missing, disabled or wrong-type key and access-denied answers are not retryable; `rotate` reports the AWS message with the key name, never the URI. A first start with no row exits 1 for either kind (`signing key: ...`); a malformed URI exits 2. The SDK retries a request up to three times before giving up, and a bearer's sign call is bounded to 10 seconds.
- Trust boundary: whoever may call `kms:Sign` on the key as the server's role can mint a bearer for any identity. Keep that permission on the server's role alone and audit it with CloudTrail.

## hashivault

Build: `go build -tags hashivault .` (the default binary and the release archives link no Vault client; the Docker image's `Dockerfile` builds with no tag at present). The backend works with HashiCorp Vault and with OpenBao (same API); the URI scheme is `hashivault` for both, and `openbao://` is refused.

- Key URI: `hashivault://NAME`, the name of a key in Vault's transit secrets engine, for example `hashivault://agentbus2`. `NAME` is letters, digits, `_`, `-` and `.`, starting and ending with a letter, digit or `_`. Anything else (a path, a query, user info, a trailing slash) is refused at start (exit 2), and the refusal does not print the URI.
- The key: a transit key of type `ecdsa-p256` that supports signing. Another type (`ed25519`, `ecdsa-p384`, RSA), a missing key or a key whose latest version has no readable public key is refused when the URI is resolved.
- Environment, read each time the backend connects (a running process cannot see a changed value, so replace a token by restarting):
  - `VAULT_ADDR`, else `BAO_ADDR`: the Vault address, `http` or `https` with a host and no user info (required);
  - `VAULT_TOKEN`, else `BAO_TOKEN`: the Vault token (required). The Vault CLI's token helper and `~/.vault-token` are not used, and the server does not log in or renew the token: use a token that outlives the process, such as a periodic token renewed outside the server;
  - `TRANSIT_SECRET_ENGINE_PATH`: the transit mount, default `transit`; slash-separated names (`team/transit`) with no leading or trailing slash. The mount is not part of the pinned key reference, so keep it the same on every replica;
  - the Vault client's own variables also apply: `VAULT_NAMESPACE`, `VAULT_HEADERS`, `VAULT_CACERT`, `VAULT_CAPATH`, `VAULT_CLIENT_CERT`, `VAULT_CLIENT_KEY`, `VAULT_SKIP_VERIFY`, `VAULT_TLS_SERVER_NAME`, `VAULT_CLIENT_TIMEOUT`, `VAULT_PROXY_ADDR` and the standard `HTTP_PROXY` family. The server makes one attempt per request (it sets no retries).
- What the server calls: a read of `transit/keys/NAME` when it resolves the URI (a rotation, the first start with no row, each scheduled-rotation tick) and, at most every five minutes, before it signs (sigstore's signer reads the key to learn its type); and a write to `transit/sign/NAME/sha2-256` per bearer, asking for a pre-hashed ECDSA signature by the pinned version. The policy is exactly: `path "transit/keys/NAME" { capabilities = ["read"] }` and `path "transit/sign/NAME/sha2-256" { capabilities = ["update"] }` (with your mount in place of `transit`). The server never creates, rotates or deletes a key.
- Pinning: `rotate` reads the key's `latest_version` and stores `hashivault://NAME/VERSION` as the row's key reference, so a bearer is always signed by the version whose public half the row holds, even after you add a version in Vault. The version, not the key, is what a rotation changes.
- Rotating: add a version in Vault (`vault write -f transit/keys/NAME/rotate`) or point `signing_key_uri` at another key and restart, then run `agentbus2 server admin signing-key rotate`. The previous version stays in the set, and signable by its pinned version, until its `remove-after`; after that you retire it in Vault yourself (for example by raising the key's `min_encryption_version`). Removal never deletes the Vault key or a version.
- Failures: Vault unreachable, a timeout, HTTP 429, any 5xx and a sealed or standby node (HTTP 503) are the service's own failure: `rotate` answers `unavailable` (retryable) and the token endpoint answers 503 `temporarily_unavailable`. A bad or expired token (403), a missing key, a key of another type and a key whose latest version cannot be read are not retryable; `rotate` reports Vault's message with the key name, never the URI, the token or the request URL. A first start with no row exits 1 for either kind (`signing key: ...`); a malformed URI exits 2. A bearer's sign call is bounded to 10 seconds, and at most 16 sign requests may wait on Vault at once (sigstore's signer cannot be canceled, so a Vault that never answers is not allowed to strand more); beyond that a sign fails at once as `unavailable`.
- Trust boundary: whoever holds a token that may update `transit/sign/NAME/sha2-256` can mint a bearer for any identity. Keep that token on the server alone and audit it with Vault's audit device.

## Trust boundary

- Store-row provider: whoever can read the database (or a backup of it) can mint a token for any identity. The database is protected like the administrator credential (`deploy.md`, `backup.md`).
- External provider: the database holds no private key. Reading it does not let anyone mint. The boundary moves to the key service: whoever may call its sign operation with the server's credentials can mint, so grant the server's identity sign and read-public-key on that one key and nothing else. The server never creates, deletes or schedules deletion of an external key.

## Rotation

`agentbus2 server admin signing-key rotate` (or `POST /v1/admin/signing-keys/rotate`) asks the configured provider for its next key and makes it the active key; the previous active key becomes retiring and stays published until its `remove_after` (the API field; `signing-key list` prints it as `remove-after`: the reload interval, the longest bearer lifetime and the verification leeway after it stopped signing). `-revoke-previous` deletes it at once instead. `signing-key list` shows every row with its `remove-after`.

- Store row: the next key is a freshly generated one. `signing_key_rotation_hours` rotates it on schedule.
- External: the next key is the one the key URI resolves to now. The operator changes what the URI names (repoints the alias, adds a version, or points the setting at a new key and restarts), then runs `rotate`. Rotating while the URI still resolves to the active key answers a `conflict` (`the key URI resolves to the active signing key ...`) and changes nothing. While `signing_key_rotation_hours` is above 0 the `signing key rotation` job resolves the URI on every maintenance tick and adopts a repointed key within one tick; it cannot rotate the external key itself, so a key that is older than the interval and still named raises the job error shown in `health.md`. 0 turns scheduled rotation off for every provider.
- Removing a retiring row never touches the key service. After `remove-after` (shown by `signing-key list`) the operator retires the old key there.

## When the key service is down

- A start with an active row makes no call to the key service and serves: verification and the JWKS work, minting answers 503 `temporarily_unavailable` until the service returns (the next mint after it returns works with no restart).
- A start with no row calls the service once to adopt a key; if it cannot, the server exits 1 (a configuration error, such as a bad URI or a missing backend, exits 2). A start that failed this way left nothing behind and the next start retries.
- `rotate` and the scheduled job answer `unavailable` (retryable) and change nothing.

On a first start the server prints `adopted signing key <kid> (<provider> <key name>)`, where the key name is the URI without user info, query or fragment; the URI itself is never printed.
