# Usage and cost reporting

The tenant policy records usage for each authorized upstream execution attempt in its durable ledger. Attribution comes from the admission record: user, SHA-256 key identifier, provider, and upstream model. Raw caller secrets, upstream credentials, prompts, response bodies, and failure bodies are excluded. Delivery replay is deduplicated by attempt ID and usage record ID, including across restarts. Distinct retry attempts count independently, including failed executions that report tokens.

```sh
tenant-policy --state /srv/tenant-policy/ledger.json metrics-export json /srv/tenant-policy/usage.json
tenant-policy --state /srv/tenant-policy/ledger.json metrics-export csv /srv/tenant-policy/usage.csv
tenant-policy --state /srv/tenant-policy/ledger.json dashboard /srv/tenant-policy/dashboard.html
```

Commands create private files with mode 0600 and refuse to overwrite existing files. Open the HTML report locally as an administrator. It has no hosted route, external assets, or JavaScript. Reports expose tenant activity and must be protected. CSV labels are escaped against spreadsheet formula execution. Reports include totals and breakdowns by user, key, provider, model, and UTC day. Regenerate the report to refresh its snapshot. Key breakdowns display each key’s current label alongside its stable SHA-256 identifier. JSON/CSV exports include `key_label`, joined from current key metadata; relabeling updates the note shown for all historical usage without changing attribution or totals.

`key-revoke HASH` archives a key by marking it revoked. The key, user, debit, and metrics rows remain available; late usage for its already authorized attempts retains original attribution. There is no hard-delete command. Earlier debit records that predate key attribution remain attributed to an unknown key rather than guessing.

## Token semantics

Input and output totals use the host's canonical non-overlapping token breakdown. Cache hit means cache read; cache miss means uncached input. Cache creation is a separate bucket. Reasoning is part of total output, never added twice. Providers that include cached tokens within input (OpenAI format) and providers that report them separately (Claude) normalize through the host's existing accounting contract.

Incomplete accounting retains positive known buckets and the unclassified remainder, while unknown counters export as JSON null or CSV `unknown`. Inconsistent accounting and missing usage never produce invented totals or a zero cost. JSON also retains reported usage counters for inspecting provider accounting without assigning unknown bucket semantics. Dashboard totals sum known classified buckets; unknown record counts identify incomplete coverage. Zero or omitted usage cannot currently be distinguished reliably by every upstream adapter, so all-zero records have unknown counters. Usage can only be captured when the upstream executor emits a record; interrupted processes and providers that omit usage cannot provide complete accounting.

## USD semantics

Three different amounts remain distinct:

- **Estimated API-equivalent USD** uses the LiteLLM snapshot or explicit input/output rates captured at admission. It is an estimate, including for subscription-backed models, and is not a provider invoice.
- **Internal policy tariff USD** is the admission charge used by configured USD limits. It is counted once per authorized attempt, including attempts with no reported usage. It is independent of observed token cost.
- **Billed USD** is unknown: upstream invoice amounts are not supplied by the current usage contract.

Pool-only models can specify `price_key` against the configured `prices` snapshot for API-equivalent cost reporting without adding a USD admission charge. Explicit per-token input/output rates can also be used. If cache usage is positive but a corresponding cache price is absent, the estimate is unknown. Unsupported additional LiteLLM cost dimensions, nonstandard service tiers, incomplete accounting, or a served model whose price identity does not match the reservation also make the estimate unknown. Reported counters are retained even if a served-model mismatch prevents pricing. Approved dated model aliases do not trigger a policy substitution freeze, but their cost estimate remains unknown until exact price identity is available.

The dashboard labels known-record estimate subtotals and counts records with unknown estimates; a zero subtotal therefore does not imply free usage. Changes to the pricing snapshot or policy affect future admissions only. Existing rows keep their original price snapshot and attribution. Protect the ledger, retain backups, and expect its per-execution history to grow; automatic retention or deletion is deliberately absent so historical attribution survives.
