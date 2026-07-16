---
title: "Pegar una imagen: el evento `paste` solo trae texto"
type: "hallazgo"
id: "G30"
status: "resuelto"
origin: "ronda 6 de pseudocódigo (harness de coding sobre enu.ui)"
resolution: "Pegar contenido no-texto del portapapeles entrega un evento paste con path a un fichero temporal, nunca los bytes."
affected: ["api.md §9.3"]
---
# G30 · Pegar una imagen: el evento `paste` solo trae texto — `api.md` §9.3 — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §9.3): pegar contenido
**no-texto** del portapapeles (una imagen) **inyecta una ruta**, no los
bytes. El core vuelca la imagen a un fichero temporal de la sesión
(`enu.fs.tmpdir`) y entrega un evento `paste` con `path` (sin `text`); la UI
inserta la ruta exactamente como una mención `@`, y el agente decide leerla
(no se incrusta el contenido a ciegas, igual que las menciones de
[chat.md](chat.md) §3). Los bytes binarios nunca cruzan las fronteras de
texto/JSON (coherente con G11). Es **distinto de P6** (render de imágenes en
el transcript, pospuesto): aquello es pintar, esto es entrada. Descartado
entregar los bytes en el evento (reintroduce binario en la frontera de
input que G11 cerró) y descartado plegarlo a P6 (P6 es salida; pegar una
ruta es útil aunque nunca se pinte la imagen).

**Problema.** Un harness de coding (estilo claude-code) pega imágenes del
portapapeles, pero el evento `paste` solo trae `text` y `clipboard_get`
devuelve `string`: pegar una imagen no se podía expresar (ronda 6,
escenario 29).

**Impacto.** Flujo cotidiano de un harness de coding; barato de cerrar
ahora sobre la superficie que se congela.

**Opciones.** (a) El evento `paste` de contenido no-texto entrega `path`
(fichero temporal volcado), insertable como `@` — la elegida; (b)
`enu.ui.clipboard_get_image() -> path?` aparte (superficie extra para lo
mismo); (c) dejarlo fuera de v1, plegado a P6 (descartado: P6 es salida).

---
