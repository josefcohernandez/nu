---
name: alta-wiki
description: Da de alta (o retira) una página de la wiki de la web de docs — nueva guía, nueva página de extensión, nueva página de «empezar», o un contrato de docs/ que pasa a publicarse. La API nueva NO es esta skill (va a /sync-web). Su valor es la checklist mecánica completa de puntos de contacto (docmap, WIKI_SLUGS, i18n, colecciones) y el cierre en verde que garantiza que docmap, el plugin de enlaces y los gates de limpieza quedan coherentes.
---

# Alta de una página en la wiki

La web publica la **Capa 1** de `docs/` (contratos vivos) más las páginas
locales de «empezar» y «extensiones», con el orden y los grupos definidos en un
único manifiesto: `web/src/lib/docmap.ts`. Añadir o quitar una página toca
**varios ficheros que deben quedar idénticos entre sí**; el valor de esta skill
es esa checklist mecánica — olvidar un punto de contacto rompe el build o filtra
un enlace muerto.

Antes de nada, ubica el caso.

## Los cuatro casos de alta

- **(a) API nueva en `docs/api.md`.** NO es esta skill. La referencia `/api` se
  deriva de `api.md` con su propio gate `check-drift`; una firma o namespace
  nuevo se publica con **`/sync-web`**, no tocando el docmap de la wiki.
- **(b) Un contrato de `docs/` que pasa a publicarse** (uno nuevo, o uno hasta
  hoy interno —como `malla.md`— cuando se apruebe). Colección **`wiki`**: la
  página se renderiza directamente desde el `.md` real del repo bajo `docs/`.
- **(c) Una página local nueva** (guía, tutorial, página de una extensión sin
  contrato propio). Colección **`empezar`** o **`extensiones`**: es un `.md`
  bajo `web/src/content/docs/<carpeta>/` con **frontmatter `title` y
  `description` y SIN H1 en el cuerpo** (el H1 lo pone la plantilla desde el
  frontmatter).
- **(d) Despublicar una página.** Se quita de todos los puntos de contacto de
  abajo. Si era un contrato de `docs/` (colección `wiki`), el fichero **no se
  borra** —sigue en `docs/`, es fuente de verdad— y sus enlaces `.md` caerán
  solos al blob de GitHub por la rama existente de `remark-enlaces-wiki`.

## Checklist mecánica de puntos de contacto

Estos ficheros son un sistema: `docmap.ts` es la fuente y el resto lo replica.
Cámbialos juntos.

1. **`web/src/lib/docmap.ts`** — el manifiesto. Añade (o quita) la entrada en su
   **grupo** (`empezar` · `espec` · `extensiones`), en la **posición correcta
   del orden de lectura** (alimenta prev/next y la statusline), con su
   **colección** (`wiki` para contratos de `docs/`; `empezar`/`extensiones` para
   páginas locales) y su **`gitPath`**, que sale de la rama de `gitPath()` según
   la colección:
   - `wiki` → `docs/<slug>.md`
   - `empezar` → `web/src/content/docs/empezando/<slug>.md`
   - `extensiones` → `web/src/content/docs/extensiones/<slug>.md`

   `TOTAL`, el 404 y prev/next se **derivan solos** del docmap; no los toques.
2. **`web/src/lib/markdown/remark-enlaces-wiki.mjs`** — el set `WIKI_SLUGS`
   **debe quedar idéntico** a la lista de slugs del docmap (el plugin corre
   fuera de `astro:content` y por eso los replica a mano). Un slug del docmap que
   falte aquí hace que sus enlaces `.md` caigan a GitHub en vez de quedar
   internos; uno de más apunta a una página inexistente. La aserción de
   consistencia de `test-limpieza.mjs` verifica exactamente este pareo.
3. **`web/src/lib/i18n.ts`** — **solo si añades un grupo nuevo** al sidebar. Cada
   grupo tiene su etiqueta i18n (`s1`, `s2`, `s3`…) en `es` y `en`. Dar de alta
   una página dentro de un grupo existente NO toca i18n.
4. **Solo caso (c), página local:**
   - Si la colección **ya existe** (`empezar`, `extensiones`), basta crear el
     `.md` con su frontmatter `title`/`description` y sin H1.
   - Si creas una **colección nueva**: decláralala en
     `web/src/content.config.ts` (glob sobre su carpeta, schema `title` +
     `description`), haz que `web/src/pages/docs/[slug].astro` la cargue en
     `getStaticPaths` y la trate como `empezar` para el H1 desde frontmatter, y
     añade su rama en `gitPath()` del docmap.
5. **Solo caso (b), contrato de `docs/`:** revisa la **contaminación de
   proceso**. El plugin `remark-limpieza-interno.mjs` barre en build los
   marcadores parentéticos (`(G##)`, `(P##)`, `(S##)`, `(ADR-NNN)`) y los
   blockquotes de estado `> ✅ …`, así que NO hay que limpiar la fuente a mano.
   Pero una **sección interna entera** que no deba publicarse (apéndices de
   «cuestiones abiertas», estado de implementación, §§ sin aprobar) necesita el
   par explícito `<!-- nu:interno -->` … `<!-- /nu:interno -->` en el `.md` de
   `docs/`. La trazabilidad restante se queda en la fuente: es correcta, la web
   la limpia.

## Cierre en verde

No está hecho hasta que estas cuatro pasan:

1. `node web/scripts/test-limpieza.mjs` — fixtures del plugin **y** la aserción
   `WIKI_SLUGS` ↔ docmap. Si esta última falla, los ficheros de los pasos 1-2
   divergieron.
2. `npm run build` en `web/` — Astro caza slugs muertos en sidebar, prev/next e
   índice, y valida el frontmatter de la página nueva.
3. `npm run check:limpieza` — el grep del HTML de `dist/`: cero marcadores de
   proceso filtrados. En un contrato nuevo, atención a un `(G##)`/`✅` que el
   plugin no cubriera por estar fuera de un paréntesis o de un blockquote (se
   arregla en la fuente, no relajando el gate).
4. **Lee el HTML generado de la página nueva** en `dist/docs/<slug>/`: que el H1
   salga del frontmatter (páginas locales), que la prosa quede natural tras el
   strip (contratos), y que los enlaces a docs despublicados apunten al blob de
   GitHub y los publicados sigan internos.

Recuerda: `TOTAL` y el 404 se **derivan solos** del docmap — no hay que tocarlos.
Commit en español citando la página dada de alta (o retirada).
