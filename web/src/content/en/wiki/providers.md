# LLM providers: TOML registry and adapter contract

Status: **draft for discussion**. This document defines the contract of the
**official providers extension** — it is not sacred core API
([api.md](api.md)); it is versioned separately and can evolve faster.
It materializes ADR-005: *TOML declares the data, Lua implements the protocol*.

Two audiences:

1. **User adding a model** (pi's `models.json` case): edits
   `providers.toml`. Zero code.
2. **Adapter author** (new protocol or unusual dialect): writes a
   Lua module that fulfills the §3 contract.

---

## 1. The registry: `providers.toml`

Lives in `enu.config.dir()`. Declares *data*, never logic.

```toml
# Provider with an official adapter: data only.
[providers.anthropic]
adapter     = "anthropic"                  # which adapter speaks its protocol
base_url    = "https://api.anthropic.com"
api_key_env = "ANTHROPIC_API_KEY"          # never the key in the file

[[providers.anthropic.models]]
id         = "claude-opus-4-8"
context    = 200000
max_output = 32000
cost       = { input = 5.0, output = 25.0 }   # USD per Mtok (informational)
aliases    = ["opus"]
thinking   = "adaptive"                        # reasoning dialect (ADR-016):
                                               # "adaptive" (Opus 4.6+), "budget"
                                               # (legacy extended thinking) or "none".
                                               # Default "budget" if omitted.

# The models.json case: OpenAI-compatible endpoint, e.g. local Ollama.
[providers.local]
adapter  = "openai-compat"
base_url = "http://localhost:11434/v1"

[[providers.local.models]]
id      = "qwen3:32b"
context = 32768

# Provider with an exotic protocol: the adapter belongs to a third-party plugin.
[providers.corp]
adapter  = "my-plugin/corp-gateway"        # resolvable via require()
base_url = "https://llm.internal.corp"
extra    = { tenant = "team-7" }           # opaque table, passed to the adapter
```

Model resolution: `"provider/id-or-alias"` (`"anthropic/opus"`,
`"local/qwen3:32b"`). The providers extension resolves the TOML, reads the
API key from the environment and hands the adapter an already-cooked
`ProviderConfig`. `resolve` **does not fail** if the `api_key_env` variable
isn't in the environment: it hands over the config with `api_key` absent
and the adapter decides (a local Ollama doesn't need it; Anthropic will
give an actionable error on the first request, not on resolve). The
`enu --default-config` onramp leaves an **active** template of this file —
provider `anthropic` with `api_key_env = "ANTHROPIC_API_KEY"` and the
`claude-opus-4-8` model (alias `opus`) — written only if it doesn't exist,
so the harness is usable with a single command
([ADR-017](../decisions/adr/README.md), [G35](../findings/README.md)).

---

## 2. The canonical model

The agent always speaks this representation; the adapter translates
to/from the provider's dialect. It is deliberately a small superset of
what Anthropic/OpenAI/Gemini offer today.

### 2.1 Request

```
Request = {
  model:       string,            -- id exactly as the provider expects it
  system?:     string,
  messages:    Message[],
  tools?:      ToolDef[],         -- { name, description, schema (JSON Schema, table) }
  max_tokens?: integer,
  temperature?: number,
  thinking?:   { mode?: "off"|"adaptive"|"budget", budget?: integer },
}

Message = { role: "user"|"assistant", content: Block[] }
```

**Extended reasoning (`thinking`)** ([ADR-016](../decisions/adr/adr-016-modelo-canonico-de-thinking.md), closes [G34](../findings/g34-el-modelo-canonico-de-thinking.md)): `mode` requests the reasoning *mode* —`"adaptive"` (the model decides the effort, what modern models expect), `"budget"` with `budget = N` (a budget of N tokens, *legacy* extended thinking), `"off"`—; `thinking` absent = no reasoning. For **compatibility**, `{ budget = N }` without `mode` is equivalent to `mode = "budget"`. Which form each model understands is a **registry datum**: each model entry in `providers.toml` declares `thinking = "adaptive" | "budget" | "none"` (default `"budget"`), which travels in the `ModelInfo` (§3) and the adapter reads it to **translate per-model** (e.g. `mode="budget"` on a model with dialect `"adaptive"` degrades to `{type="adaptive"}`, because Opus 4.6+ removed `budget_tokens`). Requesting reasoning from a model with dialect `"none"` is a **declared degradation** (§3 obligation 5): the adapter does not simulate it. This way the adapter doesn't hardcode tables of model versions (ADR-003/ADR-005).

### 2.2 Content blocks

```
Block =
  | { type = "text",        text }
  | { type = "image",       media_type, data_base64 }
  | { type = "thinking",    text }
  | { type = "tool_call",   id, name, args }            -- args: table
  | { type = "tool_result", id, content: Block[], is_error? }
```

**The `meta` rule**: any block may carry `meta?: table` — a field
**opaque and owned by the adapter**. The agent preserves it intact and
returns it in subsequent turns without looking at it. It's the escape
valve for each protocol's quirks (Anthropic's thinking signatures,
`cache_control`, internal ids...) without contaminating the canonical
model.

### 2.3 Streaming events (what the adapter emits)

```
Event =
  | { type = "text",            text }                  -- text delta
  | { type = "thinking",        text }                  -- reasoning delta
  | { type = "tool_call.begin", id, name }
  | { type = "tool_call.delta", id, args_json }         -- fragment of the args JSON
  | { type = "tool_call.end",   id }
  | { type = "usage",           input_tokens?, output_tokens?, cache_read_tokens? }
  | { type = "done",            stop_reason: "end"|"tool_calls"|"max_tokens"|"refusal",
                                message: Message }      -- the complete assembled message
```

`done` always closes the stream and includes the complete canonical
`Message` (with its `meta`), ready to append to the conversation. This
way the agent doesn't have to re-assemble deltas, and deltas are left
purely for live rendering.

---

## 3. The adapter contract

An adapter is a Lua module that returns:

```
{
  name: string,
  caps: { tools?: boolean, images?: boolean, thinking?: boolean,
          system?: boolean, usage?: boolean },
  stream: function(req: Request, provider: ProviderConfig) -> iterator<Event>,  ⏸
  count_tokens?: function(req: Request, provider: ProviderConfig) -> integer,   ⏸ optional
}
```

where `ProviderConfig = { base_url, api_key?, extra?, model: ModelInfo }`
already resolved from the TOML.

Adapter obligations:

1. **`stream` is a suspending function** that returns an iterator of
   `Event`s (typically wrapping `enu.http.stream` + `Stream:events()`).
   It runs inside the agent's task: canceling that task cancels the
   request (the runtime closes the underlying `Stream`).
2. **Errors**: throws structured errors (ADR-009) with code
   `EPROVIDER` and `detail = { status?, provider_code?, retryable: boolean }`.
   Correctly marking `retryable` (429, 5xx, network drops) is the only
   failure intelligence asked of it.
3. **No policy**: the adapter doesn't retry, doesn't back off, doesn't
   truncate context, doesn't decide anything. That's the agent loop's job
   (which does see `retryable`). An adapter is a pure translator.
4. **Faithful round-trip**: whatever arrives in `meta` from previous
   blocks must be re-injected into the wire format exactly as the
   provider requires.
5. **Declared degradation**: if `caps.tools = false` and the request
   carries tools, it throws `EINVAL` — it doesn't silently simulate.
6. **Automatic and invisible prompt caching**: the adapter applies its
   provider's practices without the canonical model or the user
   indicating anything. OpenAI/Gemini cache prefixes on their own
   (nothing to do); on Anthropic the adapter mechanically places the
   `cache_control` breakpoints (tools + system + last messages). Exotic
   cases (e.g. Gemini's explicit cache for contexts reused across
   sessions) have their escape valve in `meta`/`extra`. *(✅ Implemented for
   `anthropic`: places breakpoints on the last tool, the system prompt and the
   last two messages, without overwriting any `cache_control` that arrives in
   `meta` — [pospuesto.md](../postponed/pospuesto.md) **P31**.)*

Illustrative skeleton (not normative):

```lua
-- adapters/openai_compat.lua
return {
  name = "openai-compat",
  caps = { tools = true, images = true, system = true, usage = true },
  stream = function(req, provider)
    local body = to_wire(req)                       -- canonical → dialect
    local s = enu.http.stream{
      url = provider.base_url .. "/chat/completions",
      method = "POST",
      headers = auth_headers(provider),
      body = enu.json.encode(body),
    }
    if s.status >= 400 then
      error({ code = "EPROVIDER", message = read_error(s),
              detail = { status = s.status, retryable = s.status == 429 or s.status >= 500 } })
    end
    return events_from(s)                           -- dialect's SSE → Event[]
  end,
}
```

*(Redirects (G54): the default in [api.md](api.md) §8 already strips the
caller's headers on cross-host hops, so a `302` from the provider towards a
third party doesn't drag `x-api-key`/`x-goog-api-key` along; an adapter that
only talks to its `base_url` needs to do nothing. If your adapter downloads
URLs it doesn't control — attachments or images the model references — set
`max_redirects = 0` (or a short limit) and validate every hop: the tools
guidance in [guia-plugins.md](guia-plugins.md) §5 has the why.)*

---

## 4. Registration and discovery

- The official adapters (`anthropic`, `openai-compat`, `gemini`) ship
  embedded as part of the providers extension. *(✅ All three are
  embedded: [pospuesto.md](../postponed/pospuesto.md) **P30** resolved. `openai-compat` serves
  the whole Chat Completions ecosystem —OpenAI, Together, Groq, OpenRouter, vLLM,
  Ollama `/v1`—; `gemini` serves the Generative Language API.)*
- A plugin contributes its own by registering it:
  `providers.register_adapter("corp-gateway", adapter)` — or by naming
  convention resolvable with `require` from the TOML
  (`"my-plugin/corp-gateway"`).
- Consumption API for the agent (and any extension):
  `providers.resolve("anthropic/opus") -> { adapter, config }` and
  `providers.list() -> ModelInfo[]` (feeds the UI's model selector).
- `providers.approx_tokens(s) -> integer`: heuristic token estimate
  (model-agnostic, ~4 bytes/token), in pure Lua. It used to live in the
  core as `enu.text.approx_tokens` and left it (G23): "token" is this
  extension's vocabulary, and a division doesn't deserve a primitive. For
  accuracy, the adapter's `count_tokens?` (§3).

**Subscriptions / OAuth (G13).** The v1 path is the one that needs no
local server: **device flow or manually pasted code** (`enu.http.request`
in polling + opening the browser with `enu.proc` — the `gh` or `gcloud`
pattern). Refresh tokens: in `data_dir()/plugins/<name>/`, `0600`
permissions, in the clear (consistent with [P7](../postponed/pospuesto.md): at-rest
encryption is the filesystem's job). The localhost-callback flow would
require an HTTP listener the core doesn't have: postponed
([P19](../postponed/pospuesto.md)).

---

## 5. v1 scope: closed decisions

1. **Prompt caching**: entirely automatic in the adapter (obligation 6
   of §3); the canonical model carries no cache marks. The user only
   notices the lower bill.
2. **Embeddings and non-chat endpoints**: out of the v1 contract. If a
   future extension (memory, semantic search) needs them, a separate
   mini-contract will be defined: this grows by addition, it doesn't get
   twisted.
3. **Images/output files from the model**: out of `Event`'s vocabulary
   in v1 (this is a coding harness; showing images in a terminal is its
   own melon). The vocabulary grows by addition when it's time.
4. **Token counting and compaction**: compaction is a feature of the
   official agent extension (customizable policy via hooks), never of
   the core (ADR-003: the core doesn't know what an LLM is). Source of
   truth for context fill: the provider's own `usage` events (exact and
   free on every turn). For prior estimation:
   `providers.approx_tokens()` (this extension's heuristic, §4 — G23) or
   the adapter's optional `count_tokens?` for whoever needs accuracy.
