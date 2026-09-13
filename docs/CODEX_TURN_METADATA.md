# Codex turn metadata encoding

`x-codex-turn-metadata` is an embedded JSON string carried in HTTP headers and
in `client_metadata` on Responses WebSocket requests. Rewriting account identity
or device/session fingerprints must preserve its header-safe encoding.

All four rewrite locations (account identity and fingerprint, each in headers
and request bodies) use `marshalCodexTurnMetadata`:

- Keep the serialized metadata in printable ASCII. Encode non-ASCII characters
  and DEL using JSON Unicode escapes, including surrogate pairs for non-BMP
  characters.
- Preserve decoded workspace paths, field names, values, literal backslashes,
  and existing escape semantics. Only the existing identity/fingerprint fields
  are changed by their respective policies.
- Leave ordinary request content, tool definitions, routing, failover, and
  cooldown behavior unchanged. Do not replace per-turn metadata with handshake
  metadata or drop workspace information to avoid encoding failures.

This prevents OAuth passthrough from converting escaped Chinese workspace paths
into raw non-ASCII metadata that causes the upstream to close before a terminal
response event. The same serializer also covers the equivalent HTTP and pooled
WS identity/fingerprint rewrite locations.

Regression verification:

```sh
cd backend
go test -tags=unit ./internal/service -run 'TestCodexTurnMetadata|TestCodex.*Identity|TestFingerprint' -count=1
```

Deployment acceptance must use the existing global Codex configuration and a
workspace with a non-ASCII path. Verify real tool execution and continuation over
WS, successful account failover when triggered, and absence of HTTP fallback.
A diagnostic proxy that alters metadata is not a substitute for this acceptance.
