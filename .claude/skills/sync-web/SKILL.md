---
name: sync-web
description: Sincroniza la web de documentación (web/referencia/) con docs/api.md, la fuente de verdad, guiado por el detector mecánico check-drift. Úsala cuando el job "Coherencia web ↔ api.md" de CI esté en rojo, o como paso final de cualquier sesión/hallazgo que añada o toque firmas de api.md (el bump de nu.version.api es la señal).
---

# Sincronizar la web con la espec

La web (`web/src/content/docs/referencia/`) es una **presentación derivada** de
[docs/api.md](../../../docs/api.md): orientada a tareas, con prosa pedagógica y
ejemplos verificados. La deriva mecánica (firmas, marcadores ⏸/[W], inventario)
la detecta un script determinista; esta skill es la parte editorial que el
script no puede hacer: redactar y verificar.

## Protocolo

1. **Corre el detector**: `node web/scripts/check-drift.mjs` (o `npm run
   check:drift` desde `web/`). Cada discrepancia trae fichero:línea y qué dice
   cada lado. `--inventario` vuelca el índice derivado de api.md en JSON.

2. **Decide la dirección del arreglo.** `docs/api.md` manda *siempre*
   (`web/README.md` lo declara). Si al mirar una discrepancia concluyes que la
   *espec* está mal, eso no se arregla editando la web ni la espec a la ligera:
   es un candidato a hallazgo → `/hallazgo`. Todo lo demás se arregla en la web.

3. **Aplica las correcciones respetando las convenciones de las páginas:**
   - Las firmas viven en **fences sin etiqueta** (las etiquetadas ```lua/```sh
     son ejemplos y el detector las ignora). Texto de la firma **idéntico** al
     de api.md, espacios aparte; los métodos de un handle van indentados bajo
     la función que lo devuelve (`nu.task.every(ms, fn) -> Timer` +
     `  Timer:stop()`).
   - Marcadores ⏸/[W]: en el **heading que nombra** la función (`## `nu.fs.read``
     ⏸ [W]`; un heading de módulo como `## `nu.json`` [W]` cubre sus funciones),
     o **inline en la línea de la firma** cuando el heading agrupa sin nombrar
     ("Manipulación", "Mensajes"). Un heading que nombra a X no contagia
     marcadores a los métodos listados debajo: estos los llevan inline.
   - Comentarios de cola con ` -- ` (el detector los recorta antes de comparar).
   - `convenciones.md` y `cli.md` no declaran API: el detector las excluye.

4. **Función o namespace nuevo en api.md**: sección nueva en la página del
   namespace (o página nueva en `referencia/` + entrada en el sidebar de
   `web/astro.config.mjs`), imitando el tono de las existentes: qué es, la
   fence de firma, prosa del porqué y un ejemplo.

5. **Verifica los ejemplos nuevos contra el binario real** (la convención de
   `web/README.md`): `go build -o nu . && ./nu -e '...'`. Recuerda que el chunk
   de `nu -e` corre en el estado principal: las funciones ⏸ van envueltas en
   `nu.task.spawn(...)`. Un ejemplo que no puedas ejecutar (red, TTY) se marca
   como tal en la página, no se inventa su salida.

6. **Cierra en verde**: el detector a cero y `npm run build` (Astro valida
   enlaces y frontmatter). El commit, en español, cita el disparador
   (`S##`/`G##`) que movió la API.

## Qué NO hace esta skill

- No toca `docs/api.md` ni los contratos: la dirección espec→web es única.
- No relaja el detector para "hacer pasar" una página: si un caso legítimo no
  parsea, se arregla el script explicando el caso en su cabecera, nunca
  añadiendo una excepción silenciosa a la comparación.
