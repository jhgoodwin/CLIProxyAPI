# Tenant policy: operation and administration

Tenant policy provides proxy-issued client keys, user permissions, durable usage
attribution, and optional quota accounting. This guide puts the reference first
and runnable examples afterward.

## Reference

### Users, keys, and providers

A **user** owns a provider permission list and, in quota mode, a shared budget
and pool shares. A **key** belongs to one user and has an optional human-readable
label. Give each app its own key to distinguish its usage. Multiple keys for a
user share that user's permissions and accounting; labels do not grant access.

Clients authenticate with exactly one `Authorization: Bearer KEY` header. The
plugin stores a SHA-256 key identifier rather than the raw secret. Query keys,
alternate authentication headers, and legacy host/bootstrap keys do not bypass
tenant authentication. Upstream API keys and subscription credentials remain in
the proxy's provider configuration; clients receive tenant keys instead.

Provider permissions use **runtime provider IDs**, not a model's vendor or its
client-visible prefix. Built-in examples are `codex`, `claude`, and `devin`.
An OpenAI-compatible group named `groq` has ID `openai-compatible-groq`;
`local-vllm` becomes `openai-compatible-local-vllm`. A group whose name already
starts with `openai-compatible-` retains that name. An Anthropic model routed
through OpenRouter requires `openai-compatible-openrouter`, not `claude`.

The literal permission `*` explicitly grants **all current and future
providers**. Always pass it as `'*'` or `"*"`: an unquoted asterisk is expanded
by the shell into filenames. This grant does not configure providers or expand
upstream entitlements, and it does not bypass disabled users, revoked keys,
optional model restrictions, or quota-mode requirements.

### Modes and API coverage

| Setting | Behavior |
| --- | --- |
| `mode: identity` | Named keys, provider authorization, and best-effort usage attribution for trusted users. No USD, pool, or overage enforcement. |
| `mode: quota` or omitted mode | Provider authorization plus configured admission debits and capacity/credit shares. Requires explicit model mappings. |

Identity mode accepts `0` as the unused user USD limit. Existing limits, spent
balances, shares, and overage blocks are preserved but ignored. Pool definitions
and model token allowances are rejected in identity configuration. Omit `models`
and `prices` for access through provider permissions without maintaining a
separate model catalog. A nonempty `models` map restricts intercepted requests
to explicit client-alias/provider/upstream-model pairs; optional prices supply
text usage estimates.

Identity mode permits the proxy's implemented HTTP APIs, including multimodal
chat/messages/responses, model discovery, images, speech/TTS, video, Gemini
interactions and token counting, compact responses, and Codex direct aliases.
It permits Responses WebSockets and Codex realtime APIs. It does not implement
missing provider capabilities or add an audio transcription endpoint. Home
dispatch and unaudited plugin execution remain unavailable.

A nonempty identity `models` map disables direct Codex live/realtime and
alpha-search routes, including realtime secret issuance, sideband, and session
control: these paths cannot enforce that model map. Leave the map absent for
full trusted-user API access.

Quota mode admits the audited text HTTP paths `/v1/chat/completions`,
`/v1/completions`, `/v1/messages`, and `/v1/responses`, including streaming.
It rejects media payloads, dedicated media and token-count APIs, WebSockets,
and the other paths outside that accounting boundary.

### Authorization changes and WebSockets

CLI provisioning changes take effect at subsequent authorization checks without
restarting the proxy. A revoked key remains in history and cannot be restored by
a CLI command. Disabling a user blocks all that user's keys; enabling the user
does not un-revoke keys. `user-set` does not enable a disabled user.

Responses WebSocket generation turns reauthorize their keys. Revocation does
not terminate an admitted stream or cancellation/control frames. Codex realtime
authenticates at request/connection boundaries; revoking a key or disabling its
owner prevents new connections but does not disconnect an active session.

Locally issued realtime temporary credentials remain bound to their issuing
tenant key. When presented as Bearer credentials, the host validates expiration
and session/model binding, restores the verified issuer identity, and rechecks
the issuer key, enabled user, and `codex` provider permission. This delegation
is supported only in identity mode. There is currently no tenant token/cost
reporting for Codex realtime sessions.

### Accounting and reports

Ordinary authorized executor attempts record their user, key identifier,
provider, and upstream model. Usage events are deduplicated; separate retries
remain separate attempts. Reports exclude raw secrets, prompts, response bodies,
and upstream credentials. They contain private activity data.

Usage coverage depends on executors emitting usage events. Missing tokens and
actual billed costs remain unknown. Token-count requests can have attributed
attempts without a usage row; media costs are not estimated using text token
rates. Realtime usage is absent. Identity attempts are not retrospectively
charged if the deployment later switches to quota mode.

Reports separate estimated API-equivalent USD, internal policy debits, and
unknown provider invoice amounts. Quota USD limits cap **internal debits**, not
provider spend: each admitted attempt uses a fixed token allowance and configured
rates, retries debit again, and failures/cancellations do not refund it. These
allowances do not cap upstream token generation. Reported usage above an
allowance or an unacceptable model/provider substitution can persistently
block the user; already admitted requests can finish. Review the cause before
clearing that block.

Provider-reported pool accounting is best effort: it attributes changed balances
using admitted attempts, not measured per-request provider consumption. Positive
shares can permit access when quota is unknown; known, unexpired exhaustion
blocks. Checks do not poll or probe. Resetting accounting does not replenish
provider balances. For pricing, pool configuration, and accounting details, see
[plugin configuration](../plugins/tenant-policy/README.md),
[capacity pools](../plugins/tenant-policy/pool.md), and
[usage reports](../plugins/tenant-policy/metrics.md).

JSON/CSV exports and the offline HTML dashboard are snapshots. Regenerate them
to refresh them. Labels are joined from current key metadata, so relabeling
changes the label displayed for historical usage without changing attribution.
Outputs are created with mode `0600`, refuse to overwrite existing paths, and
are not served as public web pages. Output paths refer to the CLI's filesystem;
inside Docker, that means the container or a mounted volume.

### Deployment and durable state

The plugin runs as trusted native code on Linux with CGO. Build the server,
CLI, and C-shared plugin from compatible source with the same Go 1.26+ toolchain
and target platform. Restrict write access to the plugin directory. The host
must support the tenant policy barriers and a compatible plugin ABI (currently
schema version 7).

Set `plugins.enabled: true`, the plugin library `dir`, and
`plugins.configs.tenant-policy.enabled: true`. Set
`plugins.configs.tenant-policy.required: true` so a missing, disabled, failed,
or fused plugin rejects proxy traffic. A top-level `plugins.required` is not a
substitute. Set `access.api-keys: []` and issue client keys through this plugin.
See [provider configuration](../plugins/tenant-policy/providers.md) for upstream
setup; this guide's API coverage distinguishes identity from quota mode.

The server and CLI must run under the same OS account against the same absolute
state path. Keep the ledger in a dedicated, real directory with mode `0700` and
the ledger/adjacent `.lock` file private (`0600`); symlink state files are
rejected. Use a local filesystem with reliable `flock`, atomic rename, and
`fsync`. Transactions lock, write a temporary file, and atomically publish it.
Do not replace/delete the lock file while any process can use the ledger.
Independent ledgers across replicas are unsupported.

Back up the policy directory while the server is stopped and no CLI or other
writer is using it. Restore with the same ownership and permissions while all
writers are stopped. A rollback restores old permissions, revocations, and
accounting as well as users and keys; deleting the ledger loses that history.
The ledger contains hashes, so it cannot recover a lost raw client key. Back up
saved client secrets and upstream credentials separately in private storage.

### CLI syntax

```text
tenant-policy --state /absolute/path/ledger.json COMMAND [ARGUMENTS...]
tenant-policy --import-prices SOURCE_JSON DESTINATION_JSON
```

`--state` must precede the command and be absolute. `--import-prices` is a
separate invocation, not a command after `--state`. Errors exit nonzero. The
CLI does not provide an extensive `--help` command; this is its command reference.
`ID`/`USER` refer to ledger users; `HASH` is the identifier from `key-list`.
Quote labels, reasons, JSON, and literal wildcard permissions as appropriate.

| Command and positional arguments | Effect |
| --- | --- |
| `init` | Create a private, empty ledger; refuse to replace an existing ledger. Run once before enabling the plugin. |
| `status` | Print users, provider grants, limits, consumption, share settings, and disable/overage state as JSON. |
| `user-set ID LIMIT_USD PROVIDER...` | Create/update a user. Replace provider grants and USD limit; preserve spent amount, disabled/overage state, and pool/credit shares. At least one provider is required. Duplicates collapse. |
| `user-disable ID` | Disable all keys owned by the user at subsequent authorization. |
| `user-enable ID` | Enable the user; revoked keys stay revoked. |
| `key-create USER [LABEL]` | Durably create a random key and print its secret once. User must exist; creating a key for a disabled user does not enable access. |
| `key-list` | Print key hashes, labels, owners, and revoked flags as JSON. Does not reveal secrets. |
| `key-label HASH LABEL` | Replace a key's label; `''` clears it. Works for revoked keys too. Labels need not be unique. |
| `key-revoke HASH` | Permanently revoke the key, retaining historical attribution. There is no hard-delete or un-revoke command. |
| `metrics-export json\|csv OUTPUT_PATH` | Export a private usage snapshot; destination must not exist. |
| `dashboard OUTPUT_HTML_PATH` | Generate a private, standalone offline HTML report; destination must not exist. |
| `account-set NAME PROVIDER AUTH_ID` | Bind a stable account alias to a runtime provider and upstream credential ID. Alias names cannot contain glob characters; bindings cannot be rebound or duplicated under another alias. |
| `user-share USER PROVIDER PERCENT` | Set the user's renewing/request share, using the configured pool name or provider pool key. Percentage is from 0 to 100; it does not itself grant provider access. |
| `user-credit-share USER PROVIDER PERCENT` | Set the independent native-credit share, using the configured pool name or provider pool key. Percentage is from 0 to 100. |
| `quota-observe PROVIDER AUTH_ID RFC3339 HEADERS_JSON` | Seed provider-reported renewing quota with a JSON object of string header names/values. Use a nonfuture timestamp newer than the existing seed and supported signals whose windows reset in the future. |
| `credits-observe PROVIDER AUTH_ID RFC3339 BALANCE [HEADER]` | Seed a nonnegative native-credit balance with a newer, nonfuture timestamp. Default header is `x-codex-credits-balance`; other providers require an explicit header. |
| `quota-reset renewing\|credits POOL_OR_PROVIDER REASON [ACCOUNT]` | Reset attributed usage for the selected resource/accounts while preserving observed remaining balances, USD debits, keys, and metrics. Requires valid recent observations; optional account can be an alias or credential ID. |
| `user-clear-overage ID` | Acknowledge/correct an overage block after administrator review. Does not reset spent amount or fix the underlying allowances. |

The share, observation, reset, and overage commands administer quota mode; they
do not introduce enforcement in identity mode. USD and balance inputs use
nonnegative decimal strings. There is no user-delete command or raw-key lookup.

`--import-prices` validates a local LiteLLM JSON pricing snapshot and creates a
new destination file without overwriting it. Requests do not fetch remote prices.
Review/pin the source, retain its revision and hash, and select the validated
snapshot in plugin configuration. Existing usage keeps its original price
snapshot. See the [pricing procedure](../plugins/tenant-policy/README.md#pricing-snapshot).

## Examples

The Docker examples use container `cli-proxy-api-personal`, CLI
`/CLIProxyAPI/tenant-policy`, state `/policy/ledger.json`, and a host policy
directory mounted from `~/.config/cliproxyapi/policy`. Adapt these names for
another installation. This deployment uses a custom image containing both the
CLI and plugin; the repository's standard Dockerfile includes only the server.
Build/copy the CLI and C-shared plugin into your image before using these
commands in another container. Define this helper in Bash:

```bash
tenant() {
  docker exec cli-proxy-api-personal \
    /CLIProxyAPI/tenant-policy --state /policy/ledger.json "$@"
}
```

### Inspect users and app keys

```bash
tenant status
tenant key-list
```

### Provision a restricted user

These examples assume identity mode. Save the printed secret privately and give
it to the client's API-key setting; it cannot be retrieved later.

```bash
tenant user-set alice 0 codex claude openai-compatible-groq
tenant key-create alice "Alice laptop"
```

### Grant all providers, including future additions

```bash
tenant user-set alice 0 '*'
tenant key-create alice "Alice phone"
```

To keep an existing owner's applications unrestricted:

```bash
tenant user-set personal 0 '*'
```

This updates the user's permissions for every owned key, including an existing
key labeled `Open WebUI`.

### Add another app for an existing user

```bash
tenant key-create personal "New app"
```

In this Docker deployment, Open WebUI on the shared network uses base URL
`http://cliproxy:8317/v1`. Host apps or an SSH-forwarded client use
`http://127.0.0.1:8317/v1`. Set the app's API key to its newly issued tenant key
and choose a model advertised by the proxy. Endpoint compatibility still
depends on the app and upstream provider.

### Replace permissions, rename a key, or suspend a user

```bash
tenant user-set alice 0 claude openai-compatible-groq
tenant key-label REPLACE_KEY_HASH "Alice desktop"
tenant key-label REPLACE_KEY_HASH ''
tenant user-disable alice
tenant user-enable alice
```

The first command removes `codex` and any earlier `*` grant. Copy the full hash
from `key-list`, not the raw API key. Enabling restores only nonrevoked keys.

### Rotate or revoke a key

```bash
tenant key-create personal "Open WebUI replacement"
# Install the printed secret in the app and verify a request succeeds.
tenant key-list
tenant key-revoke REPLACE_OLD_KEY_HASH
```

If a secret is compromised, revoke it immediately instead of waiting for a
replacement rollout. Existing admitted streams/realtime sessions may continue.

### Export usage and retrieve an offline dashboard

Use new filenames each time because outputs cannot be overwritten:

```bash
tenant metrics-export json /policy/usage-2026-10-09.json
tenant metrics-export csv /policy/usage-2026-10-09.csv
tenant dashboard /policy/dashboard-2026-10-09.html

umask 077
mkdir -p "$HOME/tenant-reports"
docker cp cli-proxy-api-personal:/policy/usage-2026-10-09.json "$HOME/tenant-reports/"
docker cp cli-proxy-api-personal:/policy/dashboard-2026-10-09.html "$HOME/tenant-reports/"
chmod 600 "$HOME/tenant-reports/usage-2026-10-09.json" "$HOME/tenant-reports/dashboard-2026-10-09.html"
```

Open the copied HTML privately on your local machine; use SSH/SCP to transfer it
if needed. Do not publish the report directory. For a native CLI, use a private
local output path directly.

### Back up and restore the policy directory

Stop all other policy writers, including CLI sessions, before either procedure.
The following backup uses a new private directory and copies state only after
the proxy stops successfully. A failed copy leaves the proxy stopped for review.

```bash
umask 077
backup_dir="$HOME/tenant-policy-backups/$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$HOME/tenant-policy-backups" &&
mkdir -m 700 "$backup_dir" &&
docker stop cli-proxy-api-personal &&
docker cp cli-proxy-api-personal:/policy/. "$backup_dir/" &&
docker start cli-proxy-api-personal
```

To restore, choose a reviewed snapshot. This replaces provisioning/accounting
with that snapshot, potentially restoring old grants or revoked keys. Ordinary
`docker cp` assigns restored files to root in this root-run container and
preserves file modes. Do not start the proxy after a partial or failed restore.

```bash
backup_dir="$HOME/tenant-policy-backups/REPLACE_SNAPSHOT_DIRECTORY"
test -f "$backup_dir/ledger.json" &&
docker stop cli-proxy-api-personal &&
docker cp "$backup_dir/." cli-proxy-api-personal:/policy/ &&
docker start cli-proxy-api-personal
```

For a non-root deployment, adapt the procedure to preserve or restore the
service account's UID/GID before starting it. Do not use the root-container
ownership assumption for a different service account.

### Initialize a native installation and import prices

Run initialization once, before starting the required plugin. The CLI and
server must use the same account:

```bash
go build -o tenant-policy ./plugins/tenant-policy
go build -buildmode=c-shared -o /srv/cliproxy-plugins/tenant-policy.so ./plugins/tenant-policy
./tenant-policy --state /srv/cliproxy-policy/ledger.json init
./tenant-policy --state /srv/cliproxy-policy/ledger.json user-set owner 0 '*'
./tenant-policy --state /srv/cliproxy-policy/ledger.json key-create owner "Owner app"
./tenant-policy --import-prices /tmp/reviewed-litellm-prices.json /srv/cliproxy-policy/prices-v1.json
sha256sum /srv/cliproxy-policy/prices-v1.json
```

Create the plugin output directory first with appropriate administrator-owned
permissions. The pricing source must already be a reviewed local snapshot.
Identity mode does not require prices; importing a file alone does not enable
pricing or quota enforcement.

Merge this identity-mode configuration with the existing upstream provider
settings, then start the server using that private configuration:

```yaml
access:
  api-keys: []
plugins:
  enabled: true
  dir: /srv/cliproxy-plugins
  configs:
    tenant-policy:
      enabled: true
      required: true
      priority: 100
      mode: identity
      state: /srv/cliproxy-policy/ledger.json
```
