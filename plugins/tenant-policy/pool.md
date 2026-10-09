# Provider capacity pools

Prefer [provider-reported mode](#provider-reported-remaining-quota) for subscription
accounts. The first example below is the explicit configured-request alternative.

A user's provider share is a maximum percentage of the combined capacity of all
currently enabled credentials for that provider. Shares aggregate every API key
and model belonging to the user. An 80% share of one account configured for 100
requests permits 80 attempts. Adding a second such account permits 160 attempts
in that same window, including the attempts already consumed.

Configure the tenant-policy plugin:

```yaml
pools:
  codex:
    unit: requests
    capacity_per_account: 100
    window_seconds: 18000
    accounts:
      optional-stable-auth-id-for-larger-tier: 200
  openai-compatible-nvidia-nim:
    capacity_per_account: 100
    window_seconds: 3600
models:
  subscription-model:
    provider: codex
    upstream_model: actual-upstream-model
    pool_only: true
```

These capacities and windows are illustrative administrator settings, **not
provider quotas**. Use the actual configured provider identifier (including the
configured name of an OpenAI compatible provider). Per-account overrides use the
host's stable credential IDs. A zero default capacity requires explicit overrides
for every enabled account; an unknown account then fails admission.

```sh
tenant-policy --state /absolute/private/ledger.json user-set alice 0 codex
tenant-policy --state /absolute/private/ledger.json user-share alice codex 80
```

`pool_only: true` requires a configured pool and bypasses USD pricing for that
model. Other models retain their USD allowance and additionally consume pool
capacity if their provider has a pool. Provider access and a provider share are
both required. Shares range from 0 through 100; the proxy also enforces the global
pool remaining capacity, so multiple users' individual shares can total more than
100 without overspending the pool. Fractional request allowances round downward.

The host supplies enabled account inventory independently of scheduler candidates.
Cooling, rate-limited, depleted and model-ineligible enabled credentials count
in the denominator. Disabled or removed credentials do not. Removing accounts
reduces the denominator immediately without erasing past user or global usage.
Upstream availability may rate limit requests before the configured proxy pool
runs out. This configured request mode is an explicit alternative to reported
quota mode below.

Windows are fixed UTC intervals anchored at the Unix epoch, independently per
provider, and persisted with usage. Changing a reset duration is rejected until
the existing window expires; a backwards clock does not refresh the allowance.
The pool and USD charges commit under the same ledger lock. Every admitted real
upstream attempt costs one request, including retries and failed attempts;
charges are retained when usage reports are absent. No refund or token-unit
conversion is implemented. `unit: tokens` and other unsupported units fail
configuration rather than silently approximating quotas.


## Provider-reported remaining quota

Use reported mode for subscriptions and volume limits without a fixed monetary
budget. Each enabled account contributes one normalized unit by default. An
owner with a 100% share can use the pool; a friend with 50% can use up to half.
Adding an account increases the same percentage denominator without clearing
already attributed use. Tier differences require explicit administrator weights;
plan names never imply a capacity. Numeric headers are normalized as the remaining
fraction of the reported limit, so differing request/token units are not added.

```yaml
pools:
  codex:
    mode: reported
    reported_max_age_seconds: 120
    account_weights:
      optional-larger-account-auth-id: "2"
models:
  subscription-model:
    provider: codex
    upstream_model: actual-upstream-model
    pool_only: true
```

Configure user shares with `user-share owner codex 100` and
`user-share friend codex 50`. Mode `reported` cannot also specify fixed request
capacities, units, or UTC windows. No silent fallback occurs.

The host supplies upstream response signals when they are available. Supported observations are Codex used percentages and reset timestamps,
Claude unified utilization and reset timestamps, and complete numeric
`x-ratelimit-limit/remaining/reset` triples (optionally suffixed `-requests` or
`-tokens`). Generic observations work with custom OpenAI compatible provider
names. Availability depends on headers actually emitted by the account/endpoint;
NIM and other providers are not assigned invented totals. Groq documents its
[remaining and reset headers](https://console.groq.com/docs/rate-limits).
Subscription headers are provider-specific best-effort signals already captured
by this repository; they are not guaranteed public contracts.

If no real observation exists yet, an administrator can import an actual quota
watermark obtained from the provider. Use the stable host credential ID and the
real observation time; import all currently active windows. For example, replace
these variables with the observed values, rather than invented capacity:

```sh
tenant-policy --state /absolute/private/ledger.json quota-observe codex "$AUTH_ID" "$OBSERVED_AT_RFC3339" "$OBSERVED_HEADERS_JSON"
```

Seeds are administrator-supplied trusted evidence, not automatic provider probes.
Newer upstream response observations replace seeds when available. Missing,
stale, malformed, rounded, or unchanged observations do not block a request and
do not trigger a wait, quota poll, or provider probe. `reported_max_age_seconds`
is retained for configuration compatibility and is advisory only; it is not an
admission gate. A positive renewing grant admits use unless locally known,
unexpired observations show the selected account has no remaining quota or the
user's tracked share is exhausted. Known exhaustion continues to block while its
provider reset window is unexpired, even when the reading is stale. Once that
window expires it no longer blocks; new provider observations are incorporated
when ordinary requests return them. Reset periods follow provider timestamps,
never a presumed UTC interval.

Each admitted attempt is counted for later attribution, but requests do not wait
for attribution. When a provider reports a changed numeric balance, the decrease
since the last changed snapshot is estimated across users in proportion to their
admitted-attempt counts. Any changed numeric balance closes the interval and
restarts those counts; an increase adds observed capacity but does not allocate
usage. Identical rounded readings keep the counts accumulating without blocking
requests. This estimate does not know which request caused provider-reported
usage: delayed observations,
external account use, and provider failures weaken accuracy. With no prior
numeric baseline, earlier balance changes cannot be attributed. Actual provider
remaining quota is authoritative; this estimate is only for per-user share
accounting. Requests have unknown quota cost in advance, so usage can overshoot
a user's share before known exhaustion is reported. Per-key token and USD metrics
remain separately available; normalized quota fractions are not dollar costs.

## Named account pools and native credits

A named pool groups explicitly selected accounts. The operator can keep a private
pool and share another pool with a friend. Adding a subscription to the private
pool does not expand the friend's allowance. Adding an account matching a shared
pool's glob expands every grant on that pool; use an explicit list when membership
should require an intentional configuration edit.

Register friendly account names against the actual credential `id` shown by the
current credentials API or administrator credential inventory. Use `id`, not
`auth_index`. Names cannot be rebound to another provider or credential.

```sh
tenant-policy --state /absolute/private/ledger.json account-set codex_sub_1 codex "$ACTUAL_AUTH_ID_1"
tenant-policy --state /absolute/private/ledger.json account-set codex_sub_2 codex "$ACTUAL_AUTH_ID_2"
tenant-policy --state /absolute/private/ledger.json user-set owner 0 codex
tenant-policy --state /absolute/private/ledger.json user-set friend 0 codex
tenant-policy --state /absolute/private/ledger.json user-share owner codex_shared 100
tenant-policy --state /absolute/private/ledger.json user-share friend codex_shared 50
tenant-policy --state /absolute/private/ledger.json user-credit-share owner codex_shared 100
tenant-policy --state /absolute/private/ledger.json user-credit-share friend codex_shared 50
```

```yaml
pools:
  codex_shared:
    provider: codex
    account_selectors: [codex_sub_1, codex_sub_2] # or ["codex_sub_*"]
    mode: reported
    reported_max_age_seconds: 120
models:
  subscription-model:
    provider: codex
    upstream_model: actual-upstream-model
    pool: codex_shared
    quota_resource: renewing
    pool_only: true
```

Grants are independent ceilings, not reservations. Three users may each have
100% renewing and credit shares on `codex_shared`: 300% provisioned is allowed.
A 100% owner plus a 50% friend is also valid. The administrator manages actual
usage patterns; each user's own percentage and actual provider remaining quota
or credit balance still limit requests. No aggregate grant limit is imposed.

Named pools require reported mode. Existing provider-keyed configured request
pools remain supported. Provider access is still required in addition to the
named pool grant. Unregistered, disabled, or unmatched accounts cannot contribute
capacity or execute a named pool request. Accounts in overlapping pools retain
one user's actual-account consumption; changing pool names does not restore
allowances. Removed members' historical consumption remains until renewal or an
explicit reset. Disjoint pools have independent grants.

`quota_resource` defaults to `renewing`; `credits` uses only native numeric credit
balances and the separate credit share. `auto` tries renewing first and falls back to credits only after actual
reported renewing capacity is exhausted. A user reaching their renewing share
cannot use automatic fallback to bypass that share.
`pool` restricts a model to one named pool; omitting it tries granted provider
pools in name order. This controls proxy admission, not how the upstream bills a
request. It cannot force credit spending, bypass provider throttling, or make an
account eligible for a model the provider disallows.

For Codex, credits come from the actual `x-codex-credits-balance` response signal.
An illustrative account with **62500 native credits**, as supplied by its owner,
contributes 62500 credits, not a guessed dollar or token amount. A friend with 50%
can consume up to 31250 observed credits from that initial balance. Balances
must be finite numeric values to contribute to the credit-share denominator;
unlimited flags are not guessed as finite capacity. Other providers require `credit_balance_header` identifying
an actual numeric balance signal captured by the host. Renewing observations are
not required for a credits-only account. Top-ups and new selected accounts add
capacity without clearing attributed consumption. The denominator includes only
accounts with an actually observed balance; if any selected pool member is
unknown, the user's percentage cannot be conclusively enforced, so a positive
credit grant remains permissive. A selected account with a known zero balance
blocks credit use until a newer positive balance is observed. A manual reset
clears attributed usage and counters but preserves actual remaining balance, so
it does not unblock a zero balance. Known user-share exhaustion also blocks.
Missing, stale, malformed, or identical balance reports do not block requests or trigger a wait, poll, or
probe. `reported_max_age_seconds` does not gate credit admission either. Changed
balance snapshots estimate the decrease across users in proportion to their
admitted-attempt counts since the previous changed snapshot. Any changed numeric
balance restarts those counts; an increase adds observed capacity but does not
allocate usage. This estimate does not establish which request caused the
reported change; delayed observations, external account use, and provider failures weaken
accuracy. Without a prior numeric baseline, earlier changes cannot be
attributed. Actual provider balance remains authoritative. The request that
crosses a share can overshoot because its credit cost is unknown before
execution. Credits stay in their native units and are never converted to tokens
or dollars.

Import an actual native balance to bootstrap a credits-only account:

```sh
tenant-policy --state /absolute/private/ledger.json credits-observe codex "$ACTUAL_AUTH_ID_1" "$OBSERVED_AT_RFC3339" 62500
```

Promotional early renewals/free resets are operator actions. First obtain a
current actual observation, then reset the provider's attribution baseline:

```sh
tenant-policy --state /absolute/private/ledger.json quota-reset renewing codex_shared "provider promotional reset" codex_sub_1
tenant-policy --state /absolute/private/ledger.json quota-reset credits codex_shared "start fresh from actual remaining credits" codex_sub_1
```

Use a named pool to reset only its currently selected accounts; pool definitions
are saved in the ledger after successful plugin configuration. The optional
account accepts its friendly name or actual credential ID and must belong to the
pool. Omit it to reset every observed member. A provider name instead explicitly
resets provider-wide. Actual accounts shared by overlapping pools are affected
in every pool, while separate private accounts retain their usage.
Renewing resets retain actual remaining fractions and provider period ends;
credits resets use the actual remaining balance as the new capacity baseline and
do not replenish a known zero balance.
The ledger retains a timestamp, resource, provider, account and reason audit.
Resets do not change archived keys, metrics, USD spend, attempt deduplication, or
provider balances. Expired renewing periods stop blocking admission immediately;
new observations update the accounting when ordinary upstream responses provide
them.
