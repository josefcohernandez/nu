-- Tools básicas de fichero de la extensión `agent` (S39, agente.md §3 dogfooding).
--
-- Se registran con la MISMA `agent.tool` que usaría cualquier extensión de
-- terceros (no hay atajo privilegiado, ADR-003). Cubren el mínimo del criterio de
-- hecho de S39 / CP-10: una tool de fichero de LECTURA (que no pide permiso, ni en
-- headless: `default = "allow"`, agente.md §5 amortiguador 1) y una de ESCRITURA
-- (que muta el disco y por tanto cae en el default "ask" → DENY en headless sin
-- respuesta, agente.md §5; el permiso denegado es el que produce el error
-- accionable de CP-10).
--
-- Los handlers corren como parte de la task del turno y SUSPENDEN (enu.fs ⏸,
-- api.md §5) sin bloquear nada. Un error lanzado (p. ej. ENOENT) lo convierte el
-- loop en un tool_result is_error que el modelo ve (agente.md §3).

local agent = require("agent")

-- read_file: lee un fichero y devuelve su contenido. Solo lectura → no pide
-- permiso (agente.md §5 amortiguador 1: read/grep/glob nunca preguntan).
agent.tool{
  name = "read_file",
  description = "Lee el contenido de un fichero del repositorio.",
  schema = {
    type = "object",
    properties = { path = { type = "string", description = "ruta del fichero a leer" } },
    required = { "path" },
  },
  permissions = { default = "allow" },
  handler = function(args, ctx)
    if type(args) ~= "table" or type(args.path) ~= "string" then
      error({ code = "EINVAL", message = "read_file requiere `path` (string)" })
    end
    -- enu.fs.read lanza estructurado (ENOENT, EACCES...) que el loop traduce a
    -- tool_result is_error. Devuelve el contenido como texto.
    return enu.fs.read(args.path)
  end,
}

-- write_file: escribe (crea/sobrescribe) un fichero. MUTA el disco → default
-- "ask": en headless sin respuesta, DENY (agente.md §5). Este es el permiso que
-- CP-10 deniega para comprobar el error accionable.
agent.tool{
  name = "write_file",
  description = "Escribe (crea o sobrescribe) un fichero del repositorio.",
  schema = {
    type = "object",
    properties = {
      path    = { type = "string", description = "ruta del fichero a escribir" },
      content = { type = "string", description = "contenido a escribir" },
    },
    required = { "path", "content" },
  },
  -- Sin `permissions.default`: hereda el default "ask" del registro (deny en
  -- headless). Una sesión que la quiera permitir añade `allow = {"write_file"}`.
  handler = function(args, ctx)
    if type(args) ~= "table" or type(args.path) ~= "string" or type(args.content) ~= "string" then
      error({ code = "EINVAL", message = "write_file requiere `path` y `content` (string)" })
    end
    enu.fs.write(args.path, args.content)
    return string.format("escrito %d bytes en %s", #args.content, args.path)
  end,
}
