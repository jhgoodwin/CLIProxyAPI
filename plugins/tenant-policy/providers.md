# Tenant Policy Provider Configuration

[The provider example](../../examples/tenant-providers.yaml) configures upstream
credentials using the v8 configuration schema. Copy it to a private file,
replace all placeholders, and remove unused provider groups. Combine it with the
[tenant plugin configuration](README.md#server-configuration) to enforce user
access and accounting; the provider example alone does not enforce tenant
policy.

Prepare the plugin, ledger, and tenant keys before exposing the listener. Stop
the server, set `access.api-keys: []`, enable the plugin with
`plugins.configs.<pluginID>.required: true`, and restart the ordinary
`cmd/server` entrypoint. A top-level `plugins.required` field does not enforce
plugin loading. Clients must use keys issued by the tenant plugin.

## Provider identifiers

Policy must authorize the actual runtime provider identifier, not a model's
vendor or its visible prefix. Compatibility provider names become
`openai-compatible-<name>` internally; a name already starting with
`openai-compatible-` is preserved. An OpenRouter request remains an OpenRouter
request even when its model is made by Anthropic or OpenAI.

| Requested provider | Runtime provider identifier | Credential/configuration | Example client model |
| --- | --- | --- | --- |
| Claude Pro/Max | `claude` | Existing Claude OAuth login | Use an entitled model ID from the account catalog |
| Codex Plus/Pro | `codex` | Existing Codex OAuth login | Use an entitled model ID from the account catalog |
| Devin Pro | `devin` | Existing Devin CLI OAuth login | Use an entitled model ID from the account catalog |
| DeepSeek | `openai-compatible-deepseek` | Named compatibility group | `deepseek/flash` |
| Cerebras | `openai-compatible-cerebras` | Named compatibility group | `cerebras/oss-120b` |
| NVIDIA NIM | `openai-compatible-nvidia-nim` | Hosted or self-hosted compatibility group | `nvidia-nim/oss-120b` |
| OpenRouter | `openai-compatible-openrouter` | Named compatibility group | `openrouter/oss-120b` |
| Groq | `openai-compatible-groq` | Named compatibility group | `groq/oss-120b` |
| Local vLLM | `openai-compatible-vllm` | Local compatibility group | `vllm/local` |
| Other OpenAI-compatible endpoint | `openai-compatible-endpoint` | Named compatibility group | `openai-compatible/custom` |

Distinct prefixes and `routing.force-model-prefix: true` prevent these API-key
groups from participating in unprefixed requests. Prefixes do not replace
authorization. Avoid shared aliases across providers unless a deliberate
authorized pool is wanted. Model examples are illustrative: check availability
with each upstream's model catalog and your credentials before deployment.

## Subscription credentials

Acquire account credentials using the existing server binary, with the same auth
directory/configuration the plugin-enabled server will load:

```bash
go run ./cmd/server --config ./private-providers.yaml --claude-login
go run ./cmd/server --config ./private-providers.yaml --codex-login
go run ./cmd/server --config ./private-providers.yaml --devin-login
```

These commands open the existing account login flows and write credentials to
`oauth.auth-dir`. Pro/Max and Plus/Pro identify account plans, not distinct
runtime providers. This configuration does not grant access beyond the logged-in
account's model entitlements or quotas. Choose model IDs from the account's
provider catalog and verify entitlement with a request. Required tenant mode
currently denies `/v1/models`, so client model discovery is unavailable.

The existing Devin executor uses the Codeium/Devin Connect-RPC model backend and
Devin CLI authentication. It provides translated inference/streaming through the
proxy. Its standalone token count is a payload-length heuristic, and it cannot
send every media representation accepted by other protocols. Actual account
entitlement must be verified with a real login and request. This is separate
from the [official Devin session
API](https://docs.devin.ai/api-reference/overview), which manages coding-agent
sessions, knowledge and organization resources; an official session API key is
not an OpenAI inference key. The executor does not adapt sessions into chat
requests, and a Devin Pro plan alone does not establish model-backend
entitlement.

## Endpoint references

The API-key providers use the existing OpenAI-compatible executor; no separate
executor is needed for ordinary chat-completion requests. Compatibility does not
imply every provider supports all OpenAI endpoints, tools or media formats.

- [DeepSeek first API call](https://api-docs.deepseek.com/): base URL `https://api.deepseek.com`; the example uses `deepseek-flash`.
- [Cerebras OpenAI compatibility](https://inference-docs.cerebras.ai/resources/openai): base URL `https://api.cerebras.ai/v1`; select an exact available model ID.
- [NVIDIA GPT OSS reference](https://docs.api.nvidia.com/nim/reference/openai-gpt-oss-120b) and [NIM API reference](https://docs.nvidia.com/nim/large-language-models/1.12.0/api-reference.html): hosted NIM uses `https://integrate.api.nvidia.com/v1`; self-hosted NIM exposes OpenAI-compatible `/v1/chat/completions`.
- [OpenRouter quickstart](https://openrouter.ai/docs/quickstart): base URL `https://openrouter.ai/api/v1`; retain the complete upstream model slug.
- [Groq compatibility](https://console.groq.com/docs/openai) and [model catalog](https://console.groq.com/docs/models): base URL `https://api.groq.com/openai/v1`; the example uses `openai/gpt-oss-120b`.
- [vLLM OpenAI-compatible server](https://docs.vllm.ai/en/stable/serving/openai_compatible_server/): match the local server's served model name and API key; the example assumes port 8000.

## Pricing and limits

Use [LiteLLM's model pricing
catalog](https://github.com/BerriAI/litellm/blob/main/model_prices_and_context_window.json)
as input to tenant pricing. Bind each permitted runtime provider and client
model to an explicit catalog entry or rate override. Client aliases such as
`oss-120b` alone cannot identify a provider's price. Refresh and review the
catalog when models or provider rates change; missing rates must not silently
become free access.

Subscription and volume-limited accounts use [capacity pools](pool.md) with
independent renewing-quota and native-credit percentages. Use named pools to
separate shared accounts from personal ones. Adding an account expands every
user's absolute allowance in that pool; existing consumption remains. Aggregate
grants may exceed 100%, but known account exhaustion still limits access.

Provider quota observations come from ordinary upstream responses or manual
imports. Positive grants allow requests when quota is unknown or stale; known,
unexpired exhaustion blocks subsequent requests. Checks never wait, poll, or
probe. Attribution is estimated from admitted attempts between changed readings,
not measured provider consumption per request. Native credits remain credits.
Manual resets clear attributed consumption without replenishing actual balances.
See [pool accounting](pool.md#provider-reported-remaining-quota) for details and
reset commands.

For USD admission, map provider/model names to LiteLLM prices or explicit rates
and configure a fixed allowance per attempt. The USD ceiling limits **internal
policy debits**, not actual provider spend; failures and retries retain their
charges. Configure deliberate rates for local vLLM and self-hosted NIM if their
operating cost should count. Catalog rates, negotiated prices, caching, routing,
and discounts can differ from provider invoices.

[Usage reports](metrics.md) show input/output/cache metrics and API-equivalent
cost estimates by user and key. Pool-only providers can carry prices for these
estimates. Revoked keys retain labels and historical attribution. Invoice
amounts and omitted upstream usage remain unknown. A budget or pool grant does
not expand provider entitlements.

This configuration has no Amp fallback.
