-- Módulo público de la extensión `providers` (S36).
--
-- Implementa el contrato de [providers.md](../../../../../docs/providers.md):
--
--   1. **Lector del registro TOML** (§1): carga `providers.toml` de
--      `nu.config.dir()`, lo decodifica con `nu.toml.decode` y construye un
--      registro de providers y modelos resoluble por `"proveedor/id-o-alias"`.
--   2. **Contrato del adaptador** (§3): `register_adapter` valida la *forma* que
--      un adaptador debe cumplir; `resolve` empareja un modelo con su adaptador
--      y una `ProviderConfig` ya cocinada (base_url + api_key del entorno + extra
--      + ModelInfo).
--   3. **`approx_tokens`** (§4, G23): heurística ~4 bytes/token, Lua puro. Vivía
--      en el core como `nu.text.approx_tokens` y salió de él (G23): "token" es
--      vocabulario de ESTA extensión.
--
-- Todo sobre la API pública (api.md): `nu.toml`, `nu.fs`, `nu.config.dir`,
-- `require`, `error` estructurado (ADR-009). NINGÚN privilegio de kernel.
--
-- Código de error de la extensión: `EPROVIDER` (providers.md §3; las extensiones
-- acuñan los suyos con la misma forma que los del core, CLAUDE.md / api.md §1.4).

local M = {}

-- Registro vivo (en memoria) de adaptadores por nombre. Se llena en el arranque
-- (`init.lua` registra los oficiales) y por plugins de terceros que aporten el
-- suyo con `register_adapter`. El nombre del adaptador es su identidad.
local adapters = {}

-- Caché del registro TOML decodificado. `nil` hasta la primera carga (perezosa:
-- no leemos el disco hasta que alguien resuelve o lista un modelo). `reload`
-- la invalida.
local registry = nil

-- ---------------------------------------------------------------------------
-- Errores estructurados de la extensión (EPROVIDER, providers.md §3 / ADR-009).
-- ---------------------------------------------------------------------------

local function eprovider(message, detail)
  error({ code = "EPROVIDER", message = message, detail = detail })
end

local function einval(message)
  error({ code = "EINVAL", message = message })
end

-- ---------------------------------------------------------------------------
-- approx_tokens (providers.md §4, G23): heurística agnóstica de modelo.
-- ---------------------------------------------------------------------------

-- approx_tokens(s) -> integer. Estimación heurística de tokens ~4 bytes/token.
-- No es exacta (para eso está el `count_tokens?` del adaptador, §3): es la
-- regla de bolsillo para estimar el llenado de contexto ANTES de un turno.
--
-- Cuenta BYTES (no caracteres): es lo que mejor aproxima la tokenización BPE
-- sobre texto mixto, y es lo que hacía el core. `#s` en Lua es la longitud en
-- bytes. Redondeo hacia arriba (`ceil`) para no infraestimar: una cadena no
-- vacía nunca es 0 tokens. La cadena vacía es 0.
function M.approx_tokens(s)
  if type(s) ~= "string" then
    einval("providers.approx_tokens espera una cadena, recibió " .. type(s))
  end
  local bytes = #s
  if bytes == 0 then
    return 0
  end
  -- ceil(bytes / 4) sin depender de math.ceil sobre flotantes: aritmética entera.
  return math.floor((bytes + 3) / 4)
end

-- ---------------------------------------------------------------------------
-- Contrato del adaptador (providers.md §3): validación de la FORMA.
-- ---------------------------------------------------------------------------

-- assert_adapter_shape verifica que `a` cumple la forma mínima de un adaptador
-- (providers.md §3): una tabla con `name: string`, `caps: tabla` y
-- `stream: function` (suspendiente). `count_tokens` es opcional pero, si está,
-- debe ser función. No validamos la SEMÁNTICA (eso lo prueba el stub contra una
-- petición simulada), solo la forma, para fallar pronto y accionable al registrar.
local function assert_adapter_shape(name, a)
  if type(a) ~= "table" then
    einval(string.format("el adaptador %q debe ser una tabla (contrato providers.md §3), es %s", name, type(a)))
  end
  if type(a.name) ~= "string" or a.name == "" then
    einval(string.format("el adaptador %q debe tener un campo `name` (string no vacío)", name))
  end
  if type(a.caps) ~= "table" then
    einval(string.format("el adaptador %q debe tener un campo `caps` (tabla de capacidades)", name))
  end
  if type(a.stream) ~= "function" then
    einval(string.format("el adaptador %q debe tener un campo `stream` (función suspendiente, providers.md §3)", name))
  end
  if a.count_tokens ~= nil and type(a.count_tokens) ~= "function" then
    einval(string.format("el adaptador %q tiene `count_tokens` pero no es función (es opcional; si está, función)", name))
  end
end

-- register_adapter(name, adapter) registra un adaptador bajo `name` (providers.md
-- §4). Lo usan tanto los adaptadores oficiales (desde el init.lua de la extensión)
-- como los plugins de terceros. Valida la forma del adaptador al registrar
-- (fallo pronto). Un re-registro del mismo nombre lo SUSTITUYE (un plugin puede
-- pisar un adaptador oficial a propósito); no es error.
function M.register_adapter(name, adapter)
  if type(name) ~= "string" or name == "" then
    einval("register_adapter: el nombre del adaptador debe ser una cadena no vacía")
  end
  assert_adapter_shape(name, adapter)
  adapters[name] = adapter
end

-- get_adapter(name) -> adapter|nil. Resuelve un adaptador por nombre. Primero el
-- registro vivo (oficiales + register_adapter); si no está y el nombre tiene
-- pinta de módulo resoluble (`"mi-plugin/corp-gateway"`, providers.md §1/§4),
-- intenta `require` y valida su forma. Devuelve nil si no se encuentra (el
-- llamante decide el error con contexto).
local function get_adapter(name)
  local a = adapters[name]
  if a ~= nil then
    return a
  end
  -- Resolución por convención de nombre con require (providers.md §4): el TOML
  -- pudo declarar `adapter = "mi-plugin/corp-gateway"`, un módulo Lua de un
  -- plugin. `require` se resuelve contra las rutas `lua/` de los plugins (§14).
  local ok, mod = pcall(require, name)
  if ok and mod ~= nil then
    assert_adapter_shape(name, mod)
    adapters[name] = mod -- cachea para no re-requerir
    return mod
  end
  return nil
end

-- ---------------------------------------------------------------------------
-- Lector del registro TOML (providers.md §1).
-- ---------------------------------------------------------------------------

-- registry_path() -> string. Ruta de `providers.toml` (providers.md §1: "vive en
-- nu.config.dir()").
local function registry_path()
  return nu.config.dir() .. "/providers.toml"
end

-- build_index construye, a partir del TOML decodificado, un índice plano de
-- modelos resolubles por `"proveedor/id"` y `"proveedor/alias"` (providers.md
-- §1). Cada entrada lleva el provider crudo (base_url, adapter, api_key_env,
-- extra...) y el ModelInfo, listos para cocinar la ProviderConfig en `resolve`.
--
-- Valida lo mínimo accionable: un provider sin `adapter`, o un modelo sin `id`,
-- es un registro inválido (EPROVIDER) que nombra el provider —el usuario edita
-- datos, no código, y el error debe decirle qué línea arreglar (providers.md §1)—.
local function build_index(decoded)
  local index = {}            -- "proveedor/clave" -> { provider_name, provider, model }
  local models = {}           -- lista de ModelInfo enriquecidos (para list())
  local provs = (decoded and decoded.providers) or {}

  for pname, prov in pairs(provs) do
    if type(prov) ~= "table" then
      eprovider(string.format("provider %q en providers.toml no es una tabla", pname))
    end
    if type(prov.adapter) ~= "string" or prov.adapter == "" then
      eprovider(string.format("el provider %q en providers.toml no declara `adapter` (providers.md §1)", pname))
    end
    local mlist = prov.models or {}
    for _, model in ipairs(mlist) do
      if type(model.id) ~= "string" or model.id == "" then
        eprovider(string.format("un modelo del provider %q en providers.toml no tiene `id` (providers.md §1)", pname))
      end
      -- ModelInfo enriquecido: el id canónico + de qué provider viene. list()
      -- lo entrega tal cual al selector de modelos de la UI (providers.md §4).
      local info = {
        provider   = pname,
        id         = model.id,
        ref        = pname .. "/" .. model.id,
        context    = model.context,
        max_output = model.max_output,
        cost       = model.cost,
        aliases    = model.aliases,
        -- El dialecto de razonamiento (ADR-016) forma parte del ModelInfo (§3):
        -- las dos vías (list y resolve) entregan la misma forma.
        thinking   = model.thinking,
      }
      models[#models + 1] = info

      local entry = { provider_name = pname, provider = prov, model = model }
      -- Clave canónica "proveedor/id".
      index[pname .. "/" .. model.id] = entry
      -- Y cada alias "proveedor/alias" (providers.md §1: "anthropic/opus").
      for _, alias in ipairs(model.aliases or {}) do
        index[pname .. "/" .. alias] = entry
      end
    end
  end

  return { index = index, models = models }
end

-- load_registry carga y cachea el registro. Perezoso (no toca el disco hasta la
-- primera resolución). Un `providers.toml` AUSENTE es válido y da un registro
-- vacío (un nu recién arrancado sin modelos configurados no es un error): se
-- distingue ENOENT de un fallo de IO real con el código del error de `nu.fs`.
-- Un TOML mal formado SÍ es error accionable (EPROVIDER) que nombra el fichero.
local function load_registry()
  if registry ~= nil then
    return registry
  end
  local path = registry_path()

  local ok, content = pcall(nu.fs.read, path)
  if not ok then
    -- `nu.fs.read` lanza estructurado (api.md §5). ENOENT: fichero ausente =>
    -- registro vacío. Cualquier otro error (EACCES, EIO) se propaga.
    if type(content) == "table" and content.code == "ENOENT" then
      registry = build_index(nil)
      return registry
    end
    error(content)
  end

  local okd, decoded = pcall(nu.toml.decode, content)
  if not okd then
    eprovider(string.format("providers.toml mal formado (%s): %s", path,
      (type(decoded) == "table" and decoded.message) or tostring(decoded)))
  end

  registry = build_index(decoded)
  return registry
end

-- reload() invalida la caché del registro (tras editar `providers.toml`). El
-- siguiente `resolve`/`list` lo relee del disco. Los adaptadores registrados NO
-- se tocan (son código, no datos).
function M.reload()
  registry = nil
end

-- ---------------------------------------------------------------------------
-- API de consumo (providers.md §4): resolve, list.
-- ---------------------------------------------------------------------------

-- resolve(ref) -> { adapter, config } (providers.md §4). `ref` es
-- `"proveedor/id-o-alias"`. Cocina la `ProviderConfig` (§3): base_url, api_key
-- leída del entorno (`api_key_env`, nunca del fichero), extra opaco y el
-- ModelInfo. Errores accionables (EPROVIDER) cuando el modelo o su adaptador no
-- existen, nombrando la `ref`.
function M.resolve(ref)
  if type(ref) ~= "string" or ref == "" then
    einval("providers.resolve espera una referencia \"proveedor/id-o-alias\"")
  end
  local reg = load_registry()
  local entry = reg.index[ref]
  if entry == nil then
    eprovider(string.format("modelo %q no encontrado en providers.toml (¿proveedor/alias correcto?)", ref),
      { ref = ref })
  end

  local prov = entry.provider
  local adapter = get_adapter(prov.adapter)
  if adapter == nil then
    eprovider(string.format("el provider %q usa el adaptador %q, que no está registrado (¿plugin del adaptador activo?)",
      entry.provider_name, prov.adapter), { ref = ref, adapter = prov.adapter })
  end

  -- api_key del ENTORNO (providers.md §1: "nunca la clave en el fichero"). Si el
  -- provider no declara `api_key_env`, la config va sin clave (p. ej. un Ollama
  -- local). No es error aquí: el adaptador decide si la necesita.
  local api_key = nil
  if type(prov.api_key_env) == "string" and prov.api_key_env ~= "" then
    api_key = nu.sys.env(prov.api_key_env)
  end

  -- ModelInfo cocinado para el adaptador: el id tal y como lo espera el provider
  -- (providers.md §2.1) más los metadatos del modelo. `thinking` es el DIALECTO
  -- de razonamiento del modelo (ADR-016): "adaptive"|"budget"|"none", dato del
  -- registro que el adaptador lee para traducir el `thinking` canónico por-modelo
  -- (el adaptador aplica el default "budget" si falta).
  local model_info = {
    id         = entry.model.id,
    context    = entry.model.context,
    max_output = entry.model.max_output,
    cost       = entry.model.cost,
    thinking   = entry.model.thinking,
  }

  local config = {
    base_url = prov.base_url,
    api_key  = api_key,
    extra    = prov.extra,
    model    = model_info,
  }

  return { adapter = adapter, config = config }
end

-- list() -> ModelInfo[] (providers.md §4). Alimenta el selector de modelos de la
-- UI. Devuelve todos los modelos declarados en `providers.toml`, con su `ref`
-- canónica (`"proveedor/id"`) lista para pasar a `resolve`.
function M.list()
  local reg = load_registry()
  -- Copia defensiva (el llamante no debe mutar el índice cacheado).
  local out = {}
  for i, info in ipairs(reg.models) do
    out[i] = info
  end
  return out
end

return M
