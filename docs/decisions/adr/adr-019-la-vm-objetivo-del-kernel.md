---
title: "La VM objetivo del kernel es PUC-Lua sobre wazero; gopher-lua queda en mantenimiento"
type: "adr"
id: "ADR-019"
status: "aceptada"
date: "2026-07"
---
# ADR-019 · La VM objetivo del kernel es PUC-Lua sobre wazero; gopher-lua queda en mantenimiento

**Estado:** Aceptada · 2026-07 (dirección; la ejecución es por fases, sin fecha comprometida. Basada en el spike [spike/lua-wasm/INFORME.md](../spike/lua-wasm/INFORME.md); relacionada con ADR-011 —que la fase de migración reemplazará— y con [G31](problemas.md#g31)/[G41](problemas.md#g41))

**Contexto.** gopher-lua —la VM sobre la que corre todo el Lua de nu— está sin
mantenimiento efectivo: la v1.1.2 pineada es su última release, `state.go` no se
toca desde diciembre de 2023, y el bug de G41 lleva reportado aguas arriba desde
2023 sin respuesta (issue #448). El kernel ya carga dos cicatrices estructurales
suyas: el scheduler sin yields de ADR-011 (porque sus corrutinas no ceden a
través de `pcall`, G31) y el blindaje de G41 (que escribe un campo no exportado
de la dependencia vía `unsafe`). El riesgo no es agudo —el statu quo funciona,
blindado y con tests— pero es compuesto: cada sesión de construcción añade
código sobre una base huérfana, y el coste de salir crece con el tiempo. Un
spike en rama aparte (`spike/lua-wasm`) midió la alternativa: el **Lua oficial
de PUC (5.4.7, sin un solo parche)** compilado a WebAssembly y ejecutado sobre
**wazero** (runtime WASM en Go puro, mantenido y con respaldo industrial;
`CGO_ENABLED=0` intacto), con el desenrollado de Lua realizado mediante un
trampolín sobre la API `Snapshot/Restore` de wazero (sin setjmp/longjmp, sin
emscripten, sin asyncify). Resultados: **semántica de referencia** (la repro de
G41 devuelve `42`; `coroutine.yield` cruza `pcall` — la limitación que motivó
ADR-011 no existe), **VM pura igual o más rápida** que gopher-lua (fib 0,8×,
tablas 0,41×), peajes concentrados en las fronteras (llamada host ~1 µs, throw
~40 µs, yield 26-192 µs — ver §4 del informe), y ~+0,7 MB de binario.

**Decisión.** Cinco piezas:

1. **Dirección comprometida:** la VM objetivo del kernel es PUC-Lua sobre
   wazero. gopher-lua pasa a **modo mantenimiento**: pin en v1.1.2, los
   blindajes existentes se conservan, y no se construye NADA nuevo que dependa
   de sus internos más allá de lo ya presente.
2. **Baseline de lenguaje: Lua 5.4.** En la migración, api.md §1.2 pasará de
   "Lua 5.1 (gopher-lua)" a "Lua 5.4 (PUC)" — cambios menores
   (`unpack`→`table.unpack` y análogos) en zonas que el sandbox ya curaba. Se
   decide ahora para que ningún código nuevo se apoye en particularidades de
   5.1 que no viajen.
3. **El puente ⏸ será por corrutinas nativas** (`lua_yieldk`; el yield cruza
   `pcall` en el Lua real): el diseño que ADR-011 quiso y G31 vetó. Cuando la
   migración se ejecute, su ADR de diseño **reemplazará** a ADR-011 (que no se
   reescribe, como manda el flujo).
4. **Ejecución por fases, sin fecha:** (a) la **interfaz de VM** en el kernel
   como sesión propia — extraer tras una interfaz Go las operaciones que el
   kernel usa de la VM; barata, sin compromiso, mejora el kernel aunque la
   migración no llegara; (b) el **ADR de diseño del puente definitivo**, que
   debe resolver la luz ámbar del coste del yield (el `Snapshot` clona la pila
   del motor; vías en el informe §4.1) ANTES de migrar; (c) la **migración por
   fases** contra la suite de conformidad existente (G31/G41, watchdog,
   cancelación, workers), estimada en 10-15 sesiones. El disparador de
   aceleración: si gopher-lua vuelve a morder con algo no blindable desde el
   kernel, la migración pasa a ser la siguiente fase de construcción.
5. **Anti-caducidad del spike:** `spike/lua-wasm` (build.sh + tests + benchs)
   se mantiene reproducible; un upgrade de wazero que cambie la API
   experimental de snapshots debe detectarse allí antes que en el kernel.

**Consecuencias.**

- La clase entera de bugs "la reimplementación diverge de la referencia" (G31,
  G41) se vuelve imposible por construcción en el destino; mientras tanto, el
  statu quo blindado sigue siendo operable.
- La memoria lineal por instancia de WASM abre **aislamiento físico** para
  workers y `caps` — [P2](pospuesto.md) (actores aislados) y [P3](pospuesto.md)
  (plugins WASM) ganan un camino natural y barato el día de la migración.
- Costes asumidos: ~+0,7 MB de binario; fronteras más caras (irrelevantes con
  el patrón "Lua decide, Go ejecuta", que ya minimiza cruces); una API
  experimental vigilada por el spike; y la ruptura menor y anunciada del
  baseline 5.1→5.4 en api.md §1.2 cuando toque.
- Los autores de plugins **no notan nada**: mismo Lua, misma API `nu.*`, mismos
  contratos — la API sagrada es exactamente la capa que hace posible cambiar el
  motor sin tocar el ecosistema (ADR-003, dogfooding estructural).
