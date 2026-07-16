---
title: "Conjunto oficial de producto y onramp no interactivo"
type: "adr"
id: "ADR-015"
status: "aceptada"
date: "2026-06"
---
# ADR-015 · Conjunto oficial de producto y onramp no interactivo

**Estado:** Aceptada · 2026-06 (**refina** [ADR-010](adr-010-extensiones-oficiales-distribuidas-con-nu.md); resuelve [G33](../../findings/g33-el-arranque-sin-tty.md)) · **Refinada por [ADR-017](adr-017-el-onramp-deja-config.md)** (el onramp deja también config de agente usable) y por **ADR-018** (qué significa "el conjunto oficial" con TTY: el repl cede la pantalla al chat, G36); ninguna la reemplaza: el "conjunto oficial" y los dos modos siguen siendo de este ADR

**Contexto.** ADR-010 dejó las extensiones oficiales **inactivas por defecto** y
[G21](../../findings/g21-el-primer-arranque-de-adr.md)
les dio el onramp del primer arranque: la **pantalla de runtime desnudo**. Pero esa
pantalla es UI —existe **solo con TTY interactivo**—; [api.md](../../contracts/api.md) §14 lo cierra
explícito: "Sin TTY no hay pantalla: arranca desnudo". Al *usar* el binario ya
terminado para probarlo con su harness en CI/Docker/scripts (sin TTY) aparecen dos
cabos que ADR-010 no ató: (1) **no hay un paso** para activar el conjunto oficial sin
TTY —hay que escribir `config.dir()/nu.toml` a mano, lo que contradice la ergonomía
"de una tecla" que el propio ADR-010 promete—; y (2) **"el conjunto oficial" nunca se
definió** con precisión: hoy `ActivateOfficial()` activa `embeddedNames()` *entero*,
que incluye `example` —el plugin-andamiaje que existe solo para probar el gating
([implementacion.md](../../plan/implementacion.md), Fase 8)—, de modo que la acción TTY ya mete
el plugin de pruebas en la config del usuario.

**Decisión.** Dos piezas, **ninguna en la API sagrada** `nu.*` (es superficie CLI y
loader, no `nu.version.api`):

1. **Un flag de CLI, `nu --default-config`**, espejo no interactivo de la acción
   "activar el conjunto oficial" de la pantalla desnuda, con **dos modos**:
   - **Solo** (`nu --default-config`): escribe `plugins.enabled` con el conjunto de
     producto en `config.dir()/nu.toml` —preservando el resto, atómico, idempotente,
     reusando la misma `writeEnabledPlugins` que la acción TTY— y **sale**.
   - **Con acción headless** (`--default-config -p '…'` / `-e '…'`): **no toca disco**;
     activa el conjunto **solo para ese proceso** (una option de runtime nueva,
     `WithEnabledPlugins`, que fija `enabled` en memoria antes de `Boot`) y ejecuta la
     acción. Es el caso Docker inmutable: correr con todo activo sin reescribir config
     en cada `docker run`.

2. **"El conjunto oficial de producto"** queda fijado en las **siete** extensiones
   embebidas de producto —`providers, sessions, agent, mcp, chat, repl, toolkit`— = el
   catálogo embebido **menos `example`**. Es cerrado bajo dependencias
   (`agent → providers, sessions`; `mcp → agent`; `chat → toolkit, agent, providers,
   sessions`). Una sola fuente de verdad, `officialProductSet` (derivada de
   `embeddedNames` filtrando `example`); la acción TTY de G21 pasa a usarla también, de
   modo que **la pantalla desnuda y el flag activan exactamente lo mismo**.

El conjunto es **idéntico en ambos modos**, incluido `chat`: aunque `chat`/`repl`
necesitan TTY, sus `init.lua` ya se auto-gatean con `if nu.has("ui")` y quedan inertes
sin superficie de UI (G20), así que activarlos en headless no estorba; tener una
segunda lista "sin UI" sería un caso borde sin ganancia.

**Razonamiento.**
- **Por qué un flag y no ampliar la API (`nu.config.enable_official()` + `nu -e`).**
  Exponerlo a Lua **ampliaría la superficie sagrada** (`nu.version.api`++, el coste más
  caro del proyecto y lo que [api.md](../../contracts/api.md) §17 blinda) para *empeorar* la ergonomía:
  `nu -e 'nu.config.enable_official()'` no es más fácil de recordar ni de teclear que el
  flag. Falla el objetivo declarado (instalación fácil) pagando el precio más alto.
- **Por qué un flag y no un subcomando `nu init`.** Sería honesto (un verbo para una
  acción con efecto en disco), pero estrenaría el **primer subcomando** del binario, que
  hoy es solo flags (`-e`, `-p`, `--continue`…): una puerta a `nu run`/`nu chat`… que
  S45 evitó a propósito manteniendo el binario delgado y delegando en extensiones. Si más
  adelante aparecen varias acciones de gestión, `nu config <verbo>` se justificará solo;
  por una sola necesidad es prematuro. **Disparador de reapertura:** una tercera o cuarta
  acción de configuración del binario.
- **Por qué excluir `example` del conjunto.** No es producto: es andamiaje de pruebas del
  gating de ADR-010. Que la acción TTY lo activara hoy es un descuido tolerable solo
  porque es visible en pantalla; meterlo en una "config por defecto" lo convierte en
  sorpresa. Sigue siendo activable **suelto** (la acción "activar extensiones sueltas" y
  un `plugins.enabled = ["example"]` a mano), que es lo único que necesita.
- **Por qué vive en el binario y no rompe ADR-003.** El CLI orquesta extensiones por la
  API pública igual que podría un `init.lua` de usuario: el core sigue sin saber lo que es
  un agente. Es exactamente la frontera de S45 (la superficie CLI vive en `main.go`, no en
  `nu.*`).

**Consecuencias.**
- Instalar `nu` y tenerlo "batteries-included" en CI/Docker es **un comando**
  (`nu --default-config`), sin editar TOML a mano. La promesa "de una tecla" de ADR-010
  vale ahora también sin TTY.
- "El conjunto oficial" tiene una **definición única** (`officialProductSet`); la pantalla
  desnuda (G21) y el flag no pueden divergir. `ActivateOfficial()` deja de activar
  `example`: cambio de comportamiento observable, cubierto por su test.
- La superficie sagrada **no crece**: `nu.version.api` se queda igual. La única API nueva
  es interna al runtime (`WithEnabledPlugins`, una option de `runtime.New`, no `nu.*`).
- **Sin red** (ADR-010): activar sale del binario embebido, en ambos modos.
- **Disparador de reapertura:** si el binario acumula más acciones de configuración
  (varios flags `--…-config` o equivalentes), reconsiderar el subcomando `nu config`
  descartado aquí.
