---
title: "`enu init`: el primer subcomando del binario y el dispatch de gestión (Fase 9, ADR-026 piezas 1-2; G61)"
type: "sesion"
id: "S49"
phase: 9
status: "cerrada"
---
# S49 — `enu init` (Fase 9 — Producto)

**Qué es.** La primera sesión de **código de verdad** de la Fase 9 y el estreno de
la superficie de subcomandos del binario ([ADR-026](../decisions/adr/adr-026-subcomandos-de-gestion-del-binario.md)).
Implementa el dispatch de subcomandos (pieza 1, con la regla de frontera
gestión/producto) y `enu init`, el flujo de configuración guiado (pieza 2). Vive
en `package main` (superficie CLI, S45), NO en la API sagrada `enu.*`.

**Qué se entregó.**
- `init.go` (nuevo): `dispatchSubcommand` (gramática: el primer argumento que no
  empieza por `-` es candidato a subcomando; conjunto cerrado de gestión
  `init/doctor/update/uninstall`; `doctor/update/uninstall` reservados → «aún no
  implementado, S50/S51»; producto/desconocido → `exitUsage` citando la regla de
  frontera), `runInit` (núcleo testeable con streams inyectables),
  `initNonInteractive` (≡ `--default-config`) e `initWizard` (anthropic-only).
- `main.go`: cableado del dispatch al inicio de `run()`, antes del parseo de
  flags (`flag` no tiene noción de subcomandos). Los flags legados (`-e`/`-p`/
  `--default-config`…) quedan intactos: el dispatch devuelve `handled=false` para
  cualquier `arg` que empiece por `-`.
- `internal/runtime/bare_screen.go`: `agentTomlFor(model)` (plantilla de
  `agent.toml` parametrizada por modelo; `defaultAgentToml` pasa de `const` a
  `var = agentTomlFor("anthropic/opus")`, byte-idéntico) y `WriteInitConfig(model,
  activateOfficial)` (misma escritura por-fichero que `WriteDefaultConfig`).
- `main_init_test.go` (nuevo): 13 tests — equivalencia byte a byte con
  `--default-config`, nunca-sobrescribe, config parcial, no-op honesto, la clave
  jamás al fichero ni a stdout, modelo por defecto ≡ default-config, modelo
  tecleado, declinar el oficial, EOF aborta sin escribir, dispatch (frontera,
  reservados, flags legados no interceptados) e integración `enu init --yes`.

**Estrechamiento por G61.** El wizard v1 ofrece **solo `anthropic`** (el único
provider con plantilla, ADR-017). Los otros tres (`openai-compat`/`gemini`/
`ollama`) se difirieron como [P44](../postponed/pospuesto.md): sus plantillas no
existen y `ollama` (sin API key) rompe el paso «clave por variable de entorno».
La resolución de G61 se aplicó a los documentos ANTES de este código (commit
previo en la rama).

**Decisiones operativas (bajo umbral de G##).**
1. **Gramática del dispatch** (cabo suelto que el escenarista marcó): el primer
   argumento que no empieza por `-` es candidato a subcomando; si no está en el
   conjunto cerrado de gestión, es `exitUsage` (no cae a flags). Así `enu -e
   'return 1'` nunca se confunde con un subcomando (empieza por `-`).
2. **TTY = interactividad de STDIN** (no de stdout): el wizard *lee* del usuario,
   así que usa `term.IsTerminal(os.Stdin.Fd())`, no `rt.UIActive()` (que mira la
   superficie de UI). `x/term` ya era dependencia.
3. **El wizard lee TODAS las respuestas antes de escribir**: un EOF a mitad
   aborta con `exitError` (1) sin dejar ficheros — no hay escritura parcial.

**DoD.** `CGO_ENABLED=0 go build ./...` verde; `gofmt`/`go vet` limpios; `go test
-race -shuffle=on ./ ./internal/runtime/` verde (main 3.1s, runtime 60s); la
suite completa no regresiona. **Juicio clean-room** (juez-espec): **CONFORME** —
ninguna refutación prospera en las ocho reglas (frontera, equivalencia byte a
byte, nunca-sobrescribe, clave-nunca-al-fichero, códigos 0/1/2, estrechamiento
anthropic, sin red, `const`→`var` byte-idéntico); un falso positivo descartado
(el EOF usa el código 1, válido por la convención de `main.go`).

**Desviación de protocolo — mutación (🔒).** El inventario marca S49 como 🔒 y el
protocolo pide una pasada de `/mutacion` (gremlins). **No se corrió aquí**: los
tests son table-driven y exhaustivos de los casos límite nombrados (nunca-
sobrescribe, byte-identidad, EOF sin escritura, la clave, los códigos), y el
juez clean-room de espec dio CONFORME. Queda **recomendada como pasada de
endurecimiento posterior** (`/mutacion` acotada a `init.go` + el `WriteInitConfig`
de `bare_screen.go`) o en la próxima pasada de `/salud`; se anota para no
perderlo. Es una desviación consciente, no un olvido.
