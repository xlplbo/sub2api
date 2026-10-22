# Composite Groups

Composite groups are an admin routing layer for API keys that should choose a
concrete provider from the requested model instead of binding the key to a
single provider group. They support both built-in model detection and an
admin-configured model route registry for public model aliases.

## Supported Providers

Composite groups can route to these concrete account platforms:

- Anthropic
- Gemini
- OpenAI
- Antigravity
- Grok
- Kimi
- Zhipu GLM
- DeepSeek
- MiniMax

The selected concrete platform is used for account selection, user platform
quota checks, post-usage billing, ops error platform attribution, channel
mapping/pricing lookup, and platform usage reporting.

## Route Registry

Admins can configure routes on a composite group from the group list's
`Routes` action or through the admin API:

- `GET /api/v1/admin/groups/:id/composite-routes`
- `POST /api/v1/admin/groups/:id/composite-routes`
- `PUT /api/v1/admin/groups/:id/composite-routes/:route_id`
- `DELETE /api/v1/admin/groups/:id/composite-routes/:route_id`
- `POST /api/v1/admin/groups/:id/composite-routes/preview`

Each route belongs to one composite group and contains:

- `public_model`: model identifier the client sends.
- `match_type`: `exact` or `prefix`.
- `target_platform`: concrete provider platform.
- `upstream_model`: model identifier sent upstream. If omitted, the public
  model is reused.
- `endpoint`: `any`, `messages`, `count_tokens`, `responses`,
  `chat_completions`, `embeddings`, `images`, or `gemini`.
- `request_condition`: `any` (default, all requests) or `has_image` (submitted
  message content contains an image). Existing routes remain `any`.
- `priority`: lower values win after match specificity.
- `enabled`: disabled routes are ignored by runtime resolution but remain
  visible to admins.

Resolution order is explicit route first, then built-in detection. When more
than one explicit route matches, exact matches beat prefix matches,
endpoint-specific routes beat `any`, longer prefixes beat shorter prefixes,
then `has_image` beats `any` for image-bearing requests, then lower `priority`,
then lower route id. Text requests ignore `has_image` routes. The same model,
match type and endpoint can have one active rule for each condition.

To route a text-only public model to a vision model, keep its ordinary route
and add another route with the same public model, match type and endpoint,
set `request_condition` to `has_image`, and choose the target platform and
upstream model that support image input. No model capability is inferred or
hardcoded; the administrator is responsible for selecting a vision-capable
target and making its accounts available in the composite group.

The route preview accepts `has_image: true`; the UI exposes this as a checkbox.
Image detection covers Anthropic `image`, OpenAI Chat `image_url`, Responses
`input_image` (URL or file ID), and Gemini image `inlineData` / `fileData` parts
(including snake_case fields). Submitted history and structured tool results
are included. Responses function/custom/MCP/tool-search outputs support image
arrays, image objects, `content` wrappers, JSON-string output and bare image
data URLs, matching the existing media conversion bridge.
Text mentioning images, tool definitions, metadata, audio and
video do not trigger image rules. Images are not decoded or fetched during
routing. Image generation/edit multipart requests and remotely stored Gemini
cached content are outside this content detector.

When Responses text and image destinations differ, the gateway records the
selected destination for response IDs in Redis for one hour, isolated by user,
group and public model. HTTP also records returned conversation IDs. HTTP
continuations and WebSocket reconnects reuse that destination, including text
following an image.
The original reference is preserved for the upstream's normal continuation.
Unknown or expired references, changed/removed destinations, and adding images
to a text continuation that requires a different destination return HTTP 400
(WebSocket policy violation), requesting full history without the references.
The gateway cannot recover unseen provider-held history or transfer it to
another model. Full-history requests include earlier images and select the
image route again. Cache failures do not silently select another destination.
Route resolution reads the group's rules once per request. `image_url` data
URLs and bare tool-output data URLs with empty or whitespace-only base64
payloads do not activate image conditions.

For JSON-body endpoints, the gateway rewrites the request `model` field to the
route's `upstream_model` before dispatch. For Gemini native paths such as
`/v1beta/models/{model}:generateContent`, the gateway resolves `{model}` and
the handler forwards the resolved upstream model.

Codex Alpha Search and Live requests use the `responses` route domain. Live
requests resolve the model from `session.model`, including multipart `session`
payloads, and apply the configured `upstream_model` before dispatch.
Codex model manifest requests reuse the existing OpenAI account selection and
failover path within the Composite group.

Responses WebSocket resolves the first `response.create` model in the
`responses` route domain (including `any` routes). Only OpenAI and Grok targets
are supported. The public model remains the admission and response identity;
the route's upstream model is applied before channel/account mapping. Subsequent
turns can repeat or omit the public model. Changing it requires reconnecting so
the gateway can resolve the route and select an account again.
The first frame also selects the image condition. An image-first connection
keeps its route for later text turns. If a text-first connection later sends an
image requiring another platform or upstream model, the gateway closes it with
a policy violation and asks the client to reconnect with full history.
Initial frames using continuation references follow the same history check as
HTTP. Public model admission and usage attribution remain unchanged; the actual
target model is recorded through the existing upstream model fields.

## Built-In Detection

Composite routing detects common public model IDs and provider-prefixed IDs:

- `claude-*` and `anthropic/claude-*` route to Anthropic.
- `gemini-*` and `google/gemini-*` route to Gemini.
- `gpt-*`, `o*`, `codex-*`, `text-embedding-*`, `dall-e-*`, and
  `openai/*` route to OpenAI.
- `grok-*` and `xai/grok-*` route to Grok.
- `kimi-*`, `moonshot-*`, and `k3` route to Kimi.
- `glm-*` routes to Zhipu GLM.
- `deepseek-*` routes to DeepSeek.
- `minimax-*`, `abab*`, and `minimax/*` route to MiniMax.

Unknown or ambiguous model names fail closed with a client error instead of
guessing a provider.

## Admin Workflows

- Admins can create a group with platform `composite`.
- Admins can add, edit, delete, and preview composite model routes.
- Composite groups can copy accounts from concrete provider groups.
- Concrete provider accounts can be assigned directly to composite groups from
  account create/edit and bulk account workflows.
- Subscription payment plans can bind to a composite group when that group's
  `subscription_type` is `subscription`. The plan grants access to the
  composite group; each request is still billed and quota-checked against the
  resolved concrete provider platform.
- Channel configuration exposes composite groups in concrete provider sections.
  The channel `group_ids` payload is still flat; provider-specific model
  mapping and pricing remain keyed by concrete platform.

## Bucket 2 Setup: OpenAI + Claude + Gemini + Grok

Use one composite subscription group when one customer-facing plan should expose
model aliases across OpenAI, Claude, Gemini, and Grok without issuing separate
keys per provider.

1. Create concrete provider groups for the upstream account pools, for example
   `OpenAI Paid`, `Claude Paid`, `Gemini Paid`, and `Grok Paid`.
2. Create a `composite` group with `subscription_type` set to `subscription`.
3. Assign provider accounts directly to the composite group, or copy accounts
   from the concrete provider groups during group creation.
4. Add explicit routes for public aliases that should not rely on built-in
   model detection:

   | Public model | Endpoint | Target platform | Upstream model |
   | --- | --- | --- | --- |
   | `all/gpt-5` | `responses` | `openai` | `gpt-5` |
   | `all/claude-sonnet` | `messages` | `anthropic` | `claude-sonnet-4-6` |
   | `all/gemini-pro` | `gemini` | `gemini` | `gemini-2.5-pro` |
   | `all/grok` | `responses` | `grok` | `grok-4.3` |

5. Configure channel pricing and model mapping under the concrete platforms
   named in each route. Composite routing does not create pricing records.
6. Create a subscription payment plan for the composite group.

The same composite group can also rely on built-in detection for standard model
names such as `gpt-*`, `claude-*`, `gemini-*`, and `grok-*`. Explicit routes are
recommended for bundled plan aliases because they make endpoint, provider, and
upstream model attribution reviewable in the admin UI.

## Limits

Composite routes choose a concrete provider and upstream model; they do not
create synthetic model metadata, pricing, or upstream capability records by
themselves. Keep channel pricing/model mapping configured for the concrete
provider platforms that the routes target.

This PR intentionally does not implement:

- AUTO smart-routing among multiple providers for the same abstract task.
- Direct API-key binding to several existing groups without a composite group.
- Protocol-agnostic provider decoupling or a LiteLLM-style adapter rewrite.

## Image Route Verification

Run the detector, resolver, ingress and WebSocket regressions from `backend`:

```sh
go test -vet=off -p 1 ./internal/pkg/requestimage ./internal/service ./internal/server/routes ./internal/handler -run 'TestHasImage|TestComposite|TestOpenAIResponsesWebSocket_Composite' -count=1
```

`TestCompositeModelRouteRepositoryImageCondition` is an integration test using
the existing Docker PostgreSQL harness. It verifies persistence, coexistence,
updates and replacement after soft deletion.

`TestCompositeImageRouteCodexE2E` is an opt-in real Codex smoke test. Set
`SUB2API_IMAGE_E2E_URL` to an existing gateway origin (without `/v1`),
`SUB2API_IMAGE_E2E_KEY` to its test key, `SUB2API_IMAGE_E2E_CODEX_ENTRY` to the
installed Codex `bin/codex.js`, `SUB2API_IMAGE_E2E_PICTURE` to an image containing
a six-digit code, and `SUB2API_IMAGE_E2E_EXPECTED` to that code. Set
`SUB2API_IMAGE_E2E_PUBLIC_MODEL` and `SUB2API_IMAGE_E2E_VISION_MODEL` to models
available through that key. Keep credentials outside repository files.

```sh
go test -tags=integration -vet=off -p 1 ./internal/server/routes -run '^TestCompositeImageRouteCodexE2E$' -count=1 -v -timeout=5m
```

This starts a loopback ingress using the new middleware with in-memory route
fixtures, forwards to the existing gateway, and runs Codex with one-off provider
overrides while retaining its global configuration. It verifies image reading
and a shell tool continuation both reach the selected model. It does not deploy
the new server or replace database-backed integration tests.
