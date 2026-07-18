// Plugin rehype: en las páginas de la referencia (web/src/content/docs/
// referencia/*.md, y SOLO en ellas — no-op fuera de esa ruta), transforma el
// patrón "fence de firma sin etiqueta + descripción siguiente" en la card de
// función del diseño 13a, y etiqueta los ejemplos ```lua/```sh. No altera los
// ficheros fuente: check-drift.mjs los sigue parseando tal cual (compara .md vs
// docs/contracts/api.md, nunca este HTML).
//
// Corre DESPUÉS del resaltado de Shiki (así lo ordena @astrojs/markdown-remark:
// rehypeShiki y luego los rehypePlugins del usuario). Por eso cada fence llega
// como <pre data-language="lua|sh|plaintext">. La firma se re-tokeniza desde el
// texto crudo del fence (Shiki solo lo marca como plaintext); los ejemplos
// conservan su resaltado de Shiki y solo se envuelven con una etiqueta.
//
// Detección firma vs ejemplo (misma heurística que check-drift.mjs):
//   - fence con data-language lua/sh/bash  -> EJEMPLO (se envuelve, no card).
//   - fence sin etiqueta (plaintext): es FIRMA si alguna de sus líneas parsea
//     como callable (empieza por `enu.` o `Handle:metodo`, con `(...)` y/o
//     `-> ret`). Si ninguna parsea (salidas como `true`, `{...}`, los modos de
//     la CLI) se deja como bloque de código tal cual.

import { toText } from 'hast-util-to-text';

const SUSP = '⏸';
const W = '[W]';
// Cabeza de un callable: nombre punteado bajo `enu.` o método `Handle:metodo`.
const RE_CABEZA = /^(enu\.[\w./]+|[A-Z][A-Za-z]*:[a-z_]\w*)/;

// --- Helpers de parseo (portados de check-drift.mjs) ------------------------

function cierreBalanceado(s, i = 0) {
  let prof = 0;
  for (; i < s.length; i++) {
    if (s[i] === '(') prof++;
    else if (s[i] === ')' && --prof === 0) return i;
  }
  return -1;
}

// Divide por " / " a nivel superior (fuera de paréntesis).
function segmentos(linea) {
  const out = [];
  let prof = 0,
    ini = 0;
  for (let i = 0; i < linea.length; i++) {
    const ch = linea[i];
    if ('([{'.includes(ch)) prof++;
    else if (')]}'.includes(ch)) prof--;
    else if (ch === '/' && prof === 0 && linea[i - 1] === ' ' && linea[i + 1] === ' ') {
      out.push(linea.slice(ini, i));
      ini = i + 1;
    }
  }
  out.push(linea.slice(ini));
  return out;
}

// ¿Este segmento (ya sin comentario/markers) parece una firma?
function segmentoEsFirma(seg) {
  let s = seg.trim().replaceAll(SUSP, '').replaceAll(W, '').replaceAll('\\|', '|').trim();
  const m = s.match(RE_CABEZA);
  if (!m) return false;
  let resto = s.slice(m[1].length);
  let tieneArgs = false;
  if (resto.startsWith('(')) {
    const fin = cierreBalanceado(resto, 0);
    if (fin === -1) return false;
    tieneArgs = true;
    resto = resto.slice(fin + 1);
  }
  const tieneRet = /^\s*->/.test(resto);
  if (!tieneRet && resto.trim() !== '') return false; // basura tras la firma
  return tieneArgs || tieneRet;
}

// ¿Alguna línea del fence es una firma? (decide card vs bloque de salida)
function fenceEsFirma(texto) {
  for (const linea of texto.split('\n')) {
    const sinComentario = linea.split(/\s+--\s/)[0];
    for (const seg of segmentos(sinComentario)) {
      if (segmentoEsFirma(seg)) return true;
    }
  }
  return false;
}

// --- Construcción de nodos hast ---------------------------------------------

function span(clase, valor) {
  return {
    type: 'element',
    tagName: 'span',
    properties: { className: [clase] },
    children: [{ type: 'text', value: valor }],
  };
}

// Nombres de callable que nombra un heading (para contagiar ⏸/[W]).
function codigosDe(nodo) {
  const r = [];
  for (const c of nodo.children || []) {
    if (c.type === 'element' && c.tagName === 'code') r.push(toText(c));
  }
  return r.filter((n) => RE_CABEZA.test(n));
}

function infoHeading(nodo) {
  const texto = toText(nodo);
  return { nombres: codigosDe(nodo), susp: texto.includes(SUSP), w: texto.includes(W) };
}

// Tokeniza UNA línea de firma en spans coloreados. `heading` contagia
// marcadores solo si nombra el callable (o un prefijo de módulo suyo), igual
// que check-drift.
function filaFirma(lineaCruda, heading) {
  const indent = (lineaCruda.match(/^(\s*)/) || ['', ''])[1].length;
  let s = lineaCruda.slice(indent);

  // Comentario de cola ("  -- ...").
  let comentario = null;
  const cm = s.match(/\s+--\s(.*)$/);
  if (cm) {
    comentario = '-- ' + cm[1].trim();
    s = s.slice(0, cm.index);
  }

  // Marcadores inline.
  const suspInline = s.includes(SUSP);
  const wInline = s.includes(W);
  s = s.replaceAll(SUSP, '').replaceAll(W, '').replace(/\s+$/, '');

  const spans = [];
  const m = s.match(RE_CABEZA);
  let nombre = null;
  if (m) {
    nombre = m[1];
    let resto = s.slice(nombre.length);
    let args = null;
    if (resto.startsWith('(')) {
      const fin = cierreBalanceado(resto, 0);
      if (fin !== -1) {
        args = resto.slice(0, fin + 1);
        resto = resto.slice(fin + 1);
      }
    }
    spans.push(span('sig-name', nombre));
    if (args !== null) spans.push(span('sig-args', args));
    const mr = resto.match(/^\s*->\s*(.+)$/);
    if (mr) spans.push(span('sig-ret', ' → ' + mr[1].trim()));
    else if (resto.trim() !== '') spans.push(span('sig-ret', ' ' + resto.trim()));
  } else {
    // Línea que no parsea como callable (p. ej. "Stream.status / Stream.headers").
    spans.push(span('sig-plain', s.trim()));
  }

  // Marcadores efectivos: inline, o del heading si nombra este callable.
  const nombrado =
    nombre && heading.nombres.some((hn) => hn === nombre || nombre.startsWith(hn + '.'));
  const susp = suspInline || (nombrado && heading.susp);
  const w = wInline || (nombrado && heading.w);
  const marks = [];
  if (susp) marks.push(SUSP);
  if (w) marks.push(W);
  if (marks.length) spans.push(span('sig-mark', ' · ' + marks.join(' · ')));
  if (comentario) spans.push(span('sig-comment', '  ' + comentario));

  const props = { className: ['sig-row'] };
  if (indent > 0) props.className.push('sig-indent');
  return { type: 'element', tagName: 'div', properties: props, children: spans };
}

function tarjeta(textoFence, desc, heading) {
  const filas = textoFence
    .split('\n')
    .filter((l) => l.trim() !== '')
    .map((l) => filaFirma(l, heading));
  const sig = {
    type: 'element',
    tagName: 'div',
    properties: { className: ['api-card-sig'] },
    children: filas,
  };
  const children = [sig];
  const descVisible = desc.filter((n) => !(n.type === 'text' && n.value.trim() === ''));
  if (descVisible.length) {
    children.push({
      type: 'element',
      tagName: 'div',
      properties: { className: ['api-card-desc'] },
      children: desc,
    });
  }
  return {
    type: 'element',
    tagName: 'div',
    properties: { className: ['api-card'] },
    children,
  };
}

function envolverEjemplo(pre) {
  const texto = toText(pre, { whitespace: 'pre' });
  const esNuE = /\bnu\s+-e\b/.test(texto);
  const etiqueta = esNuE ? 'ejemplo — pruébalo con enu -e' : 'ejemplo';
  const cap = {
    type: 'element',
    tagName: 'figcaption',
    properties: { className: ['api-ejemplo-label'] },
    children: [{ type: 'text', value: etiqueta }],
  };
  return {
    type: 'element',
    tagName: 'figure',
    properties: { className: ['api-ejemplo'] },
    children: [cap, pre],
  };
}

function langDe(pre) {
  const dl = pre.properties && pre.properties.dataLanguage;
  if (typeof dl === 'string') return dl;
  const cls = pre.properties && pre.properties.className;
  const arr = Array.isArray(cls) ? cls : typeof cls === 'string' ? [cls] : [];
  for (const c of arr) {
    const m = /\blanguage-(\S+)/.exec(c);
    if (m) return m[1];
  }
  return 'plaintext';
}

// --- Plugin ------------------------------------------------------------------

export function rehypeApiCards() {
  return (tree, file) => {
    const path = String((file && (file.path || (file.history && file.history[0]))) || '');
    if (!path.includes('/referencia/')) return; // no-op fuera de la referencia
    if (!Array.isArray(tree.children)) return;

    const hijos = tree.children;
    const out = [];
    let heading = { nombres: [], susp: false, w: false };

    for (let i = 0; i < hijos.length; i++) {
      const n = hijos[i];
      if (n.type === 'element' && /^h[1-6]$/.test(n.tagName)) {
        heading = infoHeading(n);
        // El heading que nombra funciones no se pinta (la card ya muestra el
        // identificador): se colapsa a un ancla invisible para #id y el §.
        if (heading.nombres.length) {
          const cls = n.properties.className;
          n.properties.className = (Array.isArray(cls) ? cls : cls ? [cls] : []).concat(
            'api-fn-heading'
          );
        }
        out.push(n);
        continue;
      }
      if (n.type === 'element' && n.tagName === 'pre') {
        const lang = langDe(n);
        if (lang === 'lua' || lang === 'sh' || lang === 'bash') {
          out.push(envolverEjemplo(n));
          continue;
        }
        // Sin etiqueta (plaintext): ¿firma o salida?
        const texto = toText(n, { whitespace: 'pre' });
        if (fenceEsFirma(texto)) {
          // Descripción = hermanos siguientes hasta el próximo heading o fence.
          const desc = [];
          let j = i + 1;
          for (; j < hijos.length; j++) {
            const m = hijos[j];
            if (m.type === 'element' && (/^h[1-6]$/.test(m.tagName) || m.tagName === 'pre')) break;
            desc.push(m);
          }
          i = j - 1;
          out.push(tarjeta(texto, desc, heading));
          continue;
        }
        out.push(n); // bloque de salida: se deja tal cual
        continue;
      }
      out.push(n);
    }
    tree.children = out;
  };
}
