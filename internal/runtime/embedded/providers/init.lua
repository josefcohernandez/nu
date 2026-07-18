-- Extensión oficial `providers` (S36). Implementa el contrato de
-- [providers.md](../../../../docs/providers.md): el registro TOML
-- (`providers.toml`), el contrato del adaptador y `providers.approx_tokens`.
--
-- ADR-005: *TOML declara los datos, Lua implementa el protocolo*. ADR-003: el
-- core NO sabe lo que es un provider; toda esta lógica es Lua puro sobre la API
-- pública congelada ([api.md](../../../../docs/api.md)), sin privilegio de
-- kernel. El namespace de eventos de esta extensión es `providers:` (el del
-- propio plugin, por convención §4; el core solo reserva `core:`/`ui:`).
--
-- El `init.lua` solo CABLEA: carga el módulo público y registra los adaptadores
-- oficiales embebidos. La API de consumo (`resolve`, `list`, `register_adapter`,
-- `approx_tokens`) la expone el módulo `providers`, requerible por el agente y
-- cualquier extensión con `require("providers")`.
--
-- Adaptadores oficiales (providers.md §4): `anthropic`, `openai-compat`,
-- `gemini` van embebidos. En S36 solo existe el adaptador STUB —suficiente para
-- validar el contrato contra una petición simulada (criterio de hecho de S36)—;
-- el adaptador `anthropic` REAL (SSE de Anthropic) llega en S37 y se registrará
-- aquí exactamente igual: `providers.register_adapter("anthropic", adapter)`.

local providers = require("providers")

-- Adaptador STUB: cumple el contrato de §3 sin tocar la red. Se registra como
-- adaptador oficial para que un `providers.toml` que declare `adapter = "stub"`
-- resuelva contra él. Su única misión es probar el contrato (S36); S37 lo
-- sustituye por el adaptador `anthropic` de verdad.
providers.register_adapter("stub", require("providers.adapter_stub"))

-- Adaptador `anthropic` REAL (S37): el primer dialecto sobre la red. Habla la
-- Messages API de Anthropic vía `enu.http.stream` y traduce su SSE
-- (`message_start`, `content_block_*`, `message_delta`, `message_stop`...) al
-- stream de Eventos canónico de providers.md §2.3. Reusa el contrato §3 y
-- `approx_tokens`/EPROVIDER de S36; se registra exactamente igual que el stub.
providers.register_adapter("anthropic", require("providers.adapter_anthropic"))

-- Adaptadores `openai-compat` y `gemini` (P30): los otros dos dialectos oficiales
-- embebidos que providers.md §4 promete. `openai-compat` habla la Chat Completions
-- API (OpenAI y todo el ecosistema compatible: Together, Groq, OpenRouter, vLLM,
-- Ollama `/v1`...); `gemini` habla la Generative Language API de Google. Mismo
-- contrato §3, misma forma de registro que el stub y `anthropic`. Con esto un
-- `providers.toml` con `adapter = "openai-compat"` o `adapter = "gemini"` resuelve
-- sin escribir un adaptador propio (cierre del disparador de pospuesto.md P30).
providers.register_adapter("openai-compat", require("providers.adapter_openai_compat"))
providers.register_adapter("gemini", require("providers.adapter_gemini"))
