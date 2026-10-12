# Disposable provider-neutral Files screenshot adapter

This opt-in prototype exposes a **proxy-owned OpenAI-style Files interface for screenshots**. The original image is stored locally, not uploaded to a Codex backend at file creation. An HTTP Responses request can use the returned opaque ID in message content or structured screenshot tool output; the proxy materializes a bounded image data URI before the existing provider translator runs.

The legacy name `codex-files-prototype` is retained for configuration compatibility. It no longer implies Codex-only uploads, a native upstream file namespace, or upload-account ownership. This is not a universal-provider guarantee or production attachment store.

**File references keep image bytes out of downstream serialized chat/tool history. They do not bypass model visual token/context costs.** The upstream request may contain image bytes inline in a real image field, never as ordinary text merely to accommodate a file reference.

## Approval boundary — do not run live yet

Obtain explicit operator approval before accessing credentials, changing/restarting the existing server, or sending any image upstream. Approval must cover the proxy API key, selected provider credentials and possible refresh, both inference calls and quota/cost, upstream retention despite `store: false`, and approved configuration/rebuild/restart of the existing server. Local Files upload creates local storage; it does not require a ChatGPT reservation, signed blob PUT, or finalize operation.

No live success is claimed by offline tests. Do not inspect credential files, private screenshots, runtime configuration, or dotenv files without approval. The server can auto-load dotenv credentials on restart. Do not start a replacement server to run the probe. Local expiry/deletion does not prove deletion of images already sent upstream.

## Exact v8 configuration

Merge into the approved configuration's existing `multimedia` section:

```yaml
multimedia:
  codex-files-prototype:
    enabled: false
    max-bytes: 5242880
    max-files: 128
    ttl-seconds: 3600
```

Disabled is the default. Defaults are 5 MiB per original file, 128 files, and 3600 seconds. Nonpositive or over-limit values reset to defaults; upper configuration limits are 20971520 bytes, 1024 files, and 86400 seconds. `max-bytes` bounds the compressed upload, not model context. No duplicate YAML sections or new top-level key are needed.

## Files interface and inference scope

The screenshot subset provides authenticated Files create/list/retrieve/content/delete. Multipart creation accepts `purpose=vision` or `purpose=user_data`, common PNG/JPEG/WebP images, and the supported `expires_after` option. It is not fine-tuning, Assistants, vector-store, PDF/document ingestion, or every-purpose Files support. This is a narrower screenshot contract, not full public Files API compatibility: files expire by default after the configured TTL (3600 seconds by default), rather than being retained until explicit deletion. The supported `expires_after` uses `anchor=created_at` and seconds from 3600 through the configured TTL, whose configuration maximum is 86400 seconds; it does not offer the public API's full 30-day expiry range.

Files are caller-scoped and provider-neutral. Original bytes and normalized variants are kept in process-private temporary files; metadata is in memory. Original-content retrieval returns the upload exactly, not the resized inference variant. Delete removes both files; expiry pruning is lazy on registry operations, not a background cleanup timer. Restart invalidates IDs; this is not a persistent/shared multi-instance store. Crashes can leave private temporary blobs, and there is no startup orphan purge or secure-erasure guarantee.

There is no configurable aggregate disk-byte budget. The per-file bounds imply up to `128 × (5 + 20) MiB` at default settings and `1024 × (20 + 20) MiB` at maximum settings for retained original/variant payloads, before filesystem overhead and temporary work. File count and TTL are not substitutes for disk-capacity monitoring.

For inference, use **HTTP Responses** with `input_image.file_id` in either message `content` or structured `function_call_output.output`. The client sends a reference only; the proxy reads the disk-backed normalized variant and applies existing provider translation. No native hosted-file upload or same-owner affinity is required. Supported routing includes Codex, OpenAI, Claude, Gemini (including Gemini CLI), and configured named OpenAI-compatible providers using the nonempty reserved runtime family `openai-compatible-*`. This does not establish vision support for every model or endpoint in that family; plugin routing remains unsupported. Known model input modalities without image support produce HTTP 501. Missing/unknown modality metadata does not reject the request, but also does not establish vision support. A vision-description/OCR fallback is not implemented. Chat and downstream WebSocket attachment requests return HTTP 400; arbitrary MCP wire shapes are not supported by implication.

Upload decoding is bounded to 16384 pixels per side and 64 megapixels, in addition to the configured compressed-byte limit. The generic normalization profile `screenshot-png-2048-v1` produces a PNG using BestSpeed compression, with maximum long edge 2048 pixels and preserved aspect ratio. It never upscales or tiles. Smaller images retain their dimensions and decoded pixels rather than their original encoded format: an 803×444 WebP image, for example, becomes a same-sized PNG. This avoids an additional lossy JPEG encoding, but resizing still loses detail and cannot recover information already lost in the source. Normalized output is bounded to 20 MiB.

Memoization is scoped by caller, source-content hash, profile version, and the generic vision profile. Duplicate uploads can reuse a cached normalized variant, but each file retains its own original and variant copies: this is not disk deduplication. Reference requests read the cached disk variant without decoding/resizing again. This is one generic policy, not provider-specific optimization; no mandatory tiling, OCR, or universal image-limit compatibility is promised.

## Probe primary path and privacy

The stdlib-only [probe](<../scripts/codex_files_probe.py>) generates a random six-character high-contrast bitmap-font RGB PNG entirely in memory. The expected answer is kept local, never printed or added to prompts, metadata, filenames, or history.

1. Send a fresh inline-image HTTP Responses control with a neutral OCR prompt, `tools: []`, `tool_choice: "none"`, and `store: false`.
2. Compare locally; on mismatch **stop before upload**.
3. Upload the PNG as `probe.png` with `purpose=vision` to local Files storage.
4. Send a separately constructed fresh Responses request containing only `{"type":"input_image","file_id":"<proxy ID>"}` for the image. No inline bytes, prior answer/history, or `previous_response_id` is sent by the client.
5. Compare locally and report match/mismatch only. The probe leaves its local file for expiry or explicit deletion; it does not claim cleanup occurred.

This probe exercises the message-content path, not a complete coding-session/tool-output E2E scenario or Files CRUD suite.

Both Responses requests set `X-CPA-Files-Probe: 1` to suppress supported request/response body capture before logging. Files operations receive scoped privacy protection. This is not a guarantee about unrelated reverse proxies or upstream retention. The script never prints answers, credentials, file IDs, arbitrary remote errors, URLs, images, or complete bodies. Error bodies are bounded to 65536 bytes; only the six pre-existing exact case-insensitive file-not-found diagnostic phrases are allowed through. All other details are suppressed. Redirects and ambient HTTP proxies are disabled; no network timeout is introduced.

## Evidence, not inference

The probe distinguishes client serialization from actual upstream send-boundary observations:

- `X-CPA-Files-Stage: ready` means **local file storage is ready**, not remote-provider readiness.
- `X-CPA-Image-Bytes` reports original uploaded image bytes.
- `X-CPA-Final-JSON-Bytes`, `X-CPA-Final-Input-Count`, and `X-CPA-Final-Content-Types` report actual final send-boundary metrics where implemented.
- The obsolete `X-CPA-Same-Owner` header is ignored. There is no Codex upload owner to compare.

Missing/invalid diagnostics stay `unknown`. No final byte header means `send_boundary_evidence: "no"`. A reference-only **client** request can legitimately become an inline-image **upstream** request, so neither small client payloads nor file creation prove reduced upstream context or native hosted-file use. Count headers alone do not prove image integrity or universal provider support.

## Offline commands

From the repository root, without credentials or a running application:

```bash
PYTHONDONTWRITEBYTECODE=1 python3 scripts/codex_files_probe.py --self-test
PYTHONDONTWRITEBYTECODE=1 python3 scripts/codex_files_probe.py --help
```

The twelve self-tests use ephemeral loopback fake HTTP services, not a provider/application server. They cover PNG CRC/decompression and glyph pixels, JSON/SSE primary paths, stopping before upload on mismatch, error sanitization, reference-only client shape/privacy headers, and scalar diagnostics. They do not prove provider translation or live model acceptance. No image answers are output.

### Developer implementation gates

These commands verify local code/tests and compilation, not provider acceptance or deployment. They do not launch an application server. Test fixtures must remain synthetic/local; do not provide credentials or request live-provider tests.

```bash
go test ./...
go build -o test-output ./cmd/server
# Verify the absolute path is the intended repository artifact before deleting it:
pwd
realpath test-output
# Only after that verification:
rm ./test-output
```

After Go source changes, the repository also requires `gofmt -w .`. Do not infer that running these commands authorizes a service restart or live probe. Build/cache filesystem permission failures must be handled by the supervising operator, not bypassed through alternate paths.

## Approved operator commands only

These are instructions, **not authorization to execute**. Preserve the existing service endpoint, configuration path, launch flags, and service-management method. Enable the stanza only after approval; rebuild/restart that existing service if needed rather than creating another server.

```bash
# After approval only; select the existing model under test.
read -r -p 'Approved proxy origin or /v1 URL: ' PROXY_URL
read -r -s -p 'Approved proxy API key: ' PROXY_API_KEY; printf '\n'
read -r -p 'Approved provider model: ' CODEX_MODEL
export PROXY_URL PROXY_API_KEY CODEX_MODEL
PYTHONDONTWRITEBYTECODE=1 python3 scripts/codex_files_probe.py
# Optional SSE run is a NEW upload and two inference calls; approval required.
PYTHONDONTWRITEBYTECODE=1 python3 scripts/codex_files_probe.py --stream
unset PROXY_API_KEY PROXY_URL CODEX_MODEL
```

`CODEX_MODEL` is the retained legacy variable name: it can select any approved model exposed by the proxy, but does not assert vision capability. `PROXY_URL` accepts an origin or `/v1` base without userinfo/query/fragment. The key authenticates to the proxy, not directly to a provider. The script reads no other environment settings or credential/dotenv files. Use approved HTTPS for non-loopback access.

## Validation boundary

Offline passing checks demonstrate local API/request behavior only. They do not establish universal provider support, live image understanding, quota/retention behavior, or deployment readiness. The generated test PNG is 468×132 with dark glyphs on white. User-provided screenshots should not be sent upstream without separate approval; contract tests can use local fixtures and mocked upstream transports instead.
