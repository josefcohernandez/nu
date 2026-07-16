# S14 — `enu.fs` (api.md §5)

S14 es 🔒. La superficie de §5 se implementó **sin tocar `api.md`** (no hubo
hallazgo): el puente `suspend` de S04 (ADR-011) bastó para todas las primitivas.
Las decisiones de implementación —ninguna amplía la API, todas concretan
semánticas que §5 deja a criterio del kernel— quedan aquí.

## El patrón ⏸ de IO sobre `suspend` (la plantilla de S15/S16 y la Fase 4)

Toda primitiva ⏸ de `fs` tiene la misma forma:

```
vals := rt.sched.suspend(L, func() deliverFn {
    // GOROUTINE DE FONDO: IO bloqueante en Go, fuera del token, JAMÁS toca Lua.
    res, err := os.AlgoBloqueante(...)
    return func(L *lua.LState) []lua.LValue {
        // YA con el token recuperado: aquí SÍ es seguro tocar Lua.
        if err != nil { mapFsError(L, err); return nil }
        return []lua.LValue{ /* valores Go → LValue */ }
    }
})
return pushAll(L, vals)
```

La regla que blinda el invariante 🔒 "cero data races" de S04: la goroutine de
fondo captura **solo datos Go** (un `path` string, los bytes leídos, el error
crudo) y **no construye ni toca ningún `LValue`**; el error del SO se guarda tal
cual y se traduce a la tabla §1.4 **dentro de la `deliverFn`**, que corre con el
token recuperado —porque `raiseError`/`L.NewTable` tocan Lua—. Mientras la
goroutine de fondo trabaja, la task está bloqueada sin el token, así que el loop
no se congela (otras tasks progresan). **S15 (`fs.watch`), S16 (`enu.proc`) y toda
la red (Fase 4) reusan esta plantilla literalmente**; por eso se documenta como
patrón y no como detalle de `fs`.

Guardia común `requireTask(L, nombre)`: las ⏸ exigen estar en una task (`L != host`,
como `cleanup`/`await`/`reload`); fuera → `EINVAL` accionable. `cwd` es la **única
excepción**: no es ⏸ (consulta pura), así que NO lleva guardia y funciona también
en el chunk de `-e`.

## Mapeo de errores del SO → códigos §1.4 (`mapFsError`)

Un único punto traduce el errno: `errors.Is(err, os.ErrNotExist)` → `ENOENT`,
`os.ErrExist` → `EEXIST`, `os.ErrPermission` → `EACCES`, cualquier otro → `EIO`.
Se usa `errors.Is` (no comparación directa) porque la stdlib envuelve los errnos
en `*os.PathError`; `errors.Is` los desenvuelve. `EINVAL` lo emiten los guardias
de uso (fuera de task), no `mapFsError`. El mensaje conserva el texto del error de
Go (la ruta incluida) como pista accionable; nunca se traga el error.

## Escritura atómica: temporal en el MISMO dir + rename

`write` normal escribe a `.nu-fs-*.tmp` **en el directorio destino** (no en `/tmp`)
y hace `os.Rename`. El temporal va al mismo dir para que el rename sea
**same-filesystem** y por tanto atómico —un rename entre sistemas de ficheros
distintos no es atómico (y `os.Rename` ni funciona)—. Garantía: un lector
concurrente ve el contenido viejo o el nuevo **entero**, jamás un fichero a medias.
Un `defer` borra el temporal si se retorna por error antes del rename (no deja
residuo, blindado por test); tras un rename con éxito el temporal ya no existe con
ese nombre, así que el `Remove` diferido es un no-op. Se hace `Chmod` 0644 al
temporal porque `os.CreateTemp` lo crea 0600 y un `write` debe producir un fichero
con permisos normales.

## G17 — `write{exclusive=true}` es `O_EXCL`, sin temporal+rename

La rama exclusiva NO usa temporal+rename: el rename **sobreescribiría** un fichero
existente, rompiendo la exclusión. Se usa `O_WRONLY|O_CREATE|O_EXCL`, que es la
primitiva del SO que crea **solo si no existe** en una operación indivisible y
falla con `os.ErrExist` (→ `EEXIST`) si ya existe. Es la pieza de los lockfiles de
sesiones (sesiones.md §6): la creación del lock debe ser atómica y fallar si otro
proceso ya lo tiene. `append` usa `O_APPEND` (no es atómico como `write` —un append
es incremental por naturaleza, para logs/JSONL—; el `O_APPEND` del SO garantiza que
cada escritura va al final).

## `stat` de inexistente → `nil`, no lanza (la asimetría con `read`/`list`)

`stat` es la consulta "¿existe y qué es?", no una lectura que falla: un fichero
inexistente devuelve **`nil` sin lanzar** (§5). Cualquier OTRO error (permiso sobre
un componente del path, IO) sí se lanza. Contrasta deliberadamente con `read` y
`list`, que sobre un inexistente **sí** lanzan `ENOENT` —leer/listar lo que no
existe es un fallo, no una respuesta válida—. `mtime_ms` se da en milisegundos
(`ModTime().UnixMilli()`, coherente con §1.5: los tiempos del core son en ms);
`mode` son los bits de permiso Unix (`Mode().Perm()`).

## `mkdir` crea padres (`MkdirAll`)

`mkdir` usa `os.MkdirAll`: crea los **padres que falten** y es **idempotente** si
el directorio ya existe. Es el comportamiento esperado de una herramienta de
terminal (`mkdir -p`): nadie quiere encadenar mkdirs para crear `a/b/c` ni que
falle porque el directorio ya estaba. Si el path existe pero es un **fichero**,
`MkdirAll` falla (no se sobreescribe un fichero por un directorio). La alternativa
(`os.Mkdir`, un solo nivel, falla si ya existe) se descartó por ergonomía: §5 no
exige un nivel, y un plugin que quiera crear una jerarquía no debería tener que
recorrerla a mano.

## `remove`: recursive obligatorio para dir no vacío, inexistente = no-op

Borrar un fichero o un directorio **vacío** funciona sin más. Un directorio **no
vacío** exige `opts.recursive=true` —sin él, `os.Remove` falla y se rinde como
`EIO`—: es la salvaguarda contra un `rm -rf` accidental; borrar un árbol entero
debe ser explícito. Con `recursive=true` se usa `os.RemoveAll`. **Inexistente es
no-op** (no lanza `ENOENT`): borrar lo que ya no está deja el sistema en el estado
deseado (el recurso no existe), que es justo lo que pedía la llamada —semántica
idempotente, coherente con `mkdir`—. `RemoveAll` ya es no-op sobre inexistente; en
la rama no recursiva se traga el `ErrNotExist` explícitamente.

## `copy` solo ficheros, en streaming

`copy` usa `io.Copy` (streaming, sin cargar un fichero grande entero en RAM) y
cubre **solo ficheros**: copiar un directorio recursivamente es trabajo de más alto
nivel (Lua sobre `list`+`copy`), no una primitiva del core —el core da el ladrillo,
la composición es del autor de extensiones—. Abre el origen primero para que su
inexistencia/permiso sea el error que el usuario espera ver, y entonces crea el
destino (`O_TRUNC`: sobreescribe).

## `tmpdir` propio de la sesión, perezoso y reutilizado; `cwd` inmutable

`tmpdir` crea **un** directorio temporal por sesión (`os.MkdirTemp` bajo
`os.TempDir()`), **perezosamente** la primera vez y **reutilizado** después
(cacheado en `rt.fs.tmpdir`). La creación corre en la goroutine de fondo (es IO),
así que el campo lo protege un candado en `fsState` —dos `tmpdir` concurrentes no
deben crear dos directorios ni correr una carrera sobre el campo, y el candado no
depende del token (la goroutine de fondo no lo tiene)—. `Runtime.Close` lo borra
recursivamente (`closeTmpdir`): el scratch no sobrevive al proceso. `cwd` es la
única función NO ⏸ de `fs`: una consulta pura (`os.Getwd`), [W], **inmutable**
durante la sesión —no hay `chdir`, porque cambiar el cwd del proceso sería un
efecto global que rompería el aislamiento por tarea (ADR-008); un subproceso que
quiera otro dir lo recibe por `opts.cwd` (§6), sin tocar el cwd del proceso—.

## No se usa el `io`/`os` de Lua

Todo el IO es Go puro (`os`/`io` de la stdlib de Go). El baseline del sandbox (S01,
§1.2) dejó fuera `io` y recortó `os` en Lua a propósito; `enu.fs` es la superficie
**controlada** de IO que los reemplaza, con errores estructurados, ⏸ sobre el loop
y mapeo de códigos. Un plugin nunca toca el sistema de ficheros por la puerta de
atrás del `os` de Lua.
