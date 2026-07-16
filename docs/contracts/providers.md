---
title: "Providers de LLM: registro TOML y contrato del adaptador"
description: "Contrato de la extensión oficial de providers: registro TOML + adaptadores Lua."
type: "contrato"
layer: "contracts"
web: "wiki"
status: "vigente"
---
# Providers de LLM: registro TOML y contrato del adaptador

Estado: **borrador para discusión**. Este documento define el contrato de la
**extensión oficial de providers** — no es API sagrada del core
([api.md](api.md)); se versiona aparte y puede evolucionar más rápido.
Materializa la ADR-005: *TOML declara los datos, Lua implementa el protocolo*.

Dos audiencias:

1. **Usuario que añade un modelo** (el caso `models.json` de pi): edita
   `providers.toml`. Cero código.
2. **Autor de un adaptador** (protocolo nuevo o dialecto raro): escribe un
   módulo Lua que cumple el contrato de §3.

---

## 1. El registro: `providers.toml`

Vive en `enu.config.dir()`. Declara *datos*, nunca lógica.

```toml
# Provider con adaptador oficial: solo datos.
[providers.anthropic]
adapter     = "anthropic"                  # qué adaptador habla su protocolo
base_url    = "https://api.anthropic.com"
api_key_env = "ANTHROPIC_API_KEY"          # nunca la clave en el fichero

[[providers.anthropic.models]]
id         = "claude-opus-4-8"
context    = 200000
max_output = 32000
cost       = { input = 5.0, output = 25.0 }   # USD por Mtok (informativo)
aliases    = ["opus"]
thinking   = "adaptive"                        # dialecto de razonamiento (ADR-016):
                                               # "adaptive" (Opus 4.6+), "budget"
                                               # (extended thinking legacy) o "none".
                                               # Default "budget" si se omite.

# El caso models.json: endpoint compatible-OpenAI, p. ej. Ollama local.
[providers.local]
adapter  = "openai-compat"
base_url = "http://localhost:11434/v1"

[[providers.local.models]]
id      = "qwen3:32b"
context = 32768

# Provider con protocolo exótico: el adaptador es de un plugin de terceros.
[providers.corp]
adapter  = "mi-plugin/corp-gateway"        # resoluble por require()
base_url = "https://llm.internal.corp"
extra    = { tenant = "equipo-7" }         # tabla opaca, pasa al adaptador
```

Resolución de un modelo: `"proveedor/id-o-alias"` (`"anthropic/opus"`,
`"local/qwen3:32b"`). La extensión de providers resuelve el TOML, lee la API
key del entorno y entrega al adaptador una `ProviderConfig` ya cocinada.
`resolve` **no falla** si la variable de `api_key_env` no está en el entorno:
entrega la config con `api_key` ausente y el adaptador decide (un Ollama local no
la necesita; Anthropic dará un error accionable en la primera petición, no al
resolver). El onramp `enu --default-config` deja una plantilla **activa** de este
fichero —provider `anthropic` con `api_key_env = "ANTHROPIC_API_KEY"` y el modelo
`claude-opus-4-8` (alias `opus`)— escrita solo si no existe, para que el harness
quede usable con un comando ([ADR-017](adr.md), [G35](problemas.md)).

---

## 2. El modelo canónico

El agente habla siempre esta representación; el adaptador traduce de/hacia el
dialecto del provider. Es deliberadamente un superconjunto pequeño de lo que
hoy ofrecen Anthropic/OpenAI/Gemini.

### 2.1 Request

```
Request = {
  model:       string,            -- id tal y como lo espera el provider
  system?:     string,
  messages:    Message[],
  tools?:      ToolDef[],         -- { name, description, schema (JSON Schema, tabla) }
  max_tokens?: integer,
  temperature?: number,
  thinking?:   { mode?: "off"|"adaptive"|"budget", budget?: integer },
}

Message = { role: "user"|"assistant", content: Block[] }
```

**Razonamiento extendido (`thinking`)** ([ADR-016](adr.md#adr-016--modelo-canónico-de-thinking-con-mode-y-traducción-por-modelo-en-el-adaptador), cierra [G34](problemas.md#g34)): `mode` pide el *modo* de razonamiento —`"adaptive"` (el modelo decide el esfuerzo, lo que esperan los modelos modernos), `"budget"` con `budget = N` (presupuesto de N tokens, extended thinking *legacy*), `"off"`—; `thinking` ausente = sin razonamiento. Por **compatibilidad**, `{ budget = N }` sin `mode` equivale a `mode = "budget"`. Qué forma entiende cada modelo es un **dato del registro**: cada entrada de modelo en `providers.toml` declara `thinking = "adaptive" | "budget" | "none"` (default `"budget"`), que viaja en el `ModelInfo` (§3) y el adaptador lee para **traducir por-modelo** (p. ej. `mode="budget"` sobre un modelo de dialecto `"adaptive"` degrada a `{type="adaptive"}`, porque Opus 4.6+ retiró `budget_tokens`). Pedir razonamiento a un modelo de dialecto `"none"` es una **degradación declarada** (§3 obligación 5): el adaptador no lo simula. Así el adaptador no hardcodea tablas de versiones de modelos (ADR-003/ADR-005).

### 2.2 Bloques de contenido

```
Block =
  | { type = "text",        text }
  | { type = "image",       media_type, data_base64 }
  | { type = "thinking",    text }
  | { type = "tool_call",   id, name, args }            -- args: tabla
  | { type = "tool_result", id, content: Block[], is_error? }
```

**Regla `meta`**: cualquier bloque puede llevar `meta?: tabla` — un campo
**opaco propiedad del adaptador**. El agente lo preserva intacto y lo devuelve
en turnos siguientes sin mirarlo. Es la válvula para los caprichos de cada
protocolo (firmas de thinking de Anthropic, `cache_control`, ids internos...)
sin contaminar el modelo canónico.

### 2.3 Eventos de streaming (lo que el adaptador emite)

```
Event =
  | { type = "text",            text }                  -- delta de texto
  | { type = "thinking",        text }                  -- delta de razonamiento
  | { type = "tool_call.begin", id, name }
  | { type = "tool_call.delta", id, args_json }         -- fragmento del JSON de args
  | { type = "tool_call.end",   id }
  | { type = "usage",           input_tokens?, output_tokens?, cache_read_tokens? }
  | { type = "done",            stop_reason: "end"|"tool_calls"|"max_tokens"|"refusal",
                                message: Message }      -- el mensaje completo ensamblado
```

`done` cierra siempre el stream e incluye el `Message` canónico completo (con
sus `meta`), listo para anexar a la conversación. Así el agente no tiene que
re-ensamblar deltas, y los deltas quedan solo para pintar en vivo.

---

## 3. El contrato del adaptador

Un adaptador es un módulo Lua que devuelve:

```
{
  name: string,
  caps: { tools?: boolean, images?: boolean, thinking?: boolean,
          system?: boolean, usage?: boolean },
  stream: function(req: Request, provider: ProviderConfig) -> iterator<Event>,  ⏸
  count_tokens?: function(req: Request, provider: ProviderConfig) -> integer,   ⏸ opcional
}
```

donde `ProviderConfig = { base_url, api_key?, extra?, model: ModelInfo }` ya
resuelta desde el TOML.

Obligaciones del adaptador:

1. **`stream` es una función suspendiente** que devuelve un iterador de
   `Event`s (típicamente envolviendo `enu.http.stream` + `Stream:events()`).
   Se ejecuta dentro de la task del agente: la cancelación de esa task
   cancela la petición (el runtime cierra el `Stream` subyacente).
2. **Errores**: lanza errores estructurados (ADR-009) con código
   `EPROVIDER` y `detail = { status?, provider_code?, retryable: boolean }`.
   Marcar `retryable` correctamente (429, 5xx, cortes de red) es la única
   inteligencia de fallos que se le pide.
3. **Sin política**: el adaptador no reintenta, no hace backoff, no trunca
   contexto, no decide nada. Eso es del loop del agente (que sí ve
   `retryable`). Un adaptador es un traductor puro.
4. **Round-trip fiel**: lo que llegue en `meta` de bloques previos debe
   reinyectarse en el wire format como el provider lo exige.
5. **Degradación declarada**: si `caps.tools = false` y el request trae
   tools, lanza `EINVAL` — no simula silenciosamente.
6. **Prompt caching automático e invisible**: el adaptador aplica las
   prácticas de su proveedor sin que el modelo canónico ni el usuario
   indiquen nada. OpenAI/Gemini cachean prefijos solos (nada que hacer);
   en Anthropic el adaptador coloca los breakpoints `cache_control`
   mecánicamente (tools + system + últimos mensajes). Casos exóticos
   (p. ej. la caché explícita de Gemini para contextos reutilizados entre
   sesiones) tienen su válvula en `meta`/`extra`. *(✅ Implementado para
   `anthropic`: coloca los breakpoints en la última tool, el system y los dos
   últimos mensajes, sin pisar el `cache_control` que venga en `meta` —
   [pospuesto.md](pospuesto.md) **P31**.)*

Esqueleto ilustrativo (no normativo):

```lua
-- adapters/openai_compat.lua
return {
  name = "openai-compat",
  caps = { tools = true, images = true, system = true, usage = true },
  stream = function(req, provider)
    local body = to_wire(req)                       -- canónico → dialecto
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
    return events_from(s)                           -- SSE del dialecto → Event[]
  end,
}
```

*(Redirects (G54): el default de [api.md](api.md) §8 ya recorta las cabeceras
del llamante en saltos cross-host, así que un `302` del provider hacia un
tercero no arrastra `x-api-key`/`x-goog-api-key`; un adaptador que solo habla
con su `base_url` no necesita hacer nada. Si tu adaptador descarga URLs que
no controla —adjuntos o imágenes que el modelo referencia—, pon
`max_redirects = 0` (o un límite corto) y valida cada salto: la guía de tools
de [guia-plugins.md](guia-plugins.md) §5 tiene el porqué.)*

---

## 4. Registro y descubrimiento

- Los adaptadores oficiales (`anthropic`, `openai-compat`, `gemini`) van
  embebidos como parte de la extensión de providers. *(✅ Los tres están
  embebidos: [pospuesto.md](pospuesto.md) **P30** resuelto. `openai-compat` sirve
  a todo el ecosistema Chat Completions —OpenAI, Together, Groq, OpenRouter, vLLM,
  Ollama `/v1`—; `gemini` a la Generative Language API.)*
- Un plugin aporta el suyo registrándolo:
  `providers.register_adapter("corp-gateway", adapter)` — o por convención de
  nombre resoluble con `require` desde el TOML (`"mi-plugin/corp-gateway"`).
- API de consumo para el agente (y cualquier extensión):
  `providers.resolve("anthropic/opus") -> { adapter, config }` y
  `providers.list() -> ModelInfo[]` (alimenta el selector de modelos de la UI).
- `providers.approx_tokens(s) -> integer`: estimación heurística de tokens
  (agnóstica de modelo, ~4 bytes/token), en Lua puro. Vivía en el core como
  `enu.text.approx_tokens` y salió de él (G23): "token" es vocabulario de
  esta extensión, y una división no merece primitiva. Para exactitud, el
  `count_tokens?` del adaptador (§3).
- `providers.secret_env_vars() -> string[]` (G55): los **nombres** —nunca los
  valores— de las variables de entorno que portan credenciales según el
  registro: las `api_key_env` de todos los providers declarados en
  `providers.toml`, deduplicadas. Existe porque solo esta extensión sabe qué
  variables del entorno son secretos de LLM — "provider" y "API key" son
  vocabulario de producto, invisible para el core (ADR-003) —, y otras
  extensiones necesitan esa lista para no regalarlos: la tool `bash` de la
  extensión `agent` (y el lanzamiento de servidores MCP) la usa para
  **recortar por defecto** esas variables del entorno de sus subprocesos
  ([agente.md](agente.md) §3, origen SEC-04). Es una foto del registro
  vigente, no una promesa de secreto absoluto: una credencial que el usuario
  exporte al margen del TOML no se conoce aquí — el contrato es honesto sobre
  su alcance. *(⏳ Pendiente de construcción: la extensión `0.1.0` aún no la
  expone; se implementará junto al recorte de [agente.md](agente.md) §3.)*

**Suscripciones / OAuth (G13).** El camino v1 es el que no necesita
servidor local: **device flow o pegado manual de código** (`enu.http.request`
en polling + abrir el navegador con `enu.proc` — el patrón de `gh` o
`gcloud`). Tokens de refresco: en `data_dir()/plugins/<nombre>/`, permisos
`0600`, en claro (coherente con [P7](pospuesto.md): el cifrado en reposo es
del filesystem). El flujo con callback localhost requeriría un listener
HTTP que el core no tiene: pospuesto ([P19](pospuesto.md)).

---

## 5. Alcance v1: decisiones cerradas

1. **Prompt caching**: enteramente automático en el adaptador (obligación 6
   de §3); el modelo canónico no tiene marcas de caché. El usuario solo nota
   la factura más baja.
2. **Embeddings y endpoints no-chat**: fuera del contrato v1. Si una futura
   extensión (memoria, búsqueda semántica) los necesita, se definirá un
   mini-contrato aparte: este crece por adición, no se retuerce.
3. **Imágenes/archivos de salida del modelo**: fuera del vocabulario de
   `Event` en v1 (es un harness de código; mostrar imágenes en terminal es
   un melón propio). El vocabulario crece por adición cuando toque.
4. **Token counting y compactación**: la compactación es feature de la
   extensión oficial del agente (política personalizable vía hooks), nunca
   del core (ADR-003: el core no sabe lo que es un LLM). Fuente de verdad
   del llenado de contexto: los eventos `usage` del propio proveedor
   (exactos y gratis en cada turno). Para estimación previa:
   `providers.approx_tokens()` (heurística de esta extensión, §4 — G23) o
   el `count_tokens?` opcional del adaptador para quien necesite exactitud.
