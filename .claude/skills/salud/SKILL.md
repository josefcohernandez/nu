---
name: salud
description: Pasada de salud del repo — la capa mecánica de detección de fallos que complementa las verificaciones habituales (CI, DoD, juicio). Cuatro detectores que no alucinan, fuzzing con corpus acumulativo, estrés del race detector, govulncheck y rotación de mutación. Úsala periódicamente (semanal/quincenal) o cuando lleve tiempo sin correrse; termina con una fila en su bitácora.
---

# Pasada de salud (capa mecánica)

Cuatro detectores mecánicos: reproducibles, sin falsos positivos de LLM, y
cada uno caza una familia de fallos que las verificaciones habituales (CI por
push, DoD por sesión, juicio por diff) no cubren. El principio: **el
calendario solo es buen trigger para lo que cambia aunque el código no cambie**
(el corpus de fuzzing crece, las CVEs se publican, los interleavings son
loterías); para lo demás el trigger es el cambio (eso ya lo hacen CI y
`/juicio`).

Ejecuta los cuatro pasos; los largos, en paralelo/segundo plano. Presupuesto
orientativo total: 30-45 min de máquina, casi todo desatendido.

## 1 · Fuzzing (dianas en `internal/runtime/fuzz_test.go`)

Seis dianas con invariantes fuertes sobre la lógica 🔒: `FuzzSSEChunkSplit`
(S20, equivalencia bajo partición de chunks), `FuzzDecodeInputChunkSplit`
(CP-7, disciplina pending/flush del driver), `FuzzComputeDiffReconstruct`
(S25, aplicar los hunks reconstruye `b`), `FuzzWrapText` y `FuzzTruncateText`
(S22, anchura acotada + contenido conservado), `FuzzMarkdownStreamingSafe`
(S23, prefijos arbitrarios no rompen).

```sh
for t in FuzzSSEChunkSplit FuzzDecodeInputChunkSplit FuzzComputeDiffReconstruct \
         FuzzWrapText FuzzTruncateText FuzzMarkdownStreamingSafe; do
  go test ./internal/runtime/ -run "^$t$" -fuzz "^$t$" -fuzztime 3m
done
```

- El corpus vive en `$GOCACHE/fuzz` y **se acumula entre pasadas**: cada
  ejecución parte de donde llegó la anterior — el tiempo invertido se compone.
- Un fallo deja un caso mínimo en `internal/runtime/testdata/fuzz/<diana>/`:
  **commitéalo** (se convierte en test de regresión permanente que CI ejecuta
  como semilla) y trata el fallo como hallazgo: ¿bug de código (→ arreglar
  citando la diana) o invariante mal formulado en la diana (→ corregir la
  diana explicando por qué)?
- Si una sesión nueva añade lógica 🔒 parseadora o con bordes, añade su diana
  aquí; el inventario de dianas crece como el 🔒: nunca se relaja.
- CI solo ejecuta las semillas (un `go test` normal no fuzzea): el coste del
  fuzzing vive en esta skill, no en cada push.

## 2 · Estrés del race detector

El CI corre `-race -shuffle=on` una vez; una sola ejecución es una lotería de
interleavings. La pasada de salud compra más boletos:

```sh
go test ./internal/runtime/ -race -shuffle=on -count=10 -timeout 30m
```

(`internal/vmwasm` queda fuera por el OOM de wazero+race — mismo régimen que
CI; su job dirigido ya cubre lo suyo.) Un fallo aquí es oro: guarda el seed
del shuffle que imprime el log (`-shuffle=on` lo reporta) para reproducir.

## 3 · govulncheck (CVEs en dependencias)

```sh
go install golang.org/x/vuln/cmd/govulncheck@v1.1.4   # pineado, nunca en go.mod
"$(go env GOPATH)/bin/govulncheck" ./...
```

Analiza qué funciones vulnerables se **alcanzan** desde el código (no solo qué
módulos aparecen en go.mod), así que un aviso suyo es accionable casi siempre:
subir la dependencia (cero dependency hell no es cero updates) o documentar
por qué no aplica. Este es el paso donde el calendario es exactamente el
trigger correcto: las CVEs llegan solas.

## 4 · Rotación de mutación

Una pasada de `/mutacion` sobre **un** conjunto acotado del inventario 🔒 —
el siguiente en la rotación según la última fila de la bitácora de abajo (no
repitas el último). Los LIVED se diagnostican como manda la skill: test nuevo
o mutante equivalente anotado.

## Cierre: la bitácora de salud

Añade una fila a `.claude/skills/salud/bitacora.md` (append-only, la más
reciente abajo): fecha, qué corrió (fuzztime, count, paquete mutado), qué
encontró y qué se hizo. Es lo que permite rotar la mutación, no re-diagnosticar
mutantes equivalentes conocidos y ver si el fuzzing sigue encontrando corpus
nuevo (si lleva 3 pasadas sin "new interesting", sube el fuzztime o añade
dianas).

Si algo encontrado exige tocar código con lógica: eso es trabajo de sesión
(`/planificar-sesion` si es feature, o arreglo directo con `/juicio` si es
bug), no de esta skill. La salud detecta; no opera.
