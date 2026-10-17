# Shared image Files

`/v1/files` serves OpenAI and Anthropic clients from one private, durable, caller-owned image store. There is no protocol prefix or prefixed alias. Ordinary `/v1/messages`, `/v1/chat/completions`, and `/v1/responses` retain their normal model selection, routing, streaming, non-streaming, and reasoning behavior.

## Configuration

Merge into the existing v8 `multimedia` section:

```yaml
multimedia:
  files:
    enabled: true
    storage-dir: /var/lib/cli-proxy-api/files
    max-files: 1000
    max-storage-bytes: 10737418240
    cleanup-interval-seconds: 300
```

Storage defaults to `files` next to the configuration file. Relative paths resolve against that directory. Mount the storage directory persistently and use one proxy process per store. The default quotas are 1000 live files and 10 GiB; cleanup defaults to 300 seconds. Disabled is the default. The legacy `multimedia.codex-files-prototype.enabled` also enables this same store, with its existing compressed-upload, file-count, and TTL limits. It does not create a second API namespace. Enable/disable, per-upload limits, and TTL settings update on config reload. Storage directory, aggregate storage quotas, file-count quota, and cleanup interval changes require a proxy restart and produce a reload warning while a store is open. Existing files retain their published expiry.

To reuse an existing durable bridge store, set `storage-dir` to its existing directory. No files are moved automatically. Remove the bridge's `anthropic` configuration and model aliases; use the ordinary configured upstream model IDs. Downstream clients use `http://localhost:8317` or their adapter's canonical `/v1` base instead of a protocol-prefixed base. DSH models should not advertise the removed bridge's `systemPromptUpdate: in-history` or `toolUpdate: addition-only` extensions; omit these settings to serialize ordinary Messages.

## API formats

| Method | Path | Operation |
|---|---|---|
| POST | `/v1/files` | Upload multipart image |
| GET | `/v1/files` | List caller-owned, unexpired files |
| GET | `/v1/files/:file_id` | Retrieve metadata |
| GET | `/v1/files/:file_id/content` | Download original bytes |
| DELETE | `/v1/files/:file_id` | Delete local metadata and bytes |

OpenAI is the default representation. `Anthropic-Version` or an Anthropic Files beta header selects Anthropic metadata, pagination, deletion, and errors. Neither header is a model or version allowlist. Bearer authentication and native Anthropic `x-api-key` both enter the existing frontend authentication pipeline. Ownership comes from its authenticated principal, never attribution headers or selected upstream credentials. An authenticated principal is required even if the broader server permits anonymous inference.

OpenAI multipart uploads require `purpose=vision` or `purpose=user_data`; Anthropic uploads default to `user_data`. Originals may be PNG, JPEG, WebP, or GIF; malformed images, incompatible MIME declarations, and storage quota exhaustion fail at upload. This is an image attachment API, not PDF/document ingestion, fine-tuning, Assistants, or arbitrary Files-purpose support. Without legacy limits, uploads are bounded at 128 MiB and expire after seven days by default. Optional `expires_after[anchor]=created_at` and `expires_after[seconds]` allow one hour through 30 days, within any legacy TTL cap. A shorter configured legacy default TTL remains supported for uploads that omit explicit expiry. This proxy expiry extension is not a claim of full upstream Files compatibility.

OpenAI list accepts `limit`, `after`, `order`, and `purpose`; Anthropic accepts `limit`, `after_id`, and `before_id`. Original-content download returns the exact uploaded bytes. Restarting on the same directory preserves IDs, ownership, purpose, and bytes. Local deletion or expiry does not delete copies already sent to upstream services.

## Referencing images

The same returned ID can be used by either protocol, including image content in nested tool results:

```json
{"type":"image","source":{"type":"file","file_id":"RETURNED_ID"}}
```

```json
{"type":"input_image","file_id":"RETURNED_ID"}
```

Ordinary Messages and Chat Completions resolve local images into their supported inline image representation. Responses use execution-time native uploads after selecting upstream credentials; failed required uploads stop inference rather than silently falling back inline. Responses retries recheck local ownership, deletion, and expiry and upload with each selected credential. Messages and Chat resolve originals once when preparing the request. Supported native upload origins remain the responsibility of the provider Files adapters; a shared local API does not establish native upload support for every upstream. Signed thinking, redacted thinking, caller-supplied upstream file IDs, and ordinary model routing are not rewritten by this resolver.

File IDs keep image bytes out of downstream serialized conversation history; they do not reduce image token/context costs. Upstream inference errors retain their original status, structured body, code/type, and retry metadata for the caller; private request and upstream body logging remain suppressed independently. Upload/content routes and resolved local-image upstream requests suppress raw body/error capture. Do not include originals, credentials, signed URLs, or storage paths in diagnostics. Inline Messages and Chat requests transmit original images upstream, subject to that provider's retention and account policies.

## Validation

Focused offline tests cover both representations using one store, cross-protocol reuse and deletion, ownership isolation, durable restart, original image bytes, pagination, errors, request resolution, and required native uploads. Offline checks do not establish live upstream image understanding or deployment readiness. Obtain approval before rebuilding/restarting a deployed proxy, accessing credentials, or sending any image upstream.
