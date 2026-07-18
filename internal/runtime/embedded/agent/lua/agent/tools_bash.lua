-- Tool `bash` de la extensión `agent` (agente.md §3 dogfooding). Ejecuta un
-- comando de shell y devuelve su salida. Como `read_file`/`write_file`
-- (tools_fs.lua), se registra con la MISMA `agent.tool` que usaría cualquier
-- extensión de terceros — sin atajo privilegiado (ADR-003).
--
-- MUTA el mundo (procesos, ficheros, red...) → sin `permissions.default`:
-- hereda el default "ask" del registro (deny en headless sin respuesta,
-- agente.md §5). Es la tool sobre la que corre TODA la maquinaria de
-- emparejamiento por subcomando de G53/ADR-023 (init.lua: `decompose_bash`,
-- `match_bash`) — esta tool solo aporta `name = "bash"` y `args.command`, que
-- es justo lo que esa maquinaria espera (`arg_text` lee `args.command`).
--
-- Higiene del entorno del hijo (G55, agente.md §3, SEC-04): por defecto el
-- subproceso NO ve las variables de `providers.secret_env_vars()` — la API key
-- del provider activo no debe estar al alcance de un `env`/`curl`/`postinstall`
-- que el propio modelo propuso. `agent._bash_subprocess_argv` (init.lua) hace el
-- recorte anteponiendo `env -u VAR ... --` al comando real; el opt-in nominal
-- vive en `[tools.bash] inherit_secrets` del `agent.toml` del USUARIO (§10/§11).
--
-- Sin shell implícita en la API (api.md §6: "argv es un array; quien quiera
-- shell la invoca explícitamente") — aquí SÍ queremos shell (es la tool
-- `bash`), así que el argv real es `/bin/sh -c <command>`; la higiene de G55 se
-- antepone a ESE argv, no lo sustituye.

local agent = require("agent")

agent.tool{
  name = "bash",
  description = "Ejecuta un comando de shell en el repositorio y devuelve su salida.",
  schema = {
    type = "object",
    properties = {
      command = { type = "string", description = "comando a ejecutar (vía /bin/sh -c)" },
    },
    required = { "command" },
  },
  -- Sin `permissions.default`: "ask" (deny en headless sin allow, agente.md §5).
  handler = function(args, ctx)
    if type(args) ~= "table" or type(args.command) ~= "string" then
      error({ code = "EINVAL", message = "bash requiere `command` (string)" })
    end
    local argv = agent._bash_subprocess_argv({ "/bin/sh", "-c", args.command })
    local result = enu.proc.run(argv, { cwd = ctx.cwd })

    -- Salida legible para el modelo: stdout, seguido de stderr y el código de
    -- salida si el comando falló. Un exit code != 0 NO es un error de la tool
    -- (muchos comandos lo usan como resultado con sentido, p. ej. `grep`) —
    -- se reporta como texto, no como `is_error` (agente.md §3: eso es para
    -- fallos del HANDLER, no del comando que corrió correctamente).
    local out = result.stdout or ""
    if result.stderr ~= nil and result.stderr ~= "" then
      if out ~= "" then
        out = out .. "\n"
      end
      out = out .. "[stderr]\n" .. result.stderr
    end
    if result.code ~= 0 then
      out = out .. string.format("\n[exit code %d]", result.code)
    end
    return out
  end,
}
