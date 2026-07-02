# Persistencia de sesiones

Estado: **borrador para discusión**. Contrato de almacenamiento de la
extensión oficial del agente — no es API sagrada del core ([api.md](api.md)).
Se documenta como **convención pública**: cualquier extensión o herramienta
externa puede leer sesiones (pickers, exportadores, estadísticas de coste)
sin pasar por el agente.

## 1. Principios

1. **Append-only.** Una sesión es un fichero al que solo se añaden líneas,
   nunca se reescribe. A prueba de crashes (lo escrito, escrito está), barato
   en sesiones largas (no se reserializa el historial en cada turno) y
   trivial de seguir en vivo (`tail -f`).
2. **El estado se reconstruye por replay.** Reabrir una sesión = leer el
   fichero de arriba abajo aplicando cada entrada. No hay segundo fichero de
   "estado actual" que pueda desincronizarse.
3. **Reutiliza el modelo canónico.** Los mensajes se serializan exactamente
   como los define [providers.md](providers.md) (bloques, `meta` incluido):
   una sesión reanudada produce requests idénticos a los de la original.
4. **El core solo aporta `nu.fs` y `nu.json`.** Nada de esto es primitiva.

## 2. Ubicación

```
nu.config.data_dir()/
  sessions/
    <proyecto>/                          # cwd codificado como slug
      2026-06-11T10-22-07Z-a3f9.jsonl    # una sesión = un fichero
  plugins/
    <nombre-plugin>/                     # almacenamiento privado de cada plugin
```

- Agrupación **por proyecto** (slug del `cwd`): "continuar la última sesión
  de este repo" es un listado de directorio.
- **El slug es parte del formato (G38).** Como este contrato promete lectura
  por herramientas externas (§1), la codificación cwd→directorio no puede ser
  un detalle privado. Algoritmo: todo carácter fuera de `[A-Za-z0-9.-]` se
  sustituye por `_`; se recortan los `_` de ambos bordes; si queda vacío,
  `"root"`. Ejemplo: `/home/diego/nu` → `home_diego_nu`. Es deliberadamente
  **legible y con pérdida**: no es reversible, y dos `cwd` patológicamente
  parecidos (`/a/b` y `/a_b`) pueden colisionar en el mismo directorio. No es
  una identidad sino una **clave de agrupación**: la identidad canónica de
  cada sesión viaja *dentro* del fichero (la línea `meta` lleva `cwd` e `id`)
  — quien necesite desambiguar una colisión, lee `meta`. Para que ningún
  plugin reimplemente la codificación, la extensión la expone como funciones
  puras: `sessions.slug(cwd) -> string` y `sessions.dir(cwd) -> string`
  (`data_dir()/sessions/<slug>`); las herramientas externas la componen desde
  esta especificación.
- Nombre de fichero = id de sesión: timestamp UTC + sufijo aleatorio.
  Ordenación lexicográfica = ordenación temporal.
- Permisos `0600`: los transcripts contienen código y salidas de comandos.
- Regla general para las demás extensiones: cada plugin escribe solo bajo
  `plugins/<su-nombre>/`. `sessions/` es la única convención compartida.

## 3. Formato: JSONL de entradas

Una entrada por línea. Toda entrada tiene `t` (tipo) y las de actividad
llevan `ts` (epoch ms). Tipos v1:

```
{ "t": "meta",    "v": 1, "id", "cwd", "created", "parent"? }
{ "t": "message", "ts", "message": Message, "usage"?, "model"? }
{ "t": "compact", "ts", "summary": Message, "covers": integer }
{ "t": "event",   "ts", "ns": string, "data": any }
```

- **`meta`**: siempre la primera línea. `v` es la versión del formato.
  `parent? = { id, entry }` enlaza forks (ver §5).
- **`message`**: un `Message` canónico completo (rol + bloques, con `meta` de
  bloques intacto). En los de rol `assistant`, `usage` (el evento del
  proveedor) y `model` quedan adjuntos: el coste y el llenado de contexto se
  auditan leyendo el fichero.
- **`compact`**: la compactación no borra historia. `summary` es el mensaje
  resumen y `covers` el número de entradas `message` que sustituye. En
  replay para el LLM: se toma el último `compact` y los `message` que lo
  siguen; todo lo anterior queda en el fichero para los ojos humanos y las
  herramientas.
- **`event`**: escape genérico namespaced para todo lo demás (cambio de
  modelo a mitad de sesión, título, marcas de usuario). Regla de replay:
  para datos repetibles (p. ej. título), la última gana. Extensiones de
  terceros usan su nombre de plugin como `ns`.

Robustez de lectura: una última línea truncada (crash a mitad de escritura)
se descarta en silencio. Líneas con `t` desconocido se ignoran (forward
compatible: versiones nuevas pueden añadir tipos).

## 4. Streaming y atomicidad

Durante el streaming de una respuesta no se escribe nada: los deltas son
para la pantalla. Al completarse el turno (`done` del adaptador), se hace
**un** `nu.fs.append` con la entrada `message` entera. Una sesión nunca
contiene mensajes a medias; si el proceso muere a mitad de respuesta, el
turno simplemente no existe (y la petición se puede relanzar al reanudar).

## 5. Forks y rewind

Rebobinar a un punto anterior y probar otro camino **no muta el fichero**
(append-only): crea una sesión nueva cuyo `meta.parent` apunta a la sesión
de origen. **El fork copia el prefijo al transcript de la hija (G39)**: la
sesión hija es **autocontenida** — su replay no sigue la cadena de padres, y
su fichero viaja solo (lo que hace triviales exportar un fork o moverlo
entre máquinas: el formato es la API, [P9](pospuesto.md)). El coste de
duplicar el prefijo es irrelevante frente a esa robustez. `meta.parent =
{ id, entry }` es **navegacional**, no un puntero de replay: sirve para
reconstruir el árbol de variantes leyendo los `meta`; `entry` es el índice
de mensaje del historial vigente del padre en el momento del fork (la
unidad de `Session:fork(at)`, [agente.md](agente.md) §2). El historial
original queda intacto.

## 6. Concurrencia: un escritor por sesión (G5)

Dos procesos haciendo append al mismo JSONL = corrupción intercalada. Regla:
**una sesión tiene como máximo un escritor**, garantizado por lockfile.

- `<sesión>.jsonl.lock` junto al transcript, contenido
  `{ pid, hostname, started }`. Se adquiere al abrir para escribir
  (crear/reanudar) con creación **exclusiva**
  (`nu.fs.write(..., { exclusive = true })`, atómica: dos procesos no
  pueden ganar a la vez — [api.md](api.md) §5), se libera al salir. La
  identidad del escritor que se graba es la del proceso `nu` actual: el
  `pid`, de `nu.sys.pid()` (G32); el `hostname`, de `nu.sys.hostname()`
  (G17); el `started`, de `nu.sys.now_ms()`. Al *verificar* un lock ajeno se
  comprueba su `pid` con `nu.proc.alive` (existencia en esta máquina, no
  identidad — G17). **Leer nunca requiere lock** (un append-only es seguro
  de leer a medias).
- **Lock huérfano** (crash): si el `pid` no está vivo en esta máquina, es
  basura — se limpia en silencio. Si el lock es de otro `hostname`
  (directorio sincronizado), no se puede verificar: se pregunta, nunca se
  asume.
- **Conflicto real** (pid vivo): el segundo proceso recibe aviso claro con
  tres salidas — **fork** (por defecto: continúa en rama nueva vía
  `meta.parent`, §5, sin pisar a nadie), **solo lectura**, o **forzar**
  (robar el lock, explícito y con confirmación).
- Se eligió lockfile sobre `flock` del SO por semántica predecible en
  Windows y filesystems de red; el auto-fork silencioso se descartó por
  bifurcar el historial sin conocimiento del usuario.

## 7. Listado y reanudación

- Listar sesiones de un proyecto = listar `sessions/<proyecto>/` y leer la
  primera línea (`meta`) y la última relevante (título/timestamp) de cada
  fichero. Sin índice global en v1: si algún día duele, se añade un índice
  *reconstruible* (caché, nunca fuente de verdad).
- Subagentes (corran como task o como worker): su transcript es una sesión
  propia con
  `meta.parent` apuntando a la entrada del padre que los lanzó — misma
  mecánica que los forks, auditable con las mismas herramientas.

## 8. Lo que queda fuera (v1)

- Cifrado en reposo y redacción de secretos en tool results: el transcript
  es fiel; protegerlo es trabajo del sistema de ficheros (`0600`).
- Sincronización entre máquinas e índices de búsqueda: construibles encima
  por extensiones (el formato es la API).
- Garbage collection de sesiones viejas: política de la extensión del
  agente (configurable), no del formato.
