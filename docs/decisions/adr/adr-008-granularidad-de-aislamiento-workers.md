---
title: "Granularidad de aislamiento: workers por tarea, estado principal compartido"
type: "adr"
id: "ADR-008"
status: "aceptada"
date: "2026-06"
---
# ADR-008 · Granularidad de aislamiento: workers por tarea, estado principal compartido

**Estado:** Aceptada · 2026-06

**Contexto.** Con ADR-004 decidido, queda la pregunta fina: ¿el aislamiento es
opt-in por tarea (todas las extensiones comparten el estado principal y lanzan
workers efímeros cuando lo necesitan) o por plugin (cada extensión vive
permanentemente en su propio actor)? Afecta a: composabilidad entre plugins
(que se requieran unos a otros), contención de fallos, latencia de hooks
síncronos de UI y complejidad de la API.

**Decisión.** Per-task: todos los plugins comparten el estado principal por
defecto; el aislamiento es opt-in por tarea vía `worker.spawn()`. Con tres
reglas:

1. **Los workers no tienen acceso al módulo `ui`.** La pantalla solo se pinta
   desde el estado principal (como los Web Workers respecto al DOM). El worker
   devuelve resultados por mensaje y el estado principal actualiza la UI.
2. **Watchdog con cancelación.** Cada handler en el estado principal tiene un
   presupuesto de tiempo; si lo excede, el core lo aborta vía cancelación por
   contexto de gopher-lua y marca el plugin como sospechoso/deshabilitable.
3. **`pcall` en cada frontera de hook.** Un error en un plugin nunca tumba el
   event loop ni afecta a los demás plugins.

Los mensajes entre worker y estado principal son **copias** (las tablas Lua no
cruzan estados): un worker debe devolver resultados digeridos, no datos crudos
masivos.

**Razonamiento.**
- La composabilidad es el ingrediente secreto del ecosistema Neovim: plugins
  que se `require` entre sí, librerías-plugin (plenary), extensiones de
  extensiones (telescope). Con actores aislados, "usar otro plugin" sería RPC
  asíncrono con serialización — no se pueden pasar closures por un channel — y
  ese ecosistema no puede nacer.
- Los hooks síncronos (keymaps, render) necesitan respuesta inmediata; con
  actores serían round-trips bloqueantes con riesgo de deadlock, o todo hook
  se volvería async.
- Actores por plugin: N estados = N stdlibs en memoria, copias en cada
  frontera, API más difícil para el plugin de 20 líneas.
- El watchdog + pcall cubren la mayor parte del hueco de robustez: contención
  de errores y de bucles infinitos (más de lo que Neovim ofrece de serie).

**Consecuencias.** Riesgos aceptados conscientemente: un memory leak en un
plugin infla el proceso entero, y el watchdog no protege de la "muerte por mil
cortes" (muchos handlers lentos pero bajo presupuesto). Los actores por plugin
quedan como posible evolución futura (p. ej. `isolated = true` en el manifest
para plugins no confiables), pero no en v1: dos modos de ejecución duplican la
semántica de cada hook. La regla workers-sin-UI simplifica ADR-007: solo el
estado principal pinta, así que el modelo de UI no necesita ser thread-safe ni
multiplexar autores concurrentes.

---
