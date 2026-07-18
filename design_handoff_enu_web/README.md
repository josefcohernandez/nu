# Handoff: nu — web (landing + wiki + api + plugins)

## Overview

Rediseño completo de la web de [nu](https://github.com/dbareagimeno/enu) (runtime de Lua orientado a terminal cuya killer app es un coding harness). Sustituye a la web Astro existente. El concepto: **la web ES un terminal** — la portada replica una pantalla de arranque de nu, la wiki es un pager tipo less(1), y toda la navegación funciona con teclado real. La marca no es un color sino una **paleta de terminal** ("theme nu", cian por defecto) intercambiable por themes famosos, reflejando la hiperconfigurabilidad del producto.

## About the Design Files

`nu drafts.dc.html` es una **referencia de diseño creada en HTML** — un canvas de exploración con 14 turnos de iteración, no código de producción. La tarea es **recrear las pantallas candidatas en Astro** (el stack existente del proyecto) usando sus patrones. Los turnos relevantes son los FINALES (arriba del documento); los turnos 1–9 son historia de la exploración y deben ignorarse.

**Pantallas canónicas a implementar** (ids en el canvas):
- `11a` — Portada (landing)
- `12a` — Página de doc en la wiki
- `13a` — Referencia de la API
- `14a` — 404
- `14b` — Sección plugins
- `14c` — Búsqueda
- `14d` / `14e` — Portada y wiki en móvil

## Fidelity

**High-fidelity.** Colores, tipografía, espaciados y copy son finales. Los tamaños de fuente del prototipo están pensados para cards de ~720–960px; en producción, escalar proporcionalmente (la portada real a viewport completo admite el slogan a 36–44px manteniendo las jerarquías relativas).

## Sitemap y navegación

```
/            portada (pantalla de arranque)
/docs/...    wiki-pager (un doc .md por página)
/api/...     referencia nu.* (un módulo por página)
/plugins     guía "tu primer plugin"
/404         E404
```

Nav de primer nivel en el header de TODAS las páginas internas: `docs · api · plugins` (la activa se pinta invertida: texto en `bg` sobre fondo `key`). La portada NO lleva esa nav — su menú `[i][d][a][g]` es la nav.

## Design Tokens — los 4 themes

Cada theme define 8 tokens. Implementar como CSS custom properties bajo `[data-theme="…"]` en `<html>`; persistir en localStorage; default `nu`.

| token | nu (default) | dracula | gruvbox | solarized (light) |
|---|---|---|---|---|
| bg | #0a1416 | #282a36 | #282828 | #fdf6e3 |
| fg | #a9bfc4 | #f8f8f2 | #ebdbb2 | #657b83 |
| bright | #e8f4f6 | #ffffff | #fbf1c7 | #073642 |
| dim | #4e686e | #6272a4 | #a89984 | #7d8e91 |
| border | #16292d | #3b3d51 | #3c3836 | #eee8d5 |
| key (acento) | #4fcadb | #ff79c6 | #fe8019 | #2aa198 |
| c2 (verde sem.) | #7fc8a8 | #50fa7b | #b8bb26 | #859900 |
| c3 (cian sem.) | #4fcadb | #8be9fd | #8ec07c | #268bd2 |
| c4 (ámbar/rojo sem.) | #c9a06a | #f1fa8c | #fabd2f | #cb4b16 |

Usos: `key` = teclas de menú, acento del slogan, cursor, links, elemento activo (invertido). `dim` = prompts, chrome, separadores de texto. `bright` = títulos y texto enfatizado. `border` = líneas divisorias Y fondo de bloques de código. `c2/c3/c4` = semánticos (strings Lua, rutas, errores — E404 usa c4). El cambio de theme es **instantáneo, sin transition**.

Extras de plataforma: `::selection` con `background:key; color:bg`; scrollbar estilizado (thumb `border`, track `bg`); focus visible = `box-shadow: inset 0 0 0 1.5px key` (los contenedores capturan teclado con tabindex).

## Tipografía

Una sola familia: **IBM Plex Mono** (400, 500, 600), fallback `ui-monospace, monospace`. NO cargar Newsreader ni Space Grotesk (eran de drafts descartados).

Escala (sobre card 720–960px; escalar en producción):
- Slogan portada: 28px/1.4, weight 600
- Títulos de página (wiki/api): 20px, weight 600, con línea de `═` en `dim` debajo (`letter-spacing:-1px`)
- Subtítulos de sección: 13.5–14px weight 600 con línea de `─`
- Cuerpo: 13–14px, line-height 1.9–1.95, max-width ~68ch
- Chrome (headers/statusline): 10.5–11px
- Todo el CHROME en lowercase; la prosa con capitalización normal

## Gramática visual del terminal (reglas duras)

- **Wordmark**: `nu` en caja invertida (`background:bright; color:bg; padding:2px 9px; weight:700`). Ancla de marca constante en todos los themes. Sirve también de favicon/OG.
- **Citas**: prefijo `│` en `dim` + cursiva. NUNCA border-left CSS.
- **Títulos**: subrayado con caracteres `═` (h1) y `─` (h2) en `dim`.
- **Teclas**: `[x]` — corchetes en `dim`, letra en `key` weight 600.
- **Cursor**: bloque 9×17px en `key`, animación blink 1.1s step-end (`@keyframes` 0-49% opacity 1, 50-100% opacity 0). Respetar `prefers-reduced-motion`.
- **Selección activa en listas**: fila invertida (`bg` sobre `key`) con prefijo `▸`.
- **Prohibido**: emojis, iconos SVG, border-radius (salvo ninguno: todo es rectangular), sombras, gradientes.
- **Statusline**: una sola, abajo, en todas las páginas. Izquierda = contexto (`docs/core/filosofia.md · 8% · 1/15`, `api/fs · 8% · 1/12`, `plugins/primer-plugin · 1/3`, `404 · 0/15`); derecha = teclas disponibles.

## Screens

### 11a — Portada `/`

Layout: columna flex a viewport completo. Header (borde inferior `border`): wordmark izquierda; derecha `theme: [nu] dracula gruvbox solarized` y `lang: [es] en` como TEXTO plano clicable (activo entre corchetes, en `key`, weight 600; resto en `dim`). Footer: `apache-2.0` izquierda, `v0.1.3` derecha (link subrayado a GitHub releases).

Cuerpo centrado verticalmente, padding generoso:
```
$ nu                                (dim, 12px)
Tu agente de código.                (bright)
Tus reglas.                         (key)
[cuerpo]                            (fg, max-width 450px)
[i] instalar — curl -fsSL nu.sh/install | sh
[d] documentación — la wiki de nu
[a] api — referencia nu.*
[g] github ↗
> ▊                                 (prompt interactivo)
[línea de feedback]                 (dim, 12px, min-height fija)
```

Copy exacto ES: slogan "Tu agente de código." / "Tus reglas."; cuerpo "Instálalo con una línea. Úsalo tal cual, o cámbialo entero escribiendo Lua. nu es el coding harness que es tuyo de verdad."
Copy EN: "Your coding agent." / "Your rules."; "Install it with one line. Use it as is, or rewrite the whole thing in Lua. nu is the coding harness that is truly yours." La entrada `[d]` en EN dice "documentation — the nu wiki · in spanish for now".

**NOTA dominio**: `nu.sh/install` es placeholder — el dominio real está pendiente de decisión. Centralizar en una constante.

### 12a — Wiki `/docs/<slug>`

Grid 3 columnas: sidebar 240px (borde derecho) + contenido (max ~68ch) + carril derecho 180px. Header: wordmark + nav (docs activo) + ruta `docs/core/filosofia.md` (dim, fichero en bright) + nota de idioma + `github ↗`.

Sidebar: grupos `empezar/ espec/ extensiones/ proceso/` (nombres de grupo en `dim` con `/`), ficheros `.md` indentados, activo invertido con `▸`. Contenido: título + `═`, cita `│`, prosa, bloques de código (fondo `border`, `$` en `key`), tablas como grid 2 columnas con borde inferior `border` por fila (header en `dim`). Pie de contenido: prev/next separados por borde superior (next en `key` con `→`). Carril derecho: `§ en esta página` (activo bright, resto dim) + bloque de metadata `última edición / <commit en key> · hace N días`.

**Mapa doc→página** (fuente de verdad = los .md del repo vía content collections; el commit sale de git, NO se escribe a mano):
- `empezar/`: instalación e inicio rápido (extraídos de README.md — únicas páginas sin .md propio)
- `espec/`: filosofia, arquitectura, modelo-ejecucion, api, adr
- `extensiones/`: providers, agente, sesiones, chat, guia-plugins, malla
- `proceso/`: problemas, pospuesto, pseudocodigo, implementacion, decisiones-implementacion

Nota: `docs/contracts/api.md` (la espec) permanece en el árbol; la sección `/api` es la **referencia navegable generada desde él** — su carril derecho lo declara ("generada desde docs/contracts/api.md · <commit>").

### 13a — API `/api/<módulo>`

Misma estructura 3 columnas (sidebar 210px). Header derecha: `api v1 · estable` (estable en `key`). Sidebar: grupo `nu.*` con los 12 módulos (fs, proc, http, ws, search, text, re, ui, events, task, workers, codecs), grupo `extensiones/` con contratos (agent.tool, chat.command, providers). Contenido: título `nu.fs` + `═`, intro, **cards de función**: caja con borde `border`, fila superior `read(path) → string, err` (nombre en `key` 600, params en `fg`, retorno en `dim`; si pasa por permisos, sufijo `· [permiso]`), fila inferior descripción 12px. Ejemplo de código Lua con syntax colors del theme (keywords `key`, strings `c2` o `fg`, comentarios `dim`), etiquetado `ejemplo — pruébalo con nu -e`. Carril: card CTA "¿tu primer plugin?" (borde `key`) → /plugins, `§ en esta página` con las funciones, y metadata "generada desde docs/contracts/api.md · commit".

### 14b — Plugins `/plugins`

2 columnas: contenido + carril 230px. Título "Escribe tu primer plugin". Tres pasos numerados (`1 · plugin.toml`, `2 · init.lua`, `3 · actívalo en nu.toml`), cada uno con su bloque de código real y funcional. Cierre: "Escribe /hola en el chat. Ya eres autor de plugins." Carril: links a los 5 contratos (→ /api), card de `examples/` (`XDG_CONFIG_HOME=examples nu`), nota "la regla de oro: lua decide, go ejecuta → filosofia".

### 14a — 404

Chrome completo (header+nav+statusline `404 · 0/15`). Centro:
```
$ nu docs/<ruta-pedida>
E404: no es un documento del editor: <ruta-pedida>    (c4, weight 600)
¿querías decir? → <doc más parecido>                   (link key subrayado)
[d] índice de docs
[q] portada
> ▊
```
La sugerencia usa distancia de edición contra las rutas existentes (build-time manifest).

### 14c — Búsqueda

Overlay sobre la página actual (o página /buscar). Statusline izquierda se convierte en `/término▊`. Resultados agrupados por documento (nombre en bright 600), líneas con `§n ·` en dim; el término resaltado invertido (`key` sobre `bg` en el resultado activo con `◂`, `border`+`bright` en el resto). Cabecera: "5 coincidencias en 3 documentos para <término invertido>". Teclas: `[n/p]` saltar, `[enter]` abrir, `[esc]` cerrar. Índice de búsqueda build-time (p.ej. pagefind o minisearch sobre los .md).

### 14d/14e — Móvil (<768px)

- Portada: sin prompt ni feedback (no hay teclado físico). El menú se convierte en **filas táctiles** de ancho completo, borde `border` (la primaria `[i]` con borde `key`), altura ≥48px. Theme/lang como filas de texto clicable en el footer.
- Wiki: sidebar e índice tras botón `[≡]` (drawer overlay, mismo estilo de lista que desktop); carril derecho desaparece; prev/next como dos filas táctiles grandes; statusline táctil: `[≡] índice · [/] buscar · [q] portada` con padding ≥6px extra por target.
- API y plugins siguen el mismo patrón que la wiki.

## Interactions & Behavior

### Teclado global (desktop) — módulo compartido
- Portada: `i/d/a/g` con input vacío ejecutan directo (i copia el curl al portapapeles y lo notifica en la línea de feedback; d/a/g navegan). Cualquier otro texto + Enter → `nu: «X» no encontrado — prueba [i], [d], [g] o help`.
- Comandos (portada con `>` visible; páginas internas tras pulsar `:` que convierte la statusline en prompt): `help`, `theme <nombre>`, `lang <es|en>`, `open <doc>`, `q`, `i/d/a/g`. Comando desconocido en páginas internas → `E492: no es un comando del editor: X` (guiño vim).
- `help` responde: `comandos: i · d · g · theme <nu|dracula|gruvbox|solarized> · lang <es|en>` + segunda línea `…y si sabes lua, ya sabes qué hacer` (el ÚNICO anuncio del easter egg).
- **Easter egg**: comando `lua` → mini-REPL (prompt cambia a `lua>` en `key`). Evalúa: aritmética, `print("…")`, concatenación `..`, `nu.version` → `"0.1.3"`. `salir`/`exit`/`q` para volver. Mensaje de entrada: `lua 5.4 (embebido en nu) — escribe salir para volver`.
- Pager (docs/api): `j/k` scroll, `n/p` doc/módulo siguiente/anterior, `/` búsqueda, `q` → portada.
- Backspace con prompt vacío o Escape cierran el modo comando/búsqueda.
- El teclado NUNCA captura si el foco está en un input/textarea del navegador.

### Persistencia
- `localStorage`: theme (clave p.ej. `nu-theme`) e idioma (`nu-lang`). Aplicar theme inline en `<head>` antes del primer paint para evitar flash.

### i18n
- El **chrome** entero es bilingüe es/en (strings arriba). El **contenido** de docs solo existe en español por ahora: en EN mostrar la nota "in spanish for now" en el header de páginas de contenido.

## State Management
- `theme: 'nu'|'dracula'|'gruvbox'|'solarized'` (localStorage + data-attribute)
- `lang: 'es'|'en'` (localStorage)
- `commandMode: ''|'cmd'|'search'|'repl'` + buffer de texto + línea de feedback
- Posición de lectura (% para statusline) derivada del scroll

## Assets
- Ninguna imagen. El favicon y la OG image se generan del wordmark (caja `bright` con "nu" en `bg`, sobre fondo `bg` del theme nu). No hay iconos.
- Fuente: IBM Plex Mono vía Google Fonts o self-hosted (preferible self-hosted).

## Files
- `screenshots/` — capturas de las 8 pantallas canónicas (theme nu, es). Referencia visual rápida; la verdad de píxel está en el HTML.
- `nu drafts.dc.html` — canvas completo de exploración. Pantallas canónicas: ids `11a`, `12a`, `13a`, `14a`, `14b`, `14c`, `14d`, `14e` (turnos 11–14, arriba del documento). La lógica de teclado/themes/REPL de referencia está en el `<script>` del propio fichero (clase `Component`).

## Decisiones abiertas (no bloquean)
- **Dominio** (el curl usa `nu.sh` de placeholder) — pospuesto por el autor hasta el final.
- Traducción EN del contenido de docs.
- El drawer `[≡]` abierto en móvil no está dibujado — usar la misma lista del sidebar desktop a ancho completo.
