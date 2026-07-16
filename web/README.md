# Web de documentación de `enu`

La web de `enu`: portada, wiki (los documentos de diseño de `docs/`), referencia
función a función de la API del core y guía del primer plugin. Construida con
[Astro](https://astro.build/), sin ningún theme de terceros: **la web ES un
terminal** — la portada replica la pantalla de arranque de enu, la wiki es un
pager tipo `less(1)` y toda la navegación funciona con teclado real. La
especificación de diseño completa vive en `design_handoff_nu_web/README.md`
(tokens de los 4 themes, gramática visual TTY, las 8 pantallas canónicas).

> La **fuente de verdad** de la API es [`docs/contracts/api.md`](../docs/contracts/api.md) (la
> "superficie sagrada" v1). La sección `/api` de este sitio la presenta de forma
> orientada a tareas y con ejemplos. Si algo discrepa, manda `docs/contracts/api.md`.

Esa relación se **verifica mecánicamente**: `npm run check:drift`
([`scripts/check-drift.mjs`](scripts/check-drift.mjs), sin dependencias) extrae
el inventario de firmas y marcadores (⏸/[W]) de ambos lados y falla ante
cualquier discrepancia — firma distinta, marcador que baila, función sin
documentar o inventada. Corre en CI (job "Coherencia web ↔ api.md") y como gate
del despliegue. Para corregir deriva, la skill `/sync-web` tiene el protocolo y
las convenciones de formato de las páginas (dónde van los marcadores, fences
sin etiqueta para firmas, ` -- ` para comentarios de cola).

## Desarrollo

```sh
cd web
npm install
npm run dev      # servidor de desarrollo en http://localhost:4321/enu/
npm run build    # sitio estático en dist/ + índice de búsqueda (pagefind)
npm run preview  # sirve el build (necesario para probar la búsqueda)
```

## Estructura

```
web/
├── astro.config.mjs              # base /enu/, shiki css-variables, plugins md
├── scripts/
│   ├── check-drift.mjs           # detector de deriva web ↔ docs/contracts/api.md
│   └── generar-og.mjs            # regenera public/og.png desde el wordmark
├── src/
│   ├── content.config.ts         # colecciones: wiki (../docs), empezar, referencia
│   ├── content/docs/
│   │   ├── empezando/            # instalación y primeros pasos (→ /docs/…)
│   │   └── referencia/           # una página por namespace enu.* (→ /api/…)
│   ├── pages/                    # index (portada), docs/[slug], api/[slug],
│   │   │                         # plugins, 404
│   ├── layouts/ · components/    # Base, headers, statusline, sidebars, carriles
│   ├── lib/                      # const (dominio placeholder, versión), i18n
│   │   │                         # es/en del chrome, docmap/apimap, gitmeta,
│   │   │                         # markdown/ (plugins remark/rehype)
│   ├── scripts/                  # keyboard (comandos + REPL), pager, búsqueda,
│   │   │                         # mermaid con colores del theme
│   └── styles/                   # tokens de los 4 themes + estilos por sección
└── public/                       # favicon.svg y og.png (generados del wordmark)
```

La **wiki** (`/docs/<slug>`) se genera desde los `.md` reales de `../docs/` vía
content collections; el commit y la fecha de "última edición" de cada página
salen de git en build (`src/lib/gitmeta.ts`). Las páginas de `empezando/` son
las únicas con fichero propio aquí (extraídas del README raíz). Los 4 themes
(enu, dracula, gruvbox, solarized) son CSS custom properties bajo
`[data-theme]`, persistidos en localStorage; el chrome es bilingüe es/en y el
contenido se publica en ambos idiomas (ES bajo `/docs`·`/api`·`/plugins`, EN
bajo `/en/…`).

## Contenido en inglés (instantánea traducida)

El **español es la fuente de verdad**. El contenido inglés bajo
`src/content/en/` (`wiki/`, `empezando/`, `extensiones/`, `referencia/`) es una
**instantánea traducida** de su gemelo ES —mismos slugs, mismo orden de docmap,
mismos marcadores `<!-- enu:interno -->`—, servida en rutas estáticas paralelas
`/en/docs`, `/en/api` y `/en/plugins` (resolución del hallazgo W-04). El
template es único por sección (`components/pages/{WikiPage,ApiPage,PluginsPage}
.astro`) y se parametriza por idioma; los wrappers de `pages/en/` solo cambian
la colección de origen.

**Al cambiar `docs/` o cualquier contenido ES, hay que regenerar la traducción
EN afectada** —no se actualiza sola—. El picker de idioma navega a la página
homóloga (`/enu/docs/x` ↔ `/enu/en/docs/x`), así que una página EN ausente sería
un 404. Nota: `check:drift` vigila **solo la referencia ES** frente a
`docs/contracts/api.md` (la superficie sagrada); la referencia EN, al ser instantánea, no
entra en ese gate.

## Ejemplos verificados

Los ejemplos `enu -e '...'` de la referencia están comprobados contra el binario
real (`go build -o enu . && enu -e '...'`). Recuerda que el chunk de `enu -e` corre
en el estado principal: las funciones suspendientes (⏸) van envueltas en
`enu.task.spawn(...)`.

## Despliegue

`.github/workflows/docs.yml` construye y publica el sitio en GitHub Pages al
hacer push a `main` cuando cambia algo bajo `web/`. El `base` del sitio es
`/enu/` (project page); para un dominio propio, vacía `base` en
`astro.config.mjs`. El dominio del `curl` de instalación es un placeholder
centralizado en `src/lib/const.ts` (`DOMAIN`), pendiente de decisión.
