---
name: mutacion
description: Mutation testing con gremlins sobre los ficheros de una sesión o los paquetes 🔒 de una fase — el juez de tests mecánico y no contaminado por definición. Úsala antes de cerrar una sesión 🔒 o como pasada periódica de endurecimiento. Nunca sobre internal/vmwasm, nunca como dependencia de go.mod, nunca como gate de CI.
---

# Mutation testing (gremlins)

Convierte "creo que a la suite le falta un caso límite" en "este mutante
concreto sobrevivió": gremlins introduce fallos artificiales (invierte una
condición, cambia un `+` por `-`, un `<` por `<=`) y ejecuta los tests; si
ninguno falla, esos tests no muerden. Complementa el inventario 🔒 — la
cobertura mide qué líneas se ejecutan; la mutación mide si los tests
**detectan** que están rotas.

## Instalación (pineada, fuera del proyecto)

```sh
go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0
```

**Nunca en `go.mod`** (cero dependency hell, ADR-001): es un binario externo
de desarrollo. Versión pineada, no `@latest` — gremlins es 0.x sin garantía de
compatibilidad; si se sube de versión, se prueba primero y se actualiza este
fichero.

## Ejecución

Siempre **acotada**: `internal/runtime` es un único paquete Go enorme, y el
módulo entero tiene >1000 mutantes cubiertos a ~20 s de suite por mutante —
horas. El alcance se recorta con `-E` (regexp de exclusión) hasta dejar solo
los ficheros de la sesión, o con `-D <rama>` para mutar solo el diff.

```sh
# Modo sesión: solo los ficheros tocados (ejemplo: diff.go)
gremlins unleash \
  -E 'spike/.*' -E 'web/.*' -E 'internal/vmwasm/.*' -E 'main\.go' \
  -E 'internal/runtime/[a-ce-z].*' -E 'internal/runtime/d(efault_config|river).*' \
  --timeout-coefficient 300 \
  --output <scratchpad>/gremlins.json \
  .

# Modo diff: solo las líneas que cambia la rama actual frente a main
gremlins unleash -D main --timeout-coefficient 300 -E 'internal/vmwasm/.*' .
```

Reglas operativas:

- **`--dry-run` primero, siempre**: cuenta los mutantes RUNNABLE y permite
  estimar el coste (mutantes × duración de suite / workers) antes de lanzar.
- **`--timeout-coefficient 300` es imprescindible aquí**: gremlins calcula el
  timeout por mutante a partir de la duración base de `go test`, y la caché de
  Go la hace parecer de milisegundos → todos los mutantes saldrían TIMED OUT
  falsamente (pasó en el piloto de 2026-07).
- **Exclusión dura de `internal/vmwasm`**: cada mutante recompila y re-ejecuta
  la suite; con wazero es prohibitivo (misma razón por la que CI lo separa).
- Ejecuta desde la raíz del módulo. `spike/` es otro módulo: exclúyelo.

## Interpretación

- **KILLED** — la suite muerde. Nada que hacer.
- **LIVED** — el mutante sobrevivió. Diagnostica cada uno: (a) **hueco real**
  → escribe el test que lo mata (o pásalo como evidencia a `juez-tests`
  dentro de `/juicio`: es entrada mecánica permitida); (b) **mutante
  equivalente** (el cambio no altera el comportamiento observable) → anótalo
  en la fila de bitácora de la sesión para que nadie lo re-investigue.
- **NOT COVERED** — ninguna prueba ejecuta esa línea. Contrasta con la
  política de tests: puede ser wrapper fino legítimo sin unitario, o puede ser
  lógica propia desnuda (entonces es un defecto de la suite, no del mutante).
- **TIMED OUT** — sospecha primero del coeficiente de timeout (ver arriba);
  solo después de un mutante que realmente cuelga (un lazo infinito
  introducido por la mutación cuenta como detectado).

## Cuándo se ejecuta (y cuándo no)

1. **Al cerrar una sesión 🔒** — paso 4 de `/sesion`, antes del juicio,
   acotada a los ficheros de la sesión.
2. **Endurecimiento periódico** — al cierre de una fase (checkpoint 🔎) o
   bajo demanda, sobre los paquetes del inventario 🔒; los LIVED alimentan a
   `juez-tests`.

**Nunca como gate de CI**: el coste es O(mutantes × suite) — multiplica la
suite por 10-100×, cuando el protocolo depende de un feedback loop de minutos;
los timeouts por mutante meten flakiness que convertiría el gate en ruido; la
tool es 0.x (un upgrade de Go podría romper CI sin que el código cambie); y el
resultado es **diagnóstico** (informa el diseño de tests), no un semáforo
binario. Si algún día se quiere en GitHub Actions, será un workflow
`workflow_dispatch` manual separado, jamás per-PR.
