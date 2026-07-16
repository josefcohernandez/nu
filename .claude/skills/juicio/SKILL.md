---
name: juicio
description: Revisión clean-room de un diff por un panel de jueces no contaminados (espec, tests, concurrencia) con verificación adversarial de cada hallazgo. Úsala antes de cerrar una sesión de implementación (siempre ANTES de escribir bitácora/puntero) o para auditar cualquier cambio sensible. La política de coste decide el tamaño del panel.
---

# Juicio clean-room

Los jueces valen porque **no comparten tus supuestos**. Todo este protocolo
existe para que sigan sin compartirlos: arrancan sin contexto de esta
conversación, reciben solo artefactos públicos (diff + espec) y su garantía
técnica es el frontmatter `tools:` sin Bash/Edit/Write (sin `git log`, sin
mensajes de commit, sin el razonamiento histórico del autor). **No relajes
ninguna de las dos cosas.**

## Regla de secuencia

El juicio se ejecuta **antes** de escribir la fila de bitácora y mover el
puntero ▶: la bitácora contiene la racionalización del autor y, si existiera
ya, contaminaría a cualquier juez que lea `implementacion.md`.

## Política de coste (cuándo montar qué)

| El diff... | Panel |
|---|---|
| Es de una sesión 🔒, o toca `docs/contracts/api.md`/contratos, o toca scheduler/cancelación/eventos/workers | **Completo**: `juez-espec` + `juez-tests` + `juez-concurrencia` (en paralelo) |
| Implementa lógica propia fuera de lo anterior | `juez-espec` + `juez-tests` |
| Es un wrapper fino sobre stdlib/librería | Solo `juez-espec` |
| Es glue de paso, docs o render visual | Ninguno (basta el DoD normal) |

Esta tabla es para no saltarse el juicio "porque es caro" justo cuando
importa. Para el panel completo puedes delegar la orquestación en el workflow
guardado `revision-limpia` (`.claude/workflows/revision-limpia.js`), que
implementa exactamente este protocolo de forma determinista.

## Plantilla del prompt de juez (literal — prohibido desviarse)

Construye el prompt de CADA juez exactamente así, sin añadir nada más:

```
Sesión: S## — <enunciado de la sesión copiado verbatim del plan, o "cambio
fuera de plan: <una frase factual de QUÉ cambia, no por qué>">
Espec que gobierna este diff: <doc> §N, <doc> §M <(+ G## que la sesión cita)>

Diff a juzgar (verbatim):
<salida de `git diff` (o `git diff --staged`) completa, sin recortar>

Emite tu veredicto con tu formato de salida.
```

**Prohibido incluir**: el razonamiento del autor, "decidimos X porque...",
alternativas discutidas, fragmentos de esta conversación, resultados de otros
jueces, o el contenido de la bitácora. Si el orquestador puede improvisar el
prompt, filtrará su propia justificación sin querer — por eso la plantilla es
literal.

## Pasos

1. Determina el panel con la política de coste. Prepara el diff
   (`git diff` del alcance de la sesión) y la lista de §N que la gobiernan
   (columna "Espec" de la S## en el plan).
2. Lanza los jueces del panel **en paralelo**, cada uno con la plantilla.
   (Excepción de entrada: a `juez-tests` puedes añadirle, tras el diff, el
   informe de mutantes LIVED de `/mutacion` — es evidencia mecánica, no
   razonamiento.)
3. Recoge los veredictos. Por **cada hallazgo** (de cualquier juez), lanza un
   `verificador` fresco con SOLO el hallazgo (título, cita de espec, línea de
   diff) + el diff — **nunca** el razonamiento del juez ni los demás
   hallazgos. En paralelo si hay varios.
4. Sintetiza:
   - `REAL` → se arregla **antes** del DoD. Si el arreglo revela que la espec
     es insuficiente → `/hallazgo` (el código nunca corrige la espec por la
     vía de hecho).
   - `FALSO POSITIVO` → se descarta, anotando la evidencia del verificador.
   - `NO CONCLUYENTE` → decide tú con el código delante, y si sigue en duda,
     escálalo al usuario. No lo entierres.
5. Reporta al usuario el resultado: hallazgos reales arreglados, falsos
   positivos descartados (con su evidencia) y veredicto final del panel.
   Después continúa el cierre normal de la sesión (bitácora, puntero, commit).

## Cuándo NO sirve este juicio

Para decisiones de **diseño** contenciosas (opciones de un G##, una propuesta
de ADR) el instrumento no es este panel sino `juez-filosofia` — y si la
decisión es de verdad reñida, varios `verificador` con lentes opuestas sobre
la opción favorita. El juicio clean-room juzga código contra espec congelada.
