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
// BASE es '/enu' (coincide con `base` en astro.config.mjs); en un plugin remark
// no hay acceso fiable a import.meta.env, así que se fija aquí.

const BASE = '/enu';
const GH_BLOB = 'https://github.com/dbareagimeno/enu/blob/main/docs/';

// Los 18 slugs publicados como página en /docs/<slug> (docmap.ts es la fuente;
// aquí se replican porque el plugin corre en el pipeline de build, fuera de
// astro:content). Los enlaces a docs despublicados (api, adr, malla y todo el
// grupo proceso) caen a GitHub blob por la rama de `.md` desconocido.
const WIKI_SLUGS = new Set([
  'que-es-enu', 'instalacion', 'inicio-rapido', 'primer-script', 'primer-agente', 'conceptos',
  'filosofia', 'arquitectura', 'modelo-ejecucion',
  'extensiones', 'guia-plugins', 'providers', 'agente', 'sesiones', 'chat', 'mcp', 'repl', 'toolkit',
]);

function escHtml(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

// Resuelve una ruta relativa contra el directorio del fichero fuente DENTRO
// de docs/ (`dirRepo`: '', 'core' o 'contracts'), devolviendo la ruta
// docs-relativa real. Un `..` que se sale de docs/ se recorta (mismo clamp
// que hacía el viejo limpiaRuta); un prefijo `docs/` explícito se pela.
// Sin esto, un enlace del mismo directorio ([x.md](x.md) en contracts/)
// perdería su subcarpeta camino del blob de GitHub.
function resuelveRuta(dirRepo, r) {
  const partes = [];
  for (const seg of `${dirRepo ? dirRepo + '/' : ''}${r}`.split('/')) {
    if (!seg || seg === '.') continue;
    if (seg === '..') partes.pop();
    else partes.push(seg);
  }
  if (partes[0] === 'docs') partes.shift();
  return partes.join('/');
}

// `prefLang` es '' (ES) o 'en/' (EN): las páginas EN publican sus enlaces de
// wiki bajo /enu/en/docs/<slug>, no /enu/docs/<slug> (W-04). Los enlaces a GitHub
// (audits/archive/.md despublicado) son idénticos en ambos idiomas: apuntan a
// la fuente ES, que es la de verdad.
function reescribeUrl(url, prefLang, dirRepo) {
  if (!url) return url;
  // Absolutos, anclas puras, mailto, protocolos: intactos.
  if (/^(https?:|mailto:|tel:|#|\/)/i.test(url)) return url;

  const idx = url.indexOf('#');
  const ruta = idx >= 0 ? url.slice(0, idx) : url;
  const hash = idx >= 0 ? url.slice(idx) : '';
  if (!ruta) return url;

  // audits/ y archive/ viven fuera de la wiki: al blob de GitHub.
  if (/(^|\/)(audits|archive)\//.test(ruta)) {
    return GH_BLOB + resuelveRuta(dirRepo, ruta) + hash;
  }

  if (/\.md$/i.test(ruta)) {
    const resuelto = resuelveRuta(dirRepo, ruta);
    const slug = resuelto.replace(/\.md$/i, '').split('/').pop();
    // api.md no es página de la wiki: su presentación web es la referencia
    // /api. Mandar al visitante ahí es mejor UX que el markdown crudo (y el
    // ancla §N del fuente no existe en /api, así que se descarta).
    if (slug === 'api') return `${BASE}/${prefLang}api`;
    if (WIKI_SLUGS.has(slug)) return `${BASE}/${prefLang}docs/${slug}${hash}`;
    // .md que no es página de la wiki (README.md, registros de capa 2): a GitHub.
    return GH_BLOB + resuelto + hash;
  }

  return url;
}

// Recorre el árbol una sola vez aplicando ambas transformaciones.
function recorre(node, prefLang, dirRepo) {
  if (!node || typeof node !== 'object') return;

  if (node.type === 'link' && typeof node.url === 'string') {
    node.url = reescribeUrl(node.url, prefLang, dirRepo);
  } else if (node.type === 'code' && node.lang === 'mermaid') {
    // Convierte el bloque en HTML crudo; Shiki ya no lo procesará.
    node.type = 'html';
    node.value = `<pre class="mermaid" data-mermaid>${escHtml(node.value || '')}</pre>`;
    delete node.lang;
    delete node.meta;
    return; // ya no tiene hijos relevantes
  }

  if (Array.isArray(node.children)) {
    for (const hijo of node.children) recorre(hijo, prefLang, dirRepo);
  }
}

export function remarkEnlacesWiki() {
  return (tree, file) => {
    const ruta = (file?.path || file?.history?.[0] || '').replace(/\\/g, '/');
    // La referencia (ES o EN) la transforma rehype-api-cards, no nosotros.
    const esReferencia = ruta.includes('/referencia/');
    if (esReferencia) return;

    // Jurisdicción: contenido ES (empezar/extensiones locales + wiki del repo) y
    // su instantánea EN bajo content/en/ (empezar/extensiones/wiki). Cada uno
    // reescribe sus enlaces de wiki al idioma que le corresponde.
    const esEn = ruta.includes('/content/en/');
    const esEmpezar =
      ruta.includes('/content/docs/empezando/') || ruta.includes('/content/en/empezando/');
    const esExtensiones =
      ruta.includes('/content/docs/extensiones/') || ruta.includes('/content/en/extensiones/');
    const esWikiRepo = /\/docs\/(core|contracts)\/[^/]+\.md$/.test(ruta) && !ruta.includes('/content/');
    const esWikiEn = ruta.includes('/content/en/wiki/');
    if (!esEmpezar && !esExtensiones && !esWikiRepo && !esWikiEn) return; // fuera de jurisdicción

    // Directorio del fichero dentro de docs/ (para resolver enlaces relativos
    // del mismo directorio). El contenido local y la instantánea EN escriben
    // sus enlaces docs-relativos, así que su base es la raíz de docs/.
    const dirRepo = esWikiRepo ? (ruta.match(/\/docs\/(core|contracts)\/[^/]+\.md$/)?.[1] ?? '') : '';

    recorre(tree, esEn ? 'en/' : '', dirRepo);
  };
}
