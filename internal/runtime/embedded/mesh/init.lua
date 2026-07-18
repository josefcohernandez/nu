-- Extensión oficial `mesh`: la malla de agentes (contrato: docs/malla.md).
--
-- Nace de la Ronda 8 de pseudocódigo ("kubernetes de agentes"): nodos `enu`
-- headless que ejecutan trabajos declarativos (specs Role+Job) sobre ramas de
-- git — claim por CAS de refs, worktree por job, presupuesto duro, denegaciones
-- como dato (G40) y torneo de forks (G39). Todo Lua puro sobre la API pública
-- congelada (api.md) y los contratos de `agent` y `sessions` (ADR-003: cero
-- privilegio de kernel; si algo no se pudiera escribir así, la API estaría
-- incompleta — la Ronda 8 lo validó ANTES de construir).
--
-- El `init.lua` solo CABLEA: deja el módulo accesible por `require("mesh")`.
-- No hay daemon ni auto-arranque (malla.md §1.1: primitivas componibles; el
-- bucle del nodo es un patrón del usuario, §10). Cargar la extensión no toca
-- ni la red ni git. Fuera del conjunto oficial de producto (malla.md §1.4):
-- se activa explícitamente.

require("mesh")

enu.log.info("mesh: extensión cargada (malla.md; el nodo es tuyo: require(\"mesh\"))")
