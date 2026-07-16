---
title: "Las denegaciones de permisos no son observables como dato"
type: "hallazgo"
id: "G40"
status: "resuelto"
date: "2026-07-02"
origin: "ronda 8 de pseudocódigo (malla distribuida de agentes sobre git)"
resolution: "Toda denegación emite agent:permission.denied y viaja también en el meta del tool_result, con tool.end especificado para denegaciones."
affected: ["agente.md §4/§5"]
---
# G40 · Las denegaciones de permisos no son observables como dato — `agente.md` §4/§5 — **RESUELTO**

**Resolución** (aplicada en [agente.md](../contracts/agente.md) §4 —el evento nuevo en la lista de notificaciones— y §5 —párrafo "La denegación viaja como dato"—): la opción (c) más la sub-decisión (d). El principio: la prosa accionable es *presentación*, no el portador (coherente con los errores estructurados de api.md §1.4). Toda denegación produce **una sola vez** el objeto `{ id, tool, args?, source = "deny"|"hook"|"default"|"headless", pattern?, suggested? }` con dos destinos para dos consumidores distintos:

1. **Evento `agent:permission.denied`** (simétrico de `permission.asked`, atribución G3): para observadores **vivos** — el driver del nodo, telemetría, UIs que agreguen denegaciones.
2. **El mismo objeto en el `meta` del `tool_result` denegado** (providers.md §2.2), que sesiones.md §3 persiste intacto: la denegación **viaja con el transcript**, y un controlador que lea la sesión a posteriori — incluso en otra máquina, leyendo la rama-resultado de la ronda 8 — la extrae sin parsear prosa. Un evento solo no bastaba (no viaja); un `meta` solo tampoco (obliga a los observadores vivos a leer transcripts).

Además queda especificado lo que el escenario 36 encontró ambiguo y la implementación ya hacía: **`tool.end` se emite también para calls denegadas** (todo `tool.start` tiene su `tool.end`; las UIs emparejan), con `is_error = true` — canal *genérico* de fallo, mientras `permission.denied` es el *específico*. La prosa del amortiguador 2 de §5 no cambia: sigue siendo lo que el modelo ve y el humano lee.

La implementación hizo el mejor argumento a favor: el dato **existía y se descartaba en la frontera** — `check_permission` calcula `suggested` (`agent/init.lua:377`) y lo formatea dentro del string; `permission.asked` ya emitía `{ id, tool, args, suggested }` como dato (línea 397) mientras la denegación —el mismo cruce con la otra salida— emitía prosa; y las cuatro fuentes de denegación producían cuatro formatos de prosa distintos. Nota para la sesión de construcción: rellenar el payload del evento y el `meta` del `tool_result` desde `check_permission`/`err_result` (que `check_permission` devuelva el objeto además de la razón); la emisión de `tool.end` en denegaciones ya cumple.

**Problema.** En headless con default deny (§5) la denegación de una tool call solo existe como **prosa**: el `tool_result` con `is_error` lleva el error accionable ("denegado `bash:npm install`; añade `allow = [\"bash:npm *\"]`") — perfecto para un humano, inservible para un programa. Las tres vías de observación estructurada fallan: el pipeline es deny → allow → hooks, así que un deny de política **corta antes** de llegar a los hooks `permission` (invisible para ellos); `agent:permission.asked` es solo del flujo interactivo (un deny de política no pregunta); y no está especificado siquiera si `agent:tool.end` se emite para una call denegada cuyo handler nunca corrió (ni su payload llevaría el patrón). Aflorada en la ronda 8 ([pseudocodigo.md](../validation/README.md), escenario 36): el bucle de escalado asíncrono —denegación → enmienda del Role por un humano → re-run idempotente— convierte el default deny de fricción en mecanismo, pero necesita el patrón denegado **como dato** y hoy tendría que parsearlo de la prosa.

**Impacto.** Todo orquestador headless que quiera convertir denegaciones en enmiendas de política; también auditoría y telemetría de permisos ("¿qué denegó esta sesión y por qué vía?") y cualquier UI que quiera agregar denegaciones sin re-derivarlas. La capa de permisos es de las más sensibles del contrato: mejor cerrar la observabilidad antes de congelarlo.

**Opciones.** (a) Evento de notificación `agent:permission.denied` con payload estructurado (`{ session, tool, args?, pattern, source: "deny"|"headless"|"hook" }`), simétrico de `permission.asked`; (b) `detail` estructurado en el error/`tool_result` de denegación (el patrón exacto como campo, la prosa como `message`), coherente con los errores estructurados de api.md §1.4; (c) ambas — el evento para observadores (orquestadores, UIs, telemetría) y el `detail` para el llamante que tiene el error en la mano; (d) especificar además si `tool.end` se emite en denegaciones (probablemente no: nada empezó — el evento nuevo cubre ese hueco sin ambigüedad).
