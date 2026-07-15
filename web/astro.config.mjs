// @ts-check
import { defineConfig } from 'astro/config';
import { remarkLimpiezaInterno } from './src/lib/markdown/remark-limpieza-interno.mjs';
import { remarkEnlacesWiki } from './src/lib/markdown/remark-enlaces-wiki.mjs';
import { rehypeApiCards } from './src/lib/markdown/rehype-api-cards.mjs';

// El sitio se publica bajo /nu/ en GitHub Pages (project page). Si se sirve en
// un dominio propio, basta con vaciar `base` y ajustar `site`.
//
// Sin integraciones: el sitio es 100% custom ("la web ES un terminal"). El
// resaltado de sintaxis usa el tema `css-variables` de Shiki para que los
// colores salgan de las variables CSS del theme activo (ver src/styles).
export default defineConfig({
  site: 'https://dbareagimeno.github.io',
  // Con barra final: import.meta.env.BASE_URL la conserva, y todo el código
  // enlaza concatenando `${BASE}ruta` (sin barra se generarían /nudocs, etc.).
  base: '/nu/',
  markdown: {
    // Limpieza PRIMERO: los bloques internos se van con sus enlaces antes de
    // que remark-enlaces-wiki los reescriba.
    remarkPlugins: [remarkLimpiezaInterno, remarkEnlacesWiki],
    rehypePlugins: [rehypeApiCards],
    shikiConfig: {
      theme: 'css-variables',
    },
  },
});
