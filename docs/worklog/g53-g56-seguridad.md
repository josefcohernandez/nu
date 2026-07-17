---
title: "Construcción del lote de seguridad G53+G54+G55+G56 (auditoría 2026-07-16)"
type: "sesion"
id: "G53-G56"
status: "cerrada"
date: "2026-07-17"
---
# G53–G56 — Construcción del lote de la auditoría de seguridad

Sesión post-plan (cuatro filas en la bitácora de
[estado.md](../plan/estado.md), rama `claude/g53-g56-seguridad`). Las cuatro
resoluciones estaban decididas en los documentos desde el 2026-07-16
([findings](../findings/README.md) G53–G56, ADR-023, ADR-024); esta sesión las
construye sin tocar la espec. Decisiones operativas y desviaciones que el
contrato dejaba implícitas, resueltas siempre hacia el lado seguro:

**G54 — la zona gris del upgrade TLS.** La nota G54 de api.md define cross-host
como "host (nombre y puerto) distinto" O "degradación `https`→`http`". La
segunda cláusula solo tiene sentido si la identidad de host de la primera
ignora el esquema, así que la implementación pliega el puerto default del
esquema (80/443 → "") antes de comparar: `http://a` y `https://a` son el mismo
host y un *upgrade* `http`→`https` same-host NO recorta cabeceras (recortar ahí
rompería auth legítima en el redirect benigno más común); solo la degradación
explícita recorta. Es lectura fiel del contrato, no ampliación — pero conviene
fijarla explícitamente en api.md §8 en la próxima pasada editorial.

**G56 — display vs. supervisión.** ADR-024 pide que los artefactos de
atribución distingan `<plugin> (worker)` y a la vez que lo lanzado por el
worker "se registre bajo ese plugin". La implementación separa ambos planos:
el sufijo `(worker)` es solo de *display* (`enu.log`); la clave de
*supervisión* de `enu.proc` usa el nombre crudo del plugin, para que
`plugin.reload` alcance a los procesos del worker (sin fuga de supervisión).

**G53 — dos huecos del tokenizador cerrados fail-closed.** (1) El escape `\`
no está en la lista de constructos del contrato, pero sin tratarlo una comilla
escapada (`echo \" ; curl evil`) engañaba al rastreador de comillas y ocultaba
el `;`: se trata como literal, lo que solo puede sobre-partir o sobre-rechazar
(dirección segura). (2) Bash ejecuta `$( )`/backticks *dentro* de comillas
dobles aunque `"…"` sea modelable: se detectan como no modelables también ahí
(`$VAR` en dobles sigue siendo literal opaco).

**G55 — la tool `bash` no existía.** El contrato la presupone y la maquinaria
de G53 la anticipaba, pero ningún código la registraba (los tests de G53 usaban
tools ad hoc). Se creó como scaffolding mínimo (`tools_bash.lua`: `/bin/sh
-c`, stdout+stderr+exit code, permiso "ask" por defecto del registro) porque
era prerrequisito para testear el scrubbing con subprocesos reales. Merece
sesión propia si se quiere endurecer (timeouts, streaming de salida, cwd).

Verificación del lote: `go build ./...`, suite completa `-race -shuffle=on`
(runtime + raíz) y `internal/vmwasm` en verde; `go run . -e 'return
enu.version.api'` → `4`; `gofmt`/`go vet` limpios. Panel clean-room
(revision-limpia) sobre el diff completo de la rama.

## Cierre del panel NO CONFORME (2026-07-17)

El panel clean-room dio **NO CONFORME** con dos hallazgos; ambos atendidos en
el commit de cierre, sin tocar los commits del lote:

**Concurrencia — la foto del overlay de `setenv` en `spawnProc`.** El panel
señaló que, tras G56, un `enu.proc.spawn` puede nacer en la goroutine de un
worker, y que ahí `spawnProc` lee `rt.sys.envOverlay()` mientras el estado
principal puede correr `enu.sys.setenv` — misma superficie que SEC-05. **Al
verificarlo empíricamente resultó que esa lectura NO es una carrera**: `enu.sys`
protege el mapa `envOver` con `sysState.mu` (candado que envuelve tanto
`envOverlay` como `setenv`, diseño de S17 pensado justo para las lecturas de
fondo de `enu.proc` sin token). Lo que SÍ estaba mal era el **comentario** en
`spawnProc`, que atribuía la seguridad a "correr en el estado principal" —falso
desde G56—: se reescribe para atribuirla al candado y para dejar constancia de
que aquí NO aplica la foto-en-el-spawn-del-worker de ADR-024 (tomarla al crear
el worker rompería §7: el hijo debe ver los `setenv` previos a ESTE spawn, no
los previos a la creación del worker). Se añade el test de carrera que faltaba
—`TestSpawnDesdeWorkerConcurrenteConSetenv`, que solapa `setenv` del principal
con `enu.proc.spawn` desde un worker— verde bajo `-race` (y rojo si se quita el
candado: es regresión real).

**Tests — el inventario 🔒 no crecía.** Se añaden dos filas al inventario de
[implementacion.md](../plan/implementacion.md), con el precedente de
`G42 (extensión)`: `G53 (extensión)` (tokenizador/máquina de estados de
permisos de bash, `decompose_bash`/`match_bash`) y `G54 (kernel)` (política de
redirects, `withRedirectPolicy`/`isCrossHost`), cada una nombrando el caso
exacto que su test blinda.
