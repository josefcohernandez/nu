// Plugin remark de la wiki. Dos transformaciones sobre el mdast, y SOLO sobre
// los ficheros de la wiki (docs/*.md del repo), de `empezar`
// (web/src/content/docs/empezando/*.md) y de `extensiones`
// (web/src/content/docs/extensiones/*.md). NUNCA toca la referencia
// (web/src/content/docs/referencia/*.md): de esos se encarga rehype-api-cards.
//
//  1. Enlaces `*.md` relativos entre documentos → `<BASE>/docs/<slug>[#ancla]`.
//     Enlaces a `audits/…` o `archive/…` (fuera de la wiki) y `.md` desconocidos
//     → blob de GitHub. Enlaces con prefijo `docs/` (extraídos del README) →
//     también a `/docs/<slug>`.
//  2. Bloques ```mermaid → `<pre class="mermaid">` con la fuente escapada, para
//     que Shiki no los toque y el cliente (mermaid-init.ts) los renderice.
//
// BASE es '/nu' (coincide con `base` en astro.config.mjs); en un plugin remark
// no hay acceso fiable a import.meta.env, así que se fija aquí.

const BASE = '/nu';
const GH_BLOB = 'https://github.com/dbareagimeno/nu/blob/main/docs/';

// Los 18 slugs publicados como página en /docs/<slug> (docmap.ts es la fuente;
// aquí se replican porque el plugin corre en el pipeline de build, fuera de
// astro:content). Los enlaces a docs despublicados (api, adr, malla y todo el
// grupo proceso) caen a GitHub blob por la rama de `.md` desconocido.
const WIKI_SLUGS = new Set([
  'que-es-nu', 'instalacion', 'inicio-rapido', 'primer-script', 'primer-agente', 'conceptos',
  'filosofia', 'arquitectura', 'modelo-ejecucion',
  'extensiones', 'guia-plugins', 'providers', 'agente', 'sesiones', 'chat', 'mcp', 'repl', 'toolkit',
]);

function escHtml(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

// Quita prefijos relativos (./  ../) repetidos y un `docs/` inicial.
function limpiaRuta(r) {
  return r.replace(/^(\.\.?\/)+/, '').replace(/^docs\//, '');
}

function reescribeUrl(url) {
  if (!url) return url;
  // Absolutos, anclas puras, mailto, protocolos: intactos.
  if (/^(https?:|mailto:|tel:|#|\/)/i.test(url)) return url;

  const idx = url.indexOf('#');
  const ruta = idx >= 0 ? url.slice(0, idx) : url;
  const hash = idx >= 0 ? url.slice(idx) : '';
  if (!ruta) return url;

  // audits/ y archive/ viven fuera de la wiki: al blob de GitHub.
  if (/(^|\/)(audits|archive)\//.test(ruta)) {
    return GH_BLOB + limpiaRuta(ruta) + hash;
  }

  if (/\.md$/i.test(ruta)) {
    const limpio = limpiaRuta(ruta);
    const slug = limpio.replace(/\.md$/i, '').split('/').pop();
    if (WIKI_SLUGS.has(slug)) return `${BASE}/docs/${slug}${hash}`;
    // .md que no es página de la wiki (README.md, ficheros sueltos): a GitHub.
    return GH_BLOB + limpio + hash;
  }

  return url;
}

// Recorre el árbol una sola vez aplicando ambas transformaciones.
function recorre(node) {
  if (!node || typeof node !== 'object') return;

  if (node.type === 'link' && typeof node.url === 'string') {
    node.url = reescribeUrl(node.url);
  } else if (node.type === 'code' && node.lang === 'mermaid') {
    // Convierte el bloque en HTML crudo; Shiki ya no lo procesará.
    node.type = 'html';
    node.value = `<pre class="mermaid" data-mermaid>${escHtml(node.value || '')}</pre>`;
    delete node.lang;
    delete node.meta;
    return; // ya no tiene hijos relevantes
  }

  if (Array.isArray(node.children)) {
    for (const hijo of node.children) recorre(hijo);
  }
}

export function remarkEnlacesWiki() {
  return (tree, file) => {
    const ruta = (file?.path || file?.history?.[0] || '').replace(/\\/g, '/');
    const esReferencia = ruta.includes('/content/docs/referencia/');
    if (esReferencia) return; // la transforma rehype-api-cards, no nosotros

    const esEmpezar = ruta.includes('/content/docs/empezando/');
    const esExtensiones = ruta.includes('/content/docs/extensiones/');
    const esWikiRepo = /\/docs\/[^/]+\.md$/.test(ruta) && !ruta.includes('/content/docs/');
    if (!esEmpezar && !esExtensiones && !esWikiRepo) return; // fuera de nuestra jurisdicción

    recorre(tree);
  };
}
