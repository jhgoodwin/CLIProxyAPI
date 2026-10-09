# Tenant policy plugin suite

This Linux Go C-shared plugin groups exclusive tenant-key authentication, provider authorization, shared quota pools, durable accounting, LiteLLM pricing, and usage dashboards in one artifact. It requires the accompanying host barriers and schema version 7; built-in upstream executors remain unchanged. The default `mode: quota` enforces the accounting policy described below. Explicit `mode: identity` provides named keys and best-effort usage attribution for trusted users without quota enforcement.

Subscription and volume-limited providers use named pools selected by account identifiers, arrays, or globs. Users receive independent percentages of renewing capacity and native credits. An owner can have 100% and a friend 50%; adding a member expands that pool without clearing past consumption. Keep personal accounts in another pool when their capacity should stay private. Positive grants are permissive when quota is unknown; only known, unexpired exhaustion blocks, and quota checks never wait or poll. Changed balance deltas are estimated in proportion to each user's admitted-attempt count since the prior changed balance; this is best-effort share accounting, not per-request provider usage. See the [quota and attribution rules](pool.md#provider-reported-remaining-quota).

The USD limit caps **internal policy debits**, not provider invoices. Each authorized upstream attempt consumes a fixed allowance: `input_allowance_tokens × input_rate + output_allowance_tokens × output_rate`. Allowance token counts are accounting inputs, not enforced upstream token ceilings. Retries consume another allowance. No refund occurs on failure, cancellation, missing usage, or restart. Observed usage is separately accumulated and deduplicated; usage above an attempt allowance or a mismatched usage model/provider persistently blocks that user. Already admitted requests can finish and incur further upstream charges. An administrator must review rates/allowances before explicitly clearing that block.

Token prices are exact decimal USD. Imported cache and long-context token prices use conservative maximum input/output rates. Cache and reasoning counters are counted separately, which can overstate usage when upstream counters overlap. Extra non-token billing dimensions require explicit administrator rates. Unknown aliases, provider substitutions, upstream model substitutions, prices, and unsupported request paths fail closed. In quota mode, the supported text paths are `/v1/chat/completions`, `/v1/completions`, `/v1/messages`, and `/v1/responses`, including HTTP streaming. WebSocket, Home dispatch, plugin direct execution, images/audio, and token-count paths are denied under the required policy guard.

## Named keys without quota enforcement

For personal use or trusted users, explicitly select identity mode:

```yaml
plugins:
  enabled: true
  dir: /srv/cliproxy-plugins
  configs:
    tenant-policy:
      required: true
      enabled: true
      mode: identity
      state: /srv/cliproxy-policy/ledger.json
```

Initialize the ledger and create a named user and labeled key with the CLI below. `user-set USER 0 PROVIDER...` accepts zero as the unused USD limit in this mode. Identity mode still requires one valid Bearer key, an enabled user, and access to the selected provider. Revoked keys fail subsequent authorization. Bootstrap host keys cannot bypass tenant authentication. Locally issued realtime ephemeral secrets are bound to their issuing tenant key and recheck that key, its user, and the Codex provider grant on each new request/connection. Keep `required: true` so missing, disabled, or failed plugins reject requests.

Identity mode permits the proxy's implemented HTTP APIs: text and multimodal chat/messages/responses, image generation and edits, speech/TTS, video, model discovery, Gemini interactions and token counting, compact responses, and Codex direct aliases. It also permits Responses WebSockets and Codex realtime APIs. It does not add new upstream capabilities or an audio transcription endpoint. Home dispatch and unaudited plugin execution paths remain unavailable.

Omit `models` and `prices` to authorize the models in the proxy registry using the user's provider allowlist. A nonempty `models` map optionally restricts aliases to exact provider/upstream-model pairs; each mapping needs only `provider` and `upstream_model`. Because direct Codex live/realtime and alpha-search routes do not pass through model interception, a nonempty map disables those routes, including realtime secret issuance, sideband, and session-control endpoints. Omit the map for full trusted-user API access. Optional token price keys or explicit input/output rates provide estimates for ordinary text requests. Pools and token allowances are rejected in identity configuration to prevent misleading claims of enforcement. Existing user USD limits, spent balances, overage blocks, and pool shares are ignored in identity mode and preserved for later quota-mode use.

Normal execution attempts retain trusted user/key/provider/model attribution, and deduplicate usage records. Missing token usage and actual billed costs remain unknown. Media costs are not estimated from text-token rates. USD estimates are not provider invoices, and requests without executor usage events have no usage row. Token-count requests create attribution attempts but do not consume a USD allowance. Switching back to quota mode does not retrospectively charge identity attempts.

Responses WebSocket generation turns reauthorize their keys; existing streams and cancellation/control frames are not terminated when a key is revoked. Codex realtime connections authenticate at connection/request boundaries, require the `codex` provider grant, and currently have no tenant token/cost reporting. Revocation prevents new connections, but does not disconnect an active realtime session. Realtime clients can use the locally issued ephemeral secret as their Bearer credential; the host restores its verified issuer identity and reauthorizes it through the current policy. Expiration and session/model binding still apply. Quota mode does not accept this delegation. Use realtime only with trusted users who understand its usage is absent from this dashboard.

## Build and initialize

Use the same Go 1.26+ toolchain and target platform as the server, with CGO enabled:

```sh
go build -o tenant-policy ./plugins/tenant-policy
go build -buildmode=c-shared -o /srv/cliproxy-plugins/tenant-policy.so ./plugins/tenant-policy
./tenant-policy --state /srv/cliproxy-policy/ledger.json init
./tenant-policy --state /srv/cliproxy-policy/ledger.json account-set claude_sub_1 claude REPLACE_CLAUDE_AUTH_ID
./tenant-policy --state /srv/cliproxy-policy/ledger.json account-set codex_sub_1 codex REPLACE_CODEX_AUTH_ID
./tenant-policy --state /srv/cliproxy-policy/ledger.json user-set alice 20 claude codex openai-compatible-deepseek
./tenant-policy --state /srv/cliproxy-policy/ledger.json user-share alice claude_shared 100
./tenant-policy --state /srv/cliproxy-policy/ledger.json user-share alice codex_shared 100
./tenant-policy --state /srv/cliproxy-policy/ledger.json user-credit-share alice codex_shared 100
./tenant-policy --state /srv/cliproxy-policy/ledger.json user-set friend 0 codex
./tenant-policy --state /srv/cliproxy-policy/ledger.json user-share friend codex_shared 50
./tenant-policy --state /srv/cliproxy-policy/ledger.json user-credit-share friend codex_shared 50
./tenant-policy --state /srv/cliproxy-policy/ledger.json key-create alice "Website production"
./tenant-policy --state /srv/cliproxy-policy/ledger.json key-list
./tenant-policy --state /srv/cliproxy-policy/ledger.json key-label KEY_SHA256_HASH "Website staging"
./tenant-policy --state /srv/cliproxy-policy/ledger.json key-label KEY_SHA256_HASH "" # Clear the label
./tenant-policy --state /srv/cliproxy-policy/ledger.json key-revoke KEY_SHA256_HASH
./tenant-policy --state /srv/cliproxy-policy/ledger.json user-disable alice
./tenant-policy --state /srv/cliproxy-policy/ledger.json user-enable alice
./tenant-policy --state /srv/cliproxy-policy/ledger.json status
./tenant-policy --state /srv/cliproxy-policy/ledger.json user-clear-overage alice
```

Keys print once after durable creation. `key-create USER [LABEL]` accepts an optional human-readable note; quote labels containing spaces. `key-label HASH LABEL` updates or clears the note, including for archived keys. `key-list` includes labels. Labels need not be unique and do not change authentication, balances, or the stable SHA-256 key identifier. Clients use `Authorization: Bearer KEY`; legacy host keys, query credentials, and alternate auth headers are not accepted. Multiple keys share the same user balance. Changes are effective for subsequent authorization checks without restarting. Store the ledger in its own private directory on a local filesystem with reliable flock, atomic rename, and fsync. The server and CLI must use the same OS account/state file. Back up this directory; deleting or rolling back it resets accounting. Do not use separate ledgers across replicas. CLI `user-set` preserves consumption and disable/overage state; increasing the limit is an explicit administrator action.

`user-set` replaces the user's provider list. A literal `*` grants every current and future provider, including realtime Codex routes. Quote it in the shell to prevent filename expansion:

```sh
./tenant-policy --state /srv/cliproxy-policy/ledger.json user-set personal 0 '*'
```

This example uses identity mode's unused USD limit. The wildcard only grants provider access: disabled users, revoked keys, configured model restrictions, and quota-mode pricing, budgets, and pool shares still apply. It does not configure upstream providers or credentials. Use explicit provider IDs to keep a user's access restricted. Existing explicit lists retain their meaning; no ledger migration is needed.

## Pricing snapshot

Download the LiteLLM `model_prices_and_context_window.json` from a reviewed pinned revision of [BerriAI/litellm](https://github.com/BerriAI/litellm/blob/main/model_prices_and_context_window.json), then validate and import it locally:

```sh
./tenant-policy --import-prices /tmp/model_prices_and_context_window.json /srv/cliproxy-policy/prices.json
sha256sum /srv/cliproxy-policy/prices.json
```

The destination must not already exist. Keep the source revision and SHA256 with deployment records. Requests never fetch pricing remotely; configuration reload selects a new snapshot only after successful validation. Map every client alias explicitly to its runtime provider, upstream model, and exact LiteLLM key. Pool-only models can carry prices for estimated API-equivalent spend without consuming a USD budget.

## Server configuration

Merge these settings with [the provider example](../../examples/tenant-providers.yaml). See [provider configuration](providers.md) for account login commands, runtime identifiers, and endpoint references. Runtime provider IDs matter; `deepseek` is configured upstream as `openai-compatible-deepseek`.

```yaml
plugins:
  enabled: true
  dir: /srv/cliproxy-plugins
  configs:
    tenant-policy:
      required: true
      enabled: true
      priority: 100
      state: /srv/cliproxy-policy/ledger.json
      prices: /srv/cliproxy-policy/prices.json
      pools:
        claude_shared:
          provider: claude
          account_selectors: ["claude_sub_*"]
          mode: reported
          reported_max_age_seconds: 120
        codex_shared:
          provider: codex
          account_selectors: [codex_sub_1]
          mode: reported
          reported_max_age_seconds: 120
      models:
        deepseek/flash:
          provider: openai-compatible-deepseek
          upstream_model: deepseek-flash
          price_key: REPLACE_EXACT_LITELLM_KEY_FOR_DEEPSEEK_FLASH
          input_allowance_tokens: 32768
          output_allowance_tokens: 8192
        claude/claude-example:
          provider: claude
          upstream_model: claude-example
          pool_only: true
          pool: claude_shared
        codex/codex-example:
          provider: codex
          upstream_model: codex-example
          pool_only: true
          pool: codex_shared
          quota_resource: auto
```

Replace subscription model IDs, credential IDs, and price keys with actual values. Account bindings use the management credential's `id`, not its `auth_index`. Reported mode uses quota signals already present in ordinary upstream responses; initial administrator imports are optional, and no automatic quota probes run. `reported_max_age_seconds` remains accepted for compatibility but does not gate admission. Explicit configured request pools remain an alternative. `auto` can use credit admission after known, unexpired renewing-capacity exhaustion; it cannot force the upstream funding source. Unknown credit pool members prevent conclusive percentage enforcement. A selected account with known zero credits stays blocked until a newer positive balance is observed; manual reset clears attributed usage and counters but preserves actual remaining balance. Native credits are never converted into dollars or tokens. Positive USD allowance is required for USD mappings. Required mode blocks proxy traffic when the plugin is unavailable or fused; deployments must preserve that setting. Plugin libraries are trusted native code: restrict write access to the plugin directory. Do not combine this policy with plugins that perform hidden outbound calls or rewrite business payloads outside audited host paths.

The CLI can manually reset renewing or credit accounting for a named pool. See [reset commands](pool.md) for exact syntax and scope. Resets preserve actual remaining balances, archived keys, usage metrics, and USD debits.

See [pool policies](pool.md) for percentage shares over active subscription or volumetric credential pools.

See [usage and cost reporting](metrics.md) for private offline dashboards, per-user/key/provider exports, API-equivalent estimates, unknown accounting, and archived key attribution.

## Verification

```sh
go test -race ./plugins/tenant-policy
go build -buildmode=c-shared -o /tmp/tenant-policy.so ./plugins/tenant-policy
go build -o test-output ./cmd/server
rm test-output
```

Tests cover concurrent reservations, exact USD arithmetic, provider/path denial, key revocation, usage-event deduplication, retained reservations, and overage freezes. Real provider credentials are required for end-to-end upstream tests.
