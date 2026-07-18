---
title: "El wizard de `enu init` ofrece cuatro providers pero solo `anthropic` tiene plantilla: la espec presupone plantillas de config que no existen"
type: "hallazgo"
id: "G61"
status: "resuelto"
date: "2026-07-18"
origin: "escenarista BDD de la sesión S49 (enu init)"
affected: ["adr-026 §pieza 2", "agente.md / providers.md (plantillas)", "S49"]
---
# G61 · El wizard de `enu init` ofrece cuatro providers pero solo `anthropic` tiene plantilla — ADR-026 pieza 2

**Problema.** [ADR-026](../decisions/adr/adr-026-subcomandos-de-gestion-del-binario.md)
pieza 2 especifica que el asistente TTY de `enu init` ofrece elegir entre
**cuatro** providers —`anthropic | openai-compat | gemini | ollama`— y luego
«propone el modelo del provider elegido» y escribe `providers.toml`. Pero solo
`anthropic` tiene **plantilla** definida: [ADR-017](../decisions/adr/adr-017-el-onramp-deja-config.md)
fija su `base_url`, `api_key_env = "ANTHROPIC_API_KEY"`, modelo
(`claude-opus-4-8`), `context` y `thinking`. Para los otros tres **no hay
plantilla especificada** en ningún contrato:

- **openai-compat / gemini**: sin `base_url` por defecto, sin convención de
  `api_key_env` (`OPENAI_API_KEY`? `GEMINI_API_KEY`?), sin modelo ni `context`
  de referencia.
- **ollama**: además **rompe el propio flujo del wizard** — es local y
  normalmente **no usa API key**, así que el paso «clave por variable de
  entorno» de la pieza 2 no le aplica.

La propia ADR-026 lo delata: «modelo → propone el del provider elegido; **para
anthropic**, la plantilla de ADR-017» — nombra los cuatro como opción pero solo
tiene diseño para uno. El escenarista BDD de S49 no pudo escribir los
escenarios del wizard para tres de las cuatro ramas sin inventar contrato: la
espec presupone plantillas que no existen (el patrón clásico de `G##`).

**Impacto.** S49 (`enu init`) no se puede implementar tal como está escrito: el
wizard multi-provider necesitaría inventar `base_url`/`api_key_env`/modelo/
`context` para tres providers y una excepción para el flujo sin-key de ollama —
diseño de contrato de providers que no cabe de rebote en una sesión de código.

> ✅ **RESUELTO (2026-07-18) — opción (a): el wizard v1 ofrece solo
> `anthropic`.** Elegida por el operador. Se estrecha la pieza 2 de ADR-026: el
> asistente de `enu init` v1 ofrece **únicamente `anthropic`** (el default del
> producto, coherente con ADR-017, que ya solo tiene plantilla anthropic y con
> el modelo por defecto del proyecto, `claude-opus-4-8`, ADR-016). El resto del
> wizard (clave por `api_key_env`, modelo propuesto, activar el conjunto
> oficial, semántica por-fichero, no-op honesto, códigos de salida) queda
> intacto. El dispatch de subcomandos (pieza 1) y las piezas 3-5 no se tocan.
>
> **Los otros tres providers se difieren** como
> [P44](../postponed/pospuesto.md): reabrir cuando se especifiquen sus
> plantillas (base_url, convención de `api_key_env`, modelo/`context` por
> defecto) y cómo encaja el flujo sin-key de ollama. No es una grieta de la
> API sagrada (`enu.*` intacto): es superficie CLI + contrato de providers.
>
> **Aplicación:** nota de estrechamiento en ADR-026 pieza 2 (con puntero a
> este hallazgo, sin reescribir la decisión); P44 registrado con disparador; la
> fila de S49 acota el wizard a anthropic. La equivalencia sin-TTY ≡
> `--default-config` no se ve afectada (ambos ya usaban la plantilla anthropic).

**Disparador de reapertura.** — (resuelto). El wizard multi-provider revive con
P44, cuando exista el diseño de las plantillas de los otros providers.
