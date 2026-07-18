// Plugin remark de limpieza. Elimina del mdast los marcadores de PROCESO que la
// fuente (`docs/*.md`) conserva por trazabilidad —el flujo de diseño y
// `auditor-docs` los necesitan— pero que no deben aparecer en la web publicada.
// Corre ANTES de `remark-enlaces-wiki` (astro.config): así los bloques internos
// se van con sus enlaces antes de que nadie los reescriba.
//
// Jurisdicción: SOLO los contratos del repo (`docs/<x>.md`), nunca la referencia
// (`content/docs/referencia/`), ni `empezando/`, ni la colección `extensiones/`.
// Es la misma frontera que usa `remark-enlaces-wiki` para su rama "wiki del repo".
//
// Cinco reglas, todas estructurales sobre el mdast (por eso remark y no rehype):
//   A. Rangos entre `<!-- enu:interno -->` y `<!-- /enu:interno -->` (secciones
//      enteras marcadas como internas): fuera, con los comentarios incluidos.
//   B. Blockquotes de estado (`> ✅ Implementado …`, `> Estado de implementación …`).
//   C. Asides `*(…)*` de estado (✅ / Implementado / Estado / Pulido) o de puro tag.
//   D. Tags parentéticos de proceso en `text`/`heading`: `(G4)`, `(files/grep,
//      G51)`, `(… de G4)`, etc. Se limpia el marcador conservando la prosa.
//   E. Limpieza final: paréntesis vacíos, dobles espacios, espacio ante puntuación.
//   F. Colas de comentario en fences de código (`-- … (G39)`, `# … (ADR-016)`):
//      mismo strip que la prosa, sin tocar el código en sí.
//
// Política de casos borde (deliberada): un tag NO parentético y *load-bearing*
// (p. ej. un `**P25**` incrustado en una frase de producto) NO lo reescribe el
// plugin —borrar la frase perdería significado—; eso se arregla en la fuente. El
// gate `check-limpieza-html.mjs` solo exige que no queden marcadores parentéticos
// `(G#/(P#/(S#/(ADR-`, ni `✅`, ni `enu:interno` en el HTML final.

import { toString as mdastToString } from 'mdast-util-to-string';

// Formas de tag que el flujo de diseño acuña (ver CLAUDE.md, "Glosario de
// prefijos"): G##/P##/S## (grietas, pospuestos, sesiones), ADR-NNN y A-## (audits).
const TAG = String.raw`(?:G\d+|P\d+|S\d+|ADR-\d+|A-\d+)`;
// Una lista de tags separados por coma/punto y coma: `G18, G46`, `ADR-017/G35`.
const TAGLIST = `${TAG}(?:\\s*[,;/]\\s*${TAG})*`;
const RE_TAG = new RegExp(TAG);

const RE_APERTURA = /<!--\s*enu:interno\s*-->/;
const RE_CIERRE = /<!--\s*\/enu:interno\s*-->/;

// --- Regla D: strip de tags parentéticos sobre el valor de un nodo `text` ------

// Cada pasada es un reemplazo dirigido a una posición del tag dentro del texto.
// El orden importa: primero paréntesis puros y cabeceras, luego el idiomático
// "de <tag>", y por último las colas; así "(… de G4)" no se rompe en "(… de)".
const PASADAS = [
  // 1. Paréntesis cuyo contenido es SOLO tags → fuera entero: `(G4)`, `(G18, G46)`.
  [new RegExp(`\\(\\s*${TAGLIST}\\s*\\)`, 'g'), ''],
  // 2. Cabecera de paréntesis con separador: `(ADR-003, [link]…)` → `([link]…)`,
  //    incluido el separador `:` de `(ADR-003: el core no sabe…)`.
  [new RegExp(`\\(\\s*${TAGLIST}\\s*[,;:]\\s*`, 'g'), '('],
  // 2c. Cabecera seguida de prosa sin separador: `(G4 y otro)` → `(y otro)`.
  [new RegExp(`\\(\\s*${TAGLIST}\\s+`, 'g'), '('],
  // 4. Idiomático "de <tag>": `(como la cola de G4)` → `(como la cola)`.
  [new RegExp(`\\s+de\\s+${TAGLIST}\\b`, 'g'), ''],
  // 3. Cola de paréntesis con separador: `(files/grep, G51)` → `(files/grep)`.
  [new RegExp(`\\s*[,;]\\s*${TAGLIST}\\s*\\)`, 'g'), ')'],
  // 3c. Cola sin separador: `(algo G16)` → `(algo)`.
  [new RegExp(`\\s+${TAGLIST}\\s*\\)`, 'g'), ')'],
  // 2b. Tag intercalado en una lista: `…wazero, ADR-020, tras…` → `…wazero, tras…`.
  [new RegExp(`[,;]\\s*${TAGLIST}\\s*(?=[,;])`, 'g'), ''],
];

function stripTags(valor) {
  if (!valor || !RE_TAG.test(valor)) return valor; // atajo: sin tags, nada que hacer
  let v = valor;
  for (const [re, rep] of PASADAS) v = v.replace(re, rep);
  // Regla E: paréntesis que quedaron vacíos, espacios dobles y espacio ante
  // puntuación (solo espacios/tabs, para no colapsar saltos de línea suaves).
  v = v.replace(/\(\s*\)/g, '');
  v = v.replace(/[ \t]{2,}/g, ' ');
  v = v.replace(/[ \t]+([,.;:!?])/g, '$1');
  return v;
}

// --- Regla F: tags en comentarios de fences de código ---------------------------

// Las firmas de los contratos llevan trazabilidad en sus comentarios
// (`Session:close()  -- suelta el lock (G39)`, `# dialecto (ADR-016):`). El
// código en sí no se toca: solo la cola de comentario (`--` Lua, `#` TOML/sh)
// pasa por el mismo strip que la prosa.
function stripTagsCodigo(valor) {
  if (!valor || !RE_TAG.test(valor)) return valor;
  return valor
    .split('\n')
    .map((linea) => {
      const idx = linea.search(/--|#/);
      if (idx < 0) return linea;
      return linea.slice(0, idx) + stripTags(linea.slice(idx));
    })
    .join('\n');
}

// --- Regla B: blockquotes de estado -------------------------------------------

const RE_ESTADO = /^(Estado de implementación|Implementado|Pulido de producto)/;

function esBlockquoteEstado(nodo) {
  if (nodo.type !== 'blockquote') return false;
  const texto = mdastToString(nodo).trim();
  return texto.includes('✅') || RE_ESTADO.test(texto);
}

// --- Regla C: asides `*(…)*` de estado o de puro tag --------------------------

// Solo se elimina un aside de PROCESO: marca de estado (✅ / Implementado / …) o
// paréntesis cuyo contenido es puramente tags. Un aside con prosa que *acaba*
// mencionando un tag (p. ej. "(El evento compact solo se emitirá … P25.)") se
// conserva: destruirlo perdería documentación de producto (ver política borde).
function esEmphasisAside(nodo) {
  if (nodo.type !== 'emphasis') return false;
  const s = mdastToString(nodo).trim();
  // [\s\S] y no `.`: un aside puede ocupar varias líneas de la fuente (salto
  // suave dentro del párrafo) y `.` no cruza el \n.
  if (!/^\(([\s\S]*)\)$/.test(s)) return false;
  const interior = s.slice(1, -1).trim();
  if (s.includes('✅') || RE_ESTADO.test(interior)) return true;
  // Puro tag: `(P23)`, `(G4, G5)`.
  return new RegExp(`^${TAGLIST}$`).test(interior);
}

// --- Regla A: rangos internos (nivel de bloque en tree.children) --------------

function eliminaRangosInternos(tree) {
  if (!Array.isArray(tree.children)) return;
  const out = [];
  let dentro = false;
  for (const nodo of tree.children) {
    if (nodo.type === 'html' && typeof nodo.value === 'string') {
      if (RE_CIERRE.test(nodo.value)) { dentro = false; continue; } // se traga el cierre
      if (RE_APERTURA.test(nodo.value)) { dentro = true; continue; } // y la apertura
    }
    if (!dentro) out.push(nodo);
  }
  tree.children = out;
}

// --- Recorrido para B, C y D ---------------------------------------------------

const CONTENEDORES_TITULO = new Set(['heading', 'strong', 'emphasis', 'link']);

// Recorta el espacio que el strip pudo dejar en los bordes de un contenedor con
// título (un `## Título (G14)` → "Título ", o un `**Reentrada (G4)**` → "Reentrada
// "): sin esto quedaría un espacio antes del `:` o `.` que sigue al `**…**`.
function recortaBordes(nodo) {
  const hijos = nodo.children;
  const primero = hijos[0];
  const ultimo = hijos[hijos.length - 1];
  if (primero && primero.type === 'text') primero.value = primero.value.replace(/^\s+/, '');
  if (ultimo && ultimo.type === 'text') ultimo.value = ultimo.value.replace(/\s+$/, '');
}

function limpia(nodo) {
  if (!nodo || typeof nodo !== 'object' || !Array.isArray(nodo.children)) return;
  // B y C: se filtran los hijos que sean blockquote de estado o aside de proceso.
  nodo.children = nodo.children.filter(
    (h) => !esBlockquoteEstado(h) && !esEmphasisAside(h),
  );
  for (const h of nodo.children) {
    if (h.type === 'text') h.value = stripTags(h.value || ''); // D
    else if (h.type === 'code' || h.type === 'inlineCode') h.value = stripTagsCodigo(h.value || ''); // F
    else limpia(h);
  }
  if (CONTENEDORES_TITULO.has(nodo.type) && nodo.children.length > 0) recortaBordes(nodo);
}

export function remarkLimpiezaInterno() {
  return (tree, file) => {
    const ruta = (file?.path || file?.history?.[0] || '').replace(/\\/g, '/');
    const esWikiRepo = /\/docs\/(core|contracts)\/[^/]+\.md$/.test(ruta) && !ruta.includes('/content/docs/');
    // La instantánea EN de la wiki conserva los marcadores de proceso
    // (<!-- enu:interno -->, (G##), > ✅ …) igual que la fuente ES: se limpia con
    // el mismo criterio, para que las páginas /en/docs no filtren trazabilidad.
    const esWikiEn = ruta.includes('/content/en/wiki/');
    if (!esWikiRepo && !esWikiEn) return; // fuera de jurisdicción: no se toca

    eliminaRangosInternos(tree); // A primero: rangos enteros con sus enlaces
    limpia(tree); // B, C, D (y E dentro de stripTags)
  };
}
