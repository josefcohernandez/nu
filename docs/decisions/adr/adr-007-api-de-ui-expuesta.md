---
title: "API de UI expuesta a Lua"
type: "adr"
id: "ADR-007"
status: "aceptada"
date: "2026-06"
---
# ADR-007 · API de UI expuesta a Lua

**Estado:** Aceptada · 2026-06 (la *validación pendiente por spike* la cerró el
spike de S28 sin ejecutar el veto: [ADR-012](#adr-012--resultado-del-spike-de-adr-007-el-toolkit-se-construye-en-lua))

**Contexto.** Si la UI de chat es una extensión (ADR-003), la API de UI debe
ser lo bastante rica para construirla entera desde Lua. Opciones evaluadas:
(A) buffers y ventanas estilo Neovim, (B) árbol de widgets retenido en el
core, (C) superficie de celdas inmediata. Análisis:

- **A (buffers)**: modelo conocido por la audiencia y buena composición, pero
  la UI de un harness no es texto plano — mapear chat, tool calls colapsables
  y diffs a buffers es la misma contorsión (extmarks, virtual text) que sufren
  los chats-en-Neovim de los que huimos. Descartada.
- **B (widgets en core)**: el mejor encaje con la UI de un harness y el mejor
  rendimiento con gopher-lua (Lua muta nodos, Go hace layout/diff/render),
  pero el mayor riesgo del proyecto: congelar mal un framework de GUI dentro
  de la API sagrada del core, y la opción más opinionada (tensión con "Lua
  puede hacer cualquier cosa").
- **C (celdas)**: API de core mínima y trivial de congelar, máxima coherencia
  filosófica, pero el peor rendimiento (Lua dentro del bucle de render, sin
  JIT) y sin composición entre plugins de serie.

**Decisión.** Síntesis B+C, en serie: cada opción neutraliza el peor defecto
de la otra.

1. **Primitiva del core: celdas + regiones + compositor en Go.** No solo "pon
   un carácter en (x,y)": regiones con z-order, blit de bloques pre-rendidos y
   damage tracking. El compositor, el diffing y el pintado viven en Go.
2. **El render caro es primitiva Go** (módulo `text`): markdown → líneas
   estilizadas, wrapping, medición de anchos. Lua coloca bloques, no celdas,
   en los caminos calientes.
3. **El toolkit de widgets es una extensión Lua oficial**, internamente
   retenida (mantiene su árbol, solo recalcula nodos sucios). Aporta slots,
   focus y composición entre plugins. Se versiona aparte del core: puede
   iterar y romperse antes de su 1.0 sin tocar la API sagrada.
4. **Coalescing en el core**: los cambios se agrupan y se repinta como mucho
   cada ~30 ms (la UI repinta por eventos, no a 60 fps).

Es el patrón de ADR-003 aplicado por segunda vez: el core no sabe lo que es
un widget; si el toolkit no se puede construir bien sobre las celdas, la
primitiva está incompleta.

**Validación pendiente (criterio de veto pre-comprometido).** Spike: primitiva
de celdas/regiones + compositor + toolkit Lua mínimo (contenedor, texto,
input, lista), torturado con dos casos: (a) streaming de tokens a pantalla
completa con markdown, (b) fuzzy picker sobre ~100k ficheros (filtrado como
primitiva Go, Lua solo repinta lo visible). Si el toolkit Lua no mantiene
ambos fluidos, **fallback**: mover la implementación del toolkit a Go (opción
B clásica) *conservando la misma API pública* de cara a las extensiones — el
diseño de la API del toolkit no se tira. Al pasar el spike, esta ADR asciende
a Aceptada.

**Consecuencias.** El éxito del ecosistema depende de que el toolkit oficial
sea bueno desde el día uno (las extensiones heredarán su calidad). La API v1
congelada es solo la pequeña (celdas/regiones/input/text). UIs alternativas
(incluso una de buffers estilo Neovim) pueden coexistir como extensiones que
compiten con el toolkit oficial. Refuerza ADR-006: la librería TUI de Go queda
como detalle de implementación del compositor.

---
