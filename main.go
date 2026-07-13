// Command nu es el binario del runtime: un kernel Lua mínimo donde todo lo demás
// son extensiones (filosofia.md). Este fichero es la SUPERFICIE CLI (S45, cuestión
// abierta nº5 de arquitectura.md): los flags, el comportamiento headless y los
// códigos de salida del ejecutable. NO es la API sagrada `nu.*` (eso es api.md, la
// superficie Lua): es la interfaz de línea de comandos del binario, y por eso vive
// aquí, en `package main`, no en el core. El core sigue sin saber lo que es un
// agente (ADR-003): el CLE orquesta las EXTENSIONES (`agent`, `sessions`) por la API
// pública, igual que podría hacerlo un `init.lua` de usuario; la lógica de turno y
// de reanudación ya vive en esas extensiones (S38/S39), aquí solo se invocan.
//
// MODOS (sin args y con TTY → pantalla de runtime desnudo / arranque normal, S33):
//
//	nu                       Arranque canónico (§14). Con TTY y ningún plugin activo,
//	                         pinta la PANTALLA DE RUNTIME DESNUDO (G21, S33).
//	nu --default-config      Activa el CONJUNTO OFICIAL DE PRODUCTO (las embebidas menos
//	                         el andamiaje `example`, ADR-015/G33) sin TTY. SOLO: escribe
//	                         `plugins.enabled` en `config.dir()/nu.toml` y sale (atómico,
//	                         idempotente, preserva el resto; un `nu.toml` roto NO se pisa).
//	                         Combinado con `-p`/`-e`: EFÍMERO, lo activa solo para ese
//	                         proceso (`WithEnabledPlugins`) sin tocar disco —Docker/CI
//	                         inmutable—. Es el onramp sin TTY que la pantalla no daba.
//	nu -e '<lua>'            Evalúa un chunk Lua SIN TTY (headless) e imprime sus
//	                         valores de retorno. El chunk corre en el estado
//	                         principal (no es task): puede `nu.task.spawn` pero no
//	                         usar funciones ⏸ directamente (§1.3).
//	nu -p '<prompt>'         Ejecuta un TURNO de AGENTE headless (agente.md §1: el
//	                         motor es headless, "modo scripting/CI gratis") con el
//	                         prompt dado e imprime el texto final del asistente a
//	                         stdout. Corre como TASK (vía EvalTaskString) para que el
//	                         turno (⏸) y sus tools (fs/proc/http) funcionen sin TTY.
//	nu --continue -p '<...>' Reanuda la ÚLTIMA sesión del proyecto (cwd) antes de
//	                         enviar el prompt: azúcar de `agent.session{resume}`
//	                         (G18) — el `--continue` que G18 dejó deliberadamente
//	                         fuera de los contratos por pertenecer a la superficie
//	                         CLI. La "más reciente" sale de `sessions.list(cwd)` (los
//	                         ids ordenan lexicográfico = temporal, sesiones.md §2/§7).
//	  --auto-permissions     Permisos del agente en modo "auto" (agente.md §5
//	                         amortiguador 3): el riesgo se ELIGE, no se hereda. SIN
//	                         este flag, en headless las tools sensibles (write, bash,
//	                         red) se DENIEGAN con error accionable (agente.md §5,
//	                         G20: no hay UI que pregunte).
//	  --model 'prov/modelo'  Selecciona el modelo/provider del turno (anula el
//	                         `model` por defecto de `agent.toml`).
//
// CÓDIGOS DE SALIDA (la convención de S45, arquitectura nº5 / agente.md §5; los
// modos headless salen con un código coherente para CI/scripts):
//
//	0  éxito.
//	1  error de ejecución: el chunk de `-e`, el turno del agente o un fallo del
//	   provider lanzaron (error estructurado §1.4 o no), o el arranque (`Boot`)
//	   falló (grafo de plugins inválido, `nu.toml` roto).
//	2  error de uso: flags incompatibles o un argumento requerido ausente.
//	3  permiso denegado en headless: una tool sensible se denegó por falta de
//	   `--auto-permissions` (agente.md §5). Código DISTINTO para que un script/CI
//	   distinga "el modelo no pudo actuar por permisos" de un fallo de ejecución;
//	   el mensaje (stderr) nombra el patrón `allow` a añadir o `--auto-permissions`.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/dbareagimeno/nu/internal/runtime"
)

// Códigos de salida del binario (la convención de S45; ver el doc de paquete).
const (
	exitOK     = 0 // éxito
	exitError  = 1 // error de ejecución o de arranque
	exitUsage  = 2 // flags/argumentos inválidos
	exitDenied = 3 // permiso denegado en headless (falta --auto-permissions)
	statusOK   = "OK"
	statusDeny = "DENIED" // marca interna que el driver Lua devuelve al detectar deny
)

// cliOptions recoge los flags ya parseados. Separar el parseo (en `run`) de la
// ejecución (en `runWith`) hace la lógica del CLI TESTEABLE sin lanzar el proceso:
// un test construye un Runtime con dirs de prueba y llama `runWith` directamente.
type cliOptions struct {
	eval      string // -e: chunk Lua a evaluar (headless)
	prompt    string // -p: prompt del turno de agente headless
	promptSet bool   // hay prompt con el que ejecutar el modo agente (true solo si -p NO vacío; `-p ""` cuenta como ausencia)
	cont      bool   // --continue: reanudar la última sesión del cwd (G18)
	autoPerm  bool   // --auto-permissions: modo "auto" de permisos (agente.md §5)
	model     string // --model: provider/modelo del turno (anula agent.toml)
	defConfig bool   // --default-config: activa el conjunto oficial de producto (ADR-015, G33)
}

func main() {
	os.Exit(run())
}

func run() int {
	var opts cliOptions
	flag.StringVar(&opts.eval, "e", "", "ejecuta el código Lua dado e imprime sus valores de retorno (headless)")
	flag.StringVar(&opts.prompt, "p", "", "ejecuta un turno de agente headless con este prompt e imprime el texto final")
	flag.BoolVar(&opts.cont, "continue", false, "reanuda la última sesión del proyecto (cwd) antes de enviar el prompt (G18)")
	flag.BoolVar(&opts.cont, "c", false, "alias de --continue")
	flag.BoolVar(&opts.autoPerm, "auto-permissions", false, "permisos del agente en modo \"auto\" (agente.md §5); sin él, en headless las tools sensibles se deniegan")
	flag.StringVar(&opts.model, "model", "", "selecciona el provider/modelo del turno de agente (anula agent.toml)")
	flag.BoolVar(&opts.defConfig, "default-config", false, "activa el conjunto oficial de producto: solo, escribe plugins.enabled en nu.toml y sale; con -p/-e, lo activa solo para ese proceso (ADR-015)")
	flag.Parse()
	// El modo agente exige un prompt NO vacío (un turno necesita algo que enviar),
	// así que tratamos `-p ""` igual que la ausencia: ambos son "sin prompt". No hay
	// que distinguirlos —ninguno dispara un turno—; `promptSet` resume "hay un prompt
	// con el que ejecutar el modo agente".
	opts.promptSet = opts.prompt != ""

	// `-e` (eval Lua) y el modo agente (`-p`/`--continue`) son modos EXCLUYENTES (ver
	// checkFlagConflicts): sin este corte `runWith` resolvería a favor de `-e` en silencio,
	// descartando el prompt y sus modificadores y saliendo 0 —engañoso para un script/CI—.
	if code := checkFlagConflicts(opts); code != exitOK {
		return code
	}

	// Una acción headless es la que dispara un modo no interactivo (`-e`/`-p`/--continue).
	// `--auto-permissions`/`--model` sueltos NO la disparan: son modificadores de un turno
	// que aquí no existe. `--default-config` tampoco es una acción headless por sí mismo:
	// su modo SOLO (persistente) escribe y sale; combinado con una acción headless pasa a
	// EFÍMERO (activa el conjunto en memoria y ejecuta esa acción).
	headless := opts.eval != "" || opts.promptSet || opts.cont

	// Modo EFÍMERO (ADR-015, G33): `--default-config` + acción headless. El runtime se
	// construye con `WithEnabledPlugins(conjunto de producto)`, que fija la activación EN
	// MEMORIA sin tocar disco; luego arranca y ejecuta la acción (`-e`/`-p`). El conjunto
	// sale de `OfficialProductSet` (estático, del binario), que se resuelve antes de `New`.
	// En el modo persistente (`--default-config` sin acción headless) NO se inyecta nada:
	// ese modo solo escribe el fichero, no arranca.
	var newOpts []runtime.Option
	if opts.defConfig && headless {
		names, err := runtime.OfficialProductSet()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return exitError
		}
		newOpts = append(newOpts, runtime.WithEnabledPlugins(names))
	}

	rt := runtime.New(newOpts...)
	defer rt.Close()

	// `--default-config` SOLO (sin acción headless): modo PERSISTENTE (ADR-015, G33).
	// Escribe el conjunto oficial de producto en `config.dir()/nu.toml` y SALE, sin
	// arrancar nada. No depende del TTY (es el onramp que la pantalla desnuda de G21 no
	// daba sin terminal): no hace `Boot`, solo escribe el fichero.
	if opts.defConfig && !headless {
		return runDefaultConfig(rt)
	}

	if !headless {
		// Sin `-e`/`-p`: el arranque INTERACTIVO (S33, CP-7). Con un TTY, el binario da
		// vida al `nu.ui`: arranca el contenido (la pantalla desnuda de G21 si no hay
		// plugins, o el `Boot` canónico que corre los `init.lua`) y entra en el bucle del
		// driver de TTY (raw mode, pintado al terminal, teclado, resize) hasta que se pide
		// apagar. Sin TTY (salida redirigida, CI) no hay superficie: se imprime el uso.
		if !rt.UIActive() {
			fmt.Fprintln(os.Stderr, "uso: nu [--default-config] | [-e '<lua>'] | [-p '<prompt>' [--continue] [--auto-permissions] [--model prov/modelo]]")
			return exitUsage
		}
		return runInteractive(rt)
	}

	// Arranque canónico (§14, S11/S12): lee `config.dir()/nu.toml` (activación de
	// plugins, rutas extra, presupuesto del watchdog), carga los plugins activados
	// en orden topológico —las extensiones embebidas solo si `plugins.enabled` las
	// nombra, ADR-010—, ejecuta el `init.lua` del usuario el último y emite
	// `core:ready`. Un grafo roto (colisión, ciclo, dependencia ausente), un
	// `nu.toml` mal formado o un `plugins.enabled` que nombra algo inexistente es un
	// error de arranque accionable que apunta a la línea de `nu.toml` que lo arregla.
	if err := rt.Boot(); err != nil {
		fmt.Fprintln(os.Stderr, "error de arranque:", err)
		return exitError
	}

	return runWith(rt, opts)
}

// runInteractive arranca el runtime con un TTY (S33, CP-7): da vida al `nu.ui`. Decide
// el CONTENIDO —la pantalla de runtime desnudo (G21) si no hay plugins activos, o el
// `Boot` canónico que corre los `init.lua` de plugins y usuario si los hay— y entra en el
// bucle del driver de TTY (`RunInteractive`: raw mode, pintado al terminal, teclado,
// resize, señales) hasta que se pide apagar. Un fallo de arranque o de inicialización del
// terminal sale con código 1.
func runInteractive(rt *runtime.Runtime) int {
	if rt.BareScreenActive() {
		// Sin plugins ni init.lua activo: la pantalla desnuda, con salida por teclado.
		rt.PrepareBareScreen()
	} else {
		// Red de salida de emergencia del kernel (ADR-017, G35) al FONDO de la pila,
		// ANTES de montar la UI de producto: cualquier app la tapa, pero si el arranque
		// no llega a montar UI (p. ej. un init.lua que falla) el usuario puede salir con
		// q/esc/ctrl+c en vez de quedar atrapado en raw mode.
		rt.InstallEmergencyExit()
		// Hay plugins/usuario que montan UI: arranque canónico (corre sus init.lua).
		if err := rt.Boot(); err != nil {
			fmt.Fprintln(os.Stderr, "error de arranque:", err)
			return exitError
		}
	}
	if err := rt.RunInteractive(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitError
	}
	return exitOK
}

// checkFlagConflicts valida las combinaciones de flags EXCLUYENTES antes de resolver
// el modo, y devuelve el código de uso inválido (`exitUsage`, escribiendo el porqué a
// stderr) o `exitOK` si no hay conflicto. Se separa de `run` para poder testear la
// regla sin tocar `flag`/os.Args ni construir un Runtime.
//
// `-e` (evaluar un chunk Lua, §1.3) y el modo agente (`-p`/`--continue`) eligen modos
// distintos que `runWith` NO combina: resolvería a favor de `-e` y descartaría en
// silencio el prompt y sus modificadores (--continue/--auto-permissions/--model),
// saliendo 0 y engañando a un script/CI. Es uso inválido (código 2), como el resto de
// combinaciones incompatibles. `-p ""` cuenta como ausencia (promptSet=false): no
// colisiona con `-e`.
func checkFlagConflicts(opts cliOptions) int {
	if opts.eval != "" && (opts.promptSet || opts.cont) {
		fmt.Fprintln(os.Stderr, "uso: -e y -p/--continue son incompatibles "+
			"(evaluar un chunk Lua o ejecutar un turno de agente, no ambos)")
		return exitUsage
	}
	return exitOK
}

// runWith ejecuta el modo headless elegido sobre un Runtime YA arrancado (`Boot`
// hecho). Es el núcleo TESTEABLE del CLI: devuelve el código de salida y escribe la
// salida útil a stdout y los errores a stderr, sin tocar `os.Exit` ni `flag`. Un
// test le pasa un Runtime con dirs de prueba y los `opts` ya fijados.
func runWith(rt *runtime.Runtime, opts cliOptions) int {
	if opts.eval != "" {
		return runEval(rt, opts.eval)
	}
	return runAgent(rt, opts)
}

// runDefaultConfig respalda el modo PERSISTENTE de `nu --default-config` (ADR-015,
// G33): escribe el conjunto oficial de producto en `config.dir()/nu.toml` y SALE, sin
// arrancar. Reusa `rt.WriteDefaultConfig` (que reusa `writeEnabledPlugins`: preserva el
// resto del fichero, atómico, idempotente, no sobrescribe un `nu.toml` mal formado).
// Informa a stdout qué activó y dónde —accionable: el usuario sabe el fichero exacto y
// el siguiente paso—. Un fallo de escritura (E/S, o `nu.toml` roto que no se pisa) sale
// con código 1 y mensaje accionable a stderr. Construye un Runtime mínimo solo para
// resolver `config.dir()` y escribir; no hace `Boot` (no carga ni una extensión).
func runDefaultConfig(rt *runtime.Runtime) int {
	dir, names, createdTemplates, err := rt.WriteDefaultConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitError
	}
	fmt.Printf("conjunto oficial de producto activado en %s/nu.toml: %s\n",
		dir, strings.Join(names, ", "))
	// Plantillas de config de agente (ADR-017, G35): informamos solo de las que
	// CREAMOS este comando; las que ya existían se respetan y no se nombran como si
	// las hubiéramos escrito.
	if len(createdTemplates) > 0 {
		fmt.Printf("plantillas de config creadas: %s\n", strings.Join(createdTemplates, ", "))
	}
	// Mensaje honesto: el harness aún necesita una API key para hablar con el modelo
	// (la plantilla usa anthropic/opus con ANTHROPIC_API_KEY). Sin ella, el chat
	// abre igual pero el primer turno dará un error accionable; con ella, ya funciona.
	fmt.Printf("exporta tu API key (p. ej. ANTHROPIC_API_KEY) o edita %s/providers.toml; "+
		"luego ejecuta `nu` (chat) o `nu -p '<prompt>'` (headless)\n", dir)
	return exitOK
}

// runEval respalda `nu -e '<lua>'`: evalúa el chunk en el estado principal (no es
// task; §1.3) e imprime cada valor de retorno en su propia línea a stdout. `print`
// (que va al log, no a la pantalla, §15) no interfiere con esta salida.
func runEval(rt *runtime.Runtime, code string) int {
	results, err := rt.EvalString(code)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitError
	}
	for _, r := range results {
		fmt.Println(r)
	}
	return exitOK
}

// runAgent respalda `nu -p '<prompt>'` (con sus modificadores --continue/
// --auto-permissions/--model): ejecuta un TURNO de agente HEADLESS y escribe el
// texto final del asistente a stdout. Construye el DRIVER Lua (agentDriver), le
// pasa los argumentos del CLI como globales (SetStringGlobal, sin interpolarlos:
// cero inyección por un prompt con comillas/saltos), y lo corre como TASK
// (EvalTaskString) porque el turno (`Session:send`) suspende (⏸).
//
// El driver devuelve `(texto, estado)`: `estado == "DENIED"` cuando una tool
// sensible se denegó en headless (sin `--auto-permissions`), lo que mapeamos al
// código de salida 3 (agente.md §5). Un error del turno/provider sale como código 1.
func runAgent(rt *runtime.Runtime, opts cliOptions) int {
	if !opts.promptSet {
		// --continue/--auto-permissions/--model sin un prompt no tienen turno que
		// ejecutar: es uso inválido (el modo agente headless necesita algo que enviar).
		fmt.Fprintln(os.Stderr, "uso: el modo agente requiere un prompt: nu -p '<prompt>' [--continue] [--auto-permissions]")
		return exitUsage
	}

	// Argumentos del CLI → globales Lua que el driver lee (sin interpolación).
	rt.SetStringGlobal("NU_CLI_PROMPT", opts.prompt)
	rt.SetStringGlobal("NU_CLI_MODEL", opts.model)
	rt.SetStringGlobal("NU_CLI_CONTINUE", boolFlag(opts.cont))
	rt.SetStringGlobal("NU_CLI_AUTOPERM", boolFlag(opts.autoPerm))

	results, err := rt.EvalTaskString(agentDriver)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return exitError
	}

	// El driver devuelve (texto, estado). Falta de retornos = comportamiento
	// inesperado del driver, pero no rompemos: imprimimos lo que haya.
	text, status := "", statusOK
	if len(results) >= 1 {
		text = results[0]
	}
	if len(results) >= 2 {
		status = results[1]
	}

	if text != "" {
		fmt.Println(text)
	}
	if status == statusDeny {
		// El texto accionable del deny ya viajó al modelo como tool_result; aquí
		// dejamos en stderr el porqué del código 3, sin volcar de nuevo el detalle.
		fmt.Fprintln(os.Stderr, "permiso denegado en headless: una tool sensible requería permiso; "+
			"añade un `allow` en agent.toml o ejecuta con --auto-permissions (agente.md §5)")
		return exitDenied
	}
	return exitOK
}

// boolFlag rinde un bool al "1"/"" que el driver Lua interpreta (un global ausente
// o cadena vacía es false; "1" es true). Evita pasar booleanos por el puente: el
// CLI solo necesita strings.
func boolFlag(b bool) string {
	if b {
		return "1"
	}
	return ""
}

// agentDriver es el DRIVER Lua del modo agente headless: Lua puro sobre las
// extensiones `agent` (S39) y `sessions` (S38), exactamente como lo escribiría un
// usuario en su `init.lua` (ADR-003: el core no privilegia al CLI). Lee sus
// parámetros de los globales `NU_CLI_*` (los fija main.go con SetStringGlobal) y
// devuelve `(texto_final, estado)` donde `estado` es "OK" o "DENIED".
//
// Corre como TASK (EvalTaskString), así que puede usar funciones ⏸ —`Session:send`
// es ⏸ (agente.md §2)— directamente, sin envolverlo en `nu.task.spawn`.
//
// `--continue` (G18): la sesión más reciente del proyecto sale de
// `sessions.list(cwd)` ordenando los ids descendente (ordenan lexicográfico =
// temporal, sesiones.md §2/§7) y se pasa como `resume`.
//
// Detección de deny headless: se suscribe al evento ESTRUCTURADO
// `agent:permission.denied` (agente.md §5, G40) y marca el estado solo cuando
// `ev.source == "headless"` —el enum cerrado de la extensión distingue el deny por
// falta de UI (G20) de un default-deny, un veto de hook o una decisión de usuario—.
// Nada de matching sobre texto libre: el `source` es dato, no prosa, y no da falsos
// positivos (un default-deny también nombra `allow` en su mensaje accionable).
//
// IMPORTANTE — el estado del handler vive en una TABLA (`state`), no en un escalar:
// los handlers de `nu.events` corren sobre un thread efímero (ADR-008), y mutar un
// upvalue ESCALAR (`denied = true`) desde ese thread no se propaga de vuelta al
// thread del driver de forma fiable; mutar el CONTENIDO de una tabla capturada SÍ
// (la tabla es una referencia). Es el mismo patrón que usan el agente y el chat para
// el estado compartido entre handlers (ver docs/decisiones-implementacion.md S45).
const agentDriver = `
-- El modo agente exige las extensiones oficiales activas (ADR-010: inactivas por
-- defecto). Si no lo están, un error accionable que nombra la línea de nu.toml a
-- añadir —misma filosofía que los demás errores de arranque (§14)— en vez del crudo
-- "module not found" de require.
local function need(name)
  local ok, mod = pcall(require, name)
  if not ok then
    error({ code = "EAGENT",
      message = "el modo agente requiere la extensión '" .. name ..
        "' activa; añade plugins.enabled = {\"providers\", \"sessions\", \"agent\"} en " ..
        nu.config.dir() .. "/nu.toml (ADR-010: las extensiones oficiales están inactivas por defecto)",
      detail = { reason = "extension_inactive", extension = name } })
  end
  return mod
end

local agent = need("agent")
local sessions = need("sessions")

local cwd = nu.fs.cwd()
local opts = { cwd = cwd, permissions = {} }

if NU_CLI_AUTOPERM == "1" then
  opts.permissions.mode = "auto"
end
if NU_CLI_MODEL ~= nil and NU_CLI_MODEL ~= "" then
  opts.model = NU_CLI_MODEL
end

if NU_CLI_CONTINUE == "1" then
  local list = sessions.list(cwd)
  if #list == 0 then
    error({ code = "EAGENT",
      message = "--continue: no hay sesiones previas en este proyecto (" .. cwd .. ")",
      detail = { reason = "no_sessions", cwd = cwd } })
  end
  -- La más reciente: los ids ordenan lexicográfico = temporal (sesiones.md §2/§7).
  table.sort(list, function(a, b) return a.id > b.id end)
  opts.resume = list[1].id
end

-- Estado compartido con el handler de eventos (tabla, no escalar: ver arriba).
local state = { denied = false }
local sub = nu.events.on("agent:permission.denied", function(ev)
  -- Solo el deny por AUSENCIA de UI (headless, G20) mapea al código 3: es el que
  -- --auto-permissions habría concedido. Un default-deny, un veto de hook o un deny
  -- de usuario son denegaciones legítimas, no "falta el flag". El campo source es un
  -- enum cerrado de la extensión (agente.md §5), no texto libre.
  if ev.source == "headless" then
    state.denied = true
  end
end)

local s = agent.session(opts)
local final = s:send(NU_CLI_PROMPT)
s:close()
sub:cancel()

-- Texto final del asistente: concatena los bloques de texto del Message (§2).
local text = ""
if type(final) == "table" and type(final.content) == "table" then
  for _, b in ipairs(final.content) do
    if b.type == "text" then
      text = text .. b.text
    end
  end
end

return text, (state.denied and "DENIED" or "OK")
`
