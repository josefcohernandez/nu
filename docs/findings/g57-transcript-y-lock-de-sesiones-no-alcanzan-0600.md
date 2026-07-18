---
title: "El transcript y el lock de sesiones no alcanzan el 0600 que sesiones.md promete: la API no deja fijar el modo de creación"
type: "hallazgo"
id: "G57"
status: "resuelto"
date: "2026-07-18"
origin: "suite e2e de los plugins oficiales (aserción de permisos del transcript)"
resolution: "enu.fs.write gana opts.mode (chmod explícito no recortado por el umask, componible con exclusive); sessions crea transcript y lock con mode=0600; append preserva el modo del fichero existente. Nivel de API 4→5."
affected: ["api.md §5", "api.md §17", "sesiones.md §2/§6/§8", "guia-plugins.md §7", "implementacion.md (inventario 🔒)"]
---
# G57 · El transcript y el lock de sesiones no alcanzan el 0600 que `sesiones.md` promete: la API no deja fijar el modo de creación — `api.md` §5 / `sesiones.md` §2/§6/§8 — **RESUELTO**

**Resolución** (2026-07-18; adición a [api.md](../contracts/api.md) §5, nivel de
API 4→5). `enu.fs.write(path, data, opts?)` gana `opts.mode?: number`: el modo de
creación del fichero, aplicado con un **chmod explícito NO recortado por el umask**
(el SO tamiza por el umask el `perm` de `open`, pero no un `chmod` posterior, que
fija los bits tal cual). Es **componible con `opts.exclusive`**: el camino
exclusivo (`O_EXCL`, la pieza de los lockfiles, G17) crea y luego hace el chmod; el
camino atómico (temporal + rename) hace el chmod sobre el temporal antes del rename,
de modo que el fichero final nunca existe ni un instante con permisos distintos. Un
`mode` explícito en una **sobrescritura** gana sobre la preservación del modo previo
del destino. El valor se valida como entero de permisos `0..0o777`; no entero,
negativo o fuera de rango → `EINVAL` (un modo mal puesto es un error de
programación, no un default silencioso). La adición no toca el default de propósito
general: sin `opts.mode`, `write`/`append`/`copy` siguen creando con `fsFilePerm`
(0644) recortado por el umask, como cualquier herramienta de terminal
(`TestWriteAtomicRespectsUmask` sigue verde).

`enu.fs.append` **no** gana opts —sigue sin ellas— y **preserva el modo del fichero
existente** (`O_CREATE` no re-chmod-ea lo ya creado): por eso el transcript
0600 se consigue **creándolo vacío** con `write{ exclusive = true, mode = 0600 }`
antes del primer `append`, tras lo cual todos los `append` conservan el 0600. La
extensión `sessions` ([init.lua](../../internal/runtime/embedded/sessions/lua/sessions/init.lua))
crea así el transcript y el lockfile (`<sesión>.jsonl.lock`, §6). Coherencia
cruzada aplicada a [sesiones.md](../contracts/sesiones.md) §2/§6/§8 (el 0600 pasa de
promesa aspiracional a alcanzable y se cita la vía) y a
[guia-plugins.md](../contracts/guia-plugins.md) §7 (credenciales de plugin en 0600).
Implementado en el mismo commit (kernel: `internal/runtime/fs.go` +
`internal/runtime/vmwasm_fs.go`), con tests 🔒 que blindan que
`enu.fs.write{ mode }` fija el modo **independientemente del umask** (en las dos
direcciones: umask laxo no deja el fichero legible por otros; umask estricto no
recorta un modo permisivo) y que `sessions` escribe el transcript en 0600 (e2e,
des-amañado: ya no fija `syscall.Umask(0o077)` alrededor de la creación).

> Nota de integración: en `develop` el nivel de API lo sube antes a 4 la
> construcción de G54 (control de redirects de `enu.http`), que vive en otra rama
> pendiente de merge; esta rama, que aún no la tiene construida, salta `APILevel`
> de 3 a 5 al integrar G57. El conflicto en `internal/runtime/enu.go` y en
> `api.md` §17 se reconcilia en el merge (ambas ediciones convergen en 5).

**Problema.** `sesiones.md` §2 promete «Permisos `0600`» para los transcripts
—que contienen código y salidas de comandos— y §8 lo reafirma como la protección
en reposo del formato. Pero la extensión `sessions` crea el transcript con
`enu.fs.append` (primer append de la entrada `meta`) y el lockfile con
`enu.fs.write{ exclusive = true }`, ambos con `fsFilePerm` (0644) recortado por el
umask del proceso (`internal/runtime/fs.go`), **sin ningún chmod a 0600**. Bajo el
umask habitual (022) el transcript queda en **0644**: legible por el grupo y por
otros usuarios de la máquina, contra lo prometido. Y la API pública **no permite
fijar el modo**: `enu.fs.write` solo aceptaba `opts.exclusive`, `append` no tiene
opts, y no existía `enu.fs.chmod`. Corolario de completitud (idea central 2): una
extensión oficial no podía cumplir su propio contrato con la API pública → **la API
estaba incompleta**; el arreglo va en la API, no en un atajo privilegiado.

**Impacto.** En cualquier máquina multiusuario con umask laxo, los transcripts de
sesión —con código, rutas y salidas de comandos— quedan world-readable pese a la
promesa del contrato. Afecta también a cualquier plugin que necesite escribir un
fichero con permisos restrictivos (credenciales, tokens; guia-plugins §7 los pide
en 0600) y hoy no tenía forma de hacerlo.

**Opciones consideradas.**
- **A (elegida): `opts.mode` en `enu.fs.write`.** Adición mínima y aditiva sobre la
  primitiva de escritura ya existente; `append` preserva el modo del fichero, así
  que basta pre-crear el transcript. No introduce vocabulario nuevo ni una primitiva
  extra: `mode` es el vocabulario universal del kernel (permisos POSIX), encaja en
  la firma que ya lleva `opts`.
- **B (descartada): `enu.fs.chmod(path, mode)` como primitiva aparte.** Superficie
  mayor (una función más), y una ventana entre crear y chmod-ear más ancha y
  observable que la de A (que hace el chmod sobre el temporal, pre-rename, o sobre
  un fichero recién creado en exclusiva). Se reserva por si aparece la necesidad de
  cambiar el modo de un fichero **ya existente** ajeno a una escritura; hoy no hay
  ese caso — se pospone tácitamente, sin `P##` (el disparador sería justo esa
  necesidad).
- **C (descartada): cambiar el default de `fsFilePerm` a 0600.** Rompería la
  semántica de propósito general de `write`/`copy` (una config de usuario no debe
  nacer en 0600) y contradice `TestWriteAtomicRespectsUmask`. El 0600 es una
  política de `sessions`, no del kernel.
