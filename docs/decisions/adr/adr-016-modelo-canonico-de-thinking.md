---
title: "Modelo canónico de `thinking` con `mode` y traducción por-modelo en el adaptador"
type: "adr"
id: "ADR-016"
status: "aceptada"
date: "2026-06"
---
# ADR-016 · Modelo canónico de `thinking` con `mode` y traducción por-modelo en el adaptador

**Estado:** Aceptada · 2026-06 (resuelve [G34](problemas.md#g34--el-modelo-canónico-de-thinking-no-expresa-el-modo-adaptativo-opus-46-400ea-con-budget_tokens); **reabre y cierra** [P21](pospuesto.md), que sale de pospuestos)

**Contexto.** El modelo canónico ([providers.md](providers.md) §2.1) congeló
`thinking?: { budget?: integer }` y el adaptador `anthropic` lo traduce a la
forma extended-thinking *legacy* `{type="enabled", budget_tokens=N}`. La familia
Opus 4.6+ —incluido el modelo por defecto del proyecto, `claude-opus-4-8`—
**retiró `budget_tokens`** y espera `{type="adaptive"}`: una petición con
`budget_tokens` sobre esos modelos devuelve **400**. La grieta no es del código
(el adaptador cumple el contrato congelado al pie) sino del **modelo canónico**,
que (1) solo sabe pedir razonamiento por *presupuesto* y (2) no tiene forma de
pedir el *modo adaptativo* que los modelos modernos exigen. Validada en
[pseudocodigo.md](pseudocodigo.md) (Ronda 7, escenario 32) y registrada como
[G34](problemas.md#g34). Estuvo pospuesta como **P21** mientras no hubo
consumidor; el disparador —el modelo por defecto ya es Opus 4.8— la reabre. Hoy
la grieta es **latente** (el agente no rellena `req.thinking` por defecto), y se
decide ahora, antes de cablear razonamiento, para no construir esa feature sobre
un canónico roto.

**Decisión.** Dos piezas, **ninguna en la API sagrada** `nu.*` (es el modelo
canónico de la extensión `providers`, no `nu.version.api`):

1. **El parámetro canónico crece por adición** a
   `thinking?: { mode?: "off" | "adaptive" | "budget", budget?: integer }`:
   - `thinking` ausente = sin razonamiento (lo de hoy).
   - `mode = "adaptive"`: razonamiento adaptativo (el modelo decide el esfuerzo).
   - `mode = "budget"` con `budget = N`: razonamiento con presupuesto de N tokens.
   - `mode = "off"`: lo desactiva explícitamente (para anular un default).
   - **Compatibilidad:** `{ budget = N }` *sin* `mode` se interpreta como
     `mode = "budget"` — la forma congelada sigue válida y significa lo mismo.
     Estricta adición; no rompe ninguna firma ni los tests grabados.

2. **El dialecto de razonamiento de cada modelo es un DATO del registro**, no
   conocimiento hardcodeado en el adaptador (ADR-005: *TOML declara los datos,
   Lua implementa el protocolo*). El `providers.toml` gana un campo opcional por
   modelo, `thinking = "adaptive" | "budget" | "none"`, que viaja en el
   `ModelInfo` (providers.md §2.1/§3). El adaptador traduce el `mode` canónico
   leyendo ese dato:
   - dialecto `"adaptive"`: `mode=adaptive` → `{type="adaptive"}`; `mode=budget`
     → también `{type="adaptive"}` (Opus 4.6+ ignora la cifra: se honra la
     intención "razona", no el presupuesto muerto).
   - dialecto `"budget"`: `mode=budget` → `{type="enabled", budget_tokens=N}`;
     `mode=adaptive` → `{type="enabled", budget_tokens=<default>}` (degrada a la
     forma que el modelo entiende).
   - dialecto `"none"` (o ausente en un modelo que no razona): no se envía
     `thinking`; si se pidió, es una **degradación declarada** (como `caps`,
     providers.md §3 obligación 5) — el adaptador no inventa.
   - `mode=off`/ausente: nunca se envía `thinking`, sea cual sea el dialecto
     (seguro en todos los modelos).
   - **Default del campo cuando falta:** `"budget"` (preserva el comportamiento
     legacy). Un modelo Opus 4.6+ se declara con `thinking = "adaptive"` en su
     entrada; omitirlo y pedir razonamiento es un **error de configuración
     accionable** (el 400 pasa a ser del `providers.toml` del usuario, que el
     mensaje nombra), no un bug del traductor.

**Razonamiento.**
- **Por qué `mode` y no reemplazar `budget`.** La superficie del modelo canónico
  crece por adición igual que la sagrada (api.md §17): romper `{budget}` rompería
  a quien ya lo usa y a los tests grabados. `mode` lo subsume (`budget` =
  `mode:"budget"`) sin romper nada.
- **Por qué el dialecto vive en el TOML y no en el adaptador.** Una tabla "qué
  familia usa qué forma" dentro del adaptador es conocimiento de producto que se
  desactualiza con cada modelo nuevo —justo lo que ADR-003/ADR-005 evitan—. Como
  dato del registro, el usuario (o el `providers.toml` distribuido) lo declara
  junto al `context` y el `max_output`, y el adaptador sigue siendo un traductor
  puro. **Descartado** inferirlo del id del modelo (`model:match("opus%-4%-[6-9]")`):
  frágil, mete una heurística de versiones en un traductor, y falla con ids no
  canónicos o gateways que renombran.
- **Por qué default `"budget"` y no `"adaptive"`.** No hay forma universalmente
  segura; el default debe preservar el comportamiento existente (modelos legacy)
  y dejar que lo nuevo se declare. El coste —una línea de TOML por modelo Opus
  4.6+— es mínimo y el error si se omite es accionable. (Descartado default
  `"adaptive"`: rompería los modelos legacy sin razón.)
- **Por qué ahora, si está latente.** Resolver el contrato es barato hoy y
  desbloquea una capacidad de primera (razonamiento con los modelos modernos);
  hacerlo después, con thinking ya cableado y consumidores que presupongan el
  canónico viejo, es caro. Es la misma economía que el resto del flujo: cerrar la
  grieta en la espec antes de construir encima.

**Consecuencias.**
- El modelo canónico puede **expresar razonamiento adaptativo**; los modelos
  Opus 4.6+ (incl. el por defecto) son usables con razonamiento sin 400.
- La superficie sagrada `nu.*` **no cambia** (es contrato de la extensión
  `providers`); `nu.version.api` igual. El `providers.toml` gana un campo
  opcional `thinking` por modelo (compatible: ausente = `"budget"`).
- **Implementación pendiente** (sesión de construcción, NO este commit, por el
  protocolo "el contrato lidera, el código sigue"): el nuevo `to_wire` del
  adaptador `anthropic`, leer `model.thinking` en `resolve`, y —cuando el agente
  exponga control de razonamiento— mapear su opción al `thinking` canónico. La
  nota `⚠` del adaptador apunta ya aquí.
- **Disparador de reapertura:** un proveedor con un tercer dialecto de
  razonamiento que `"adaptive"|"budget"|"none"` no capture (p. ej. niveles
  discretos "low/medium/high"); entonces el valor del campo se generaliza.
