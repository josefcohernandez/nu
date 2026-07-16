---
title: "El slug de proyecto de `sessions/<proyecto>/` no está especificado"
type: "hallazgo"
id: "G38"
status: "resuelto"
date: "2026-07-02"
origin: "ronda 8 de pseudocódigo (malla distribuida de agentes sobre git)"
resolution: "El algoritmo cwd→slug se congela como parte del formato, con sessions.slug/dir como helpers públicos de la extensión."
affected: ["sesiones.md §2/§7"]
---
# G38 · El slug de proyecto de `sessions/<proyecto>/` no está especificado — `sesiones.md` §2/§7 — **RESUELTO**

**Resolución** (aplicada en [sesiones.md](sesiones.md) §2): la opción (c), con el **algoritmo actual congelado tal cual**. Dos piezas:

1. **El slug pasa a ser parte del formato.** §2 especifica la codificación que la implementación ya hacía: todo carácter fuera de `[A-Za-z0-9.-]` → `_`, recorte de `_` en ambos bordes, vacío → `"root"`. Se congela con sus propiedades declaradas honestamente: **legible y con pérdida** — no reversible, colisiones posibles entre `cwd` patológicamente parecidos (`/a/b` y `/a_b`). No es una identidad sino una **clave de agrupación**: la identidad canónica de la sesión viaja dentro del fichero (línea `meta`, con `cwd` e `id`), y desambiguar una colisión es leer `meta`. Se descartó una codificación reversible (percent-encoding): compraría una propiedad que ningún consumidor pidió al precio de la legibilidad y de migrar todos los directorios existentes.
2. **La extensión expone la codificación como funciones puras**: `sessions.slug(cwd) -> string` y `sessions.dir(cwd) -> string`, junto a `open`/`list` en `require("sessions")`. Mismo reparto que G6/G22: el contrato da la garantía (el algoritmo especificado, para herramientas externas que componen rutas sin enu), la extensión da la comodidad (los plugins no reimplementan).

Nota para la sesión de construcción: la grieta ya mordía **dentro del repo** — tres copias del algoritmo sincronizadas por fe (`slug` en `sessions/init.lua`, `trust_slug` duplicado literal en `agent/init.lua`, y la réplica en Go de `main_test.go` con el comentario "debe coincidir con `slug` de sessions/init.lua"). Al construir: `sessions.slug` queda como única fuente Lua (el agente lo `require`a para las claves de `trust.json`), y la copia del test de Go — inevitable, Go no llama a Lua — pasa a replicar la *especificación*, citándola, no el código.

**Problema.** [sesiones.md](sesiones.md) §1 se documenta como convención pública ("cualquier extensión o herramienta externa puede leer sesiones sin pasar por el agente") y §2 ubica los transcripts en `sessions/<proyecto>/`, con "`<proyecto>` = cwd codificado como slug" — pero el algoritmo cwd→slug no está escrito en ningún documento. La promesa de lectura por terceros no se puede ejercer: quien quiera *localizar* el fichero de una sesión conociendo el `cwd` y el id tiene que adivinar (o ingeniería-inversear) la codificación. Aflorada en la ronda 8 de pseudocódigo ([pseudocodigo.md](pseudocodigo.md), escenarios 33-35), donde una malla distribuida lo necesita tres veces: comitear el transcript dentro de la rama-resultado, leer el transcript para elegir el punto de un `fork(at)`, e importar una sesión ajena copiando el JSONL a su sitio.

**Impacto.** Cualquier consumidor externo del formato: orquestadores, exportadores, estadísticas de coste, pickers de terceros. También muerde *dentro* del proceso: un plugin que quiera leer el transcript de una sesión que él mismo abrió no tiene forma contractual de encontrar el fichero. Barato de cerrar (es especificar lo que la implementación ya hace, o exponer un helper); caro de cambiar después, cuando haya herramientas externas dependiendo de una codificación adivinada.

**Opciones.** (a) Especificar el algoritmo del slug en sesiones.md §2 (determinista, sin estado, documentado como parte del formato); (b) no especificarlo y exponer un helper de la extensión (`agent.sessions.dir(cwd) -> string` o `agent.sessions.path(cwd, id)`), dejando la codificación como detalle interno — pero entonces las herramientas *externas* (fuera de enu) siguen sin poder resolver rutas; (c) ambas: el algoritmo especificado es la verdad para herramientas externas y el helper es la comodidad para plugins.
