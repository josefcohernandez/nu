#!/usr/bin/env node
// test-limpieza.mjs — tests unitarios de los plugins `remark-limpieza-interno`
// y `remark-enlaces-wiki`. Sin dependencias nuevas: parsea markdown real con
// `unified`+`remark-parse` (ya presentes en node_modules vía Astro), aplica el
// plugin sobre el mdast y afirma sobre el resultado (texto concatenado con
// `mdast-util-to-string` o inspección estructural). Cada fixture es un ejemplo
// REAL del plan / de los contratos.
//
// Corre con `node web/scripts/test-limpieza.mjs`. El flag `--slugs` añade la
// aserción de consistencia WIKI_SLUGS ↔ docmap; el workflow docs.yml lo
// ejecuta con el flag como gate del despliegue.

import { readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { unified } from 'unified';
import remarkParse from 'remark-parse';
import { toString as mdastToString } from 'mdast-util-to-string';
import { remarkLimpiezaInterno } from '../src/lib/markdown/remark-limpieza-interno.mjs';
import { remarkEnlacesWiki } from '../src/lib/markdown/remark-enlaces-wiki.mjs';

const AQUI = dirname(fileURLToPath(import.meta.url));
const RAIZ = resolve(AQUI, '..', '..');

// Aplica el plugin sobre `md` con una ruta que cae dentro de su jurisdicción
// (un contrato del repo `docs/<x>.md`) salvo que se indique otra.
function procesa(md, path = join(RAIZ, 'docs', 'contracts', 'fixture.md')) {
  const tree = unified().use(remarkParse).parse(md);
  remarkLimpiezaInterno()(tree, { path });
  return tree;
}
const texto = (md, path) => mdastToString(procesa(md, path));
const primerHeading = (tree) => tree.children.find((n) => n.type === 'heading');

// --- Mini-runner --------------------------------------------------------------

let ok = 0;
const fallos = [];
function comprueba(nombre, cond, detalle = '') {
  if (cond) { ok++; return; }
  fallos.push(`✗ ${nombre}${detalle ? `\n    ${detalle}` : ''}`);
}
const contiene = (s, sub) => s.includes(sub);

// --- Regla D: tags en headings y texto ----------------------------------------

{
  // `### Reentrada (G4)` → `### Reentrada` (heading, con recorte del borde).
  const h = primerHeading(procesa('### Reentrada (G4)\n'));
  comprueba('D/heading: Reentrada (G4)', mdastToString(h) === 'Reentrada', `→ "${mdastToString(h)}"`);
}
{
  // `## 11. … (G14)` real de agente.md §11 (publicada): fuera el tag del título.
  const h = primerHeading(procesa('## 11. Modelo de confianza del contenido del repo (G14)\n'));
  comprueba('D/heading: §11 (G14)', mdastToString(h) === '11. Modelo de confianza del contenido del repo', `→ "${mdastToString(h)}"`);
}
{
  // `**Reentrada (G4)**: envía` (agente.md l.65): sin espacio ante el `:`.
  const t = texto('**Reentrada (G4)**: envía el mensaje.\n');
  comprueba('D/strong: Reentrada (G4): envía', contiene(t, 'Reentrada: envía') && !contiene(t, 'G4'), `→ "${t}"`);
}
{
  // Cola con separador: `(files/grep, G51)` → `(files/grep)`.
  const t = texto('Usa (files/grep, G51) hoy.\n');
  comprueba('D/cola: (files/grep, G51)', t.trim() === 'Usa (files/grep) hoy.', `→ "${t}"`);
}
{
  // Cabecera con separador y enlace/prosa a continuación: conserva la prosa.
  const t = texto('El core no sabe de agentes (el core no sabe de agentes, ADR-003) — cita.\n');
  comprueba('D/cabecera-cola: (…, ADR-003)', contiene(t, '(el core no sabe de agentes)') && !contiene(t, 'ADR-003'), `→ "${t}"`);
}
{
  // Idiomático "de <tag>": `(como la cola de G4)` → `(como la cola)`.
  const t = texto('Se ensambla la iteración (como la cola de G4) al final.\n');
  comprueba('D/de-tag: (como la cola de G4)', contiene(t, '(como la cola)') && !contiene(t, 'G4'), `→ "${t}"`);
}
{
  // Conservar prosa con coma dentro de comillas: `(ADR-004, regla "Lua decide, Go ejecuta")`.
  const t = texto('La (ADR-004, regla "Lua decide, Go ejecuta") gobierna.\n');
  comprueba('D/prosa-comillas: (ADR-004, regla "…")', contiene(t, 'regla "Lua decide, Go ejecuta"') && !contiene(t, 'ADR-004'), `→ "${t}"`);
}
{
  // Paréntesis puros de tag → fuera enteros, sin dobles espacios ni `()`.
  const t = texto('Ver (G18, G46) y (P12) aquí.\n');
  comprueba('D/puros: (G18, G46) y (P12)', t.trim() === 'Ver y aquí.' && !contiene(t, '('), `→ "${t}"`);
}
{
  // Tag intercalado en lista: se cae y conserva el resto.
  const t = texto('Sobre wazero, ADR-020, tras la retirada.\n');
  comprueba('D/lista-intercalada: , ADR-020,', contiene(t, 'wazero, tras') && !contiene(t, 'ADR-020'), `→ "${t}"`);
}

// --- Regla C: asides `*(…)*` ---------------------------------------------------

{
  // Estado con ✅ → fuera entero.
  const t = texto('Ya está. *(✅ Implementado: P23.)*\n');
  comprueba('C/aside ✅', !contiene(t, '✅') && !contiene(t, 'P23'), `→ "${t}"`);
}
{
  // Aside de PROSA que acaba en un tag (agente.md l.161): se CONSERVA.
  const md = 'Observa. *(El evento compact solo se emitirá cuando exista la compactación automática.)*\n';
  const t = texto(md);
  comprueba('C/aside-prosa se conserva', contiene(t, 'El evento compact solo se emitirá'), `→ "${t}"`);
}
{
  // Aside ✅ MULTILÍNEA (agente.md l.71-73, providers.md l.169): el salto de
  // línea suave dentro del párrafo no debe salvarlo.
  const md = 'Antes. *(✅ Implementado: [pospuesto.md](pospuesto.md) **P23**.\nEl loop drena la cola al inicio de cada iteración.)* Después.\n';
  const t = texto(md);
  comprueba('C/aside ✅ multilínea', contiene(t, 'Antes') && contiene(t, 'Después') && !contiene(t, '✅') && !contiene(t, 'drena'), `→ "${t}"`);
}

{
  // Separador `:` en cabecera (providers.md l.242): `(ADR-003: prosa)` → `(prosa)`.
  const t = texto('Va fuera del core (ADR-003: el core no sabe lo que es un LLM).\n');
  comprueba('D/cabecera-dos-puntos: (ADR-003: …)', contiene(t, '(el core no sabe lo que es un LLM)') && !contiene(t, 'ADR-003'), `→ "${t}"`);
}

// --- Regla F: comentarios en fences de código -----------------------------------

{
  // Firmas de agente.md §2: el tag vive en la cola de comentario `--`.
  const md = [
    '```',
    'Session:fork(at?: integer) ⏸ -> Session -- bifurca; copia el prefijo (G39; sesiones.md §5)',
    'Session:set_model(model: string)         -- cambio en caliente (G19)',
    'Session:close()                          -- suelta el lock de escritor (G39); síncrona',
    '```',
  ].join('\n');
  const tree = procesa(md);
  const code = tree.children.find((n) => n.type === 'code');
  comprueba('F/fence: tags de comentario fuera, código intacto',
    !/\((?:G|P|S)\d/.test(code.value) && contiene(code.value, '(sesiones.md §5)')
      && contiene(code.value, 'Session:fork(at?: integer)') && contiene(code.value, 'cambio en caliente'),
    `→ "${code.value}"`);
}
{
  // TOML de providers.md l.34: comentario `#`.
  const md = ['```toml', 'thinking = "adaptive"  # dialecto de razonamiento (ADR-016):', '```'].join('\n');
  const tree = procesa(md);
  const code = tree.children.find((n) => n.type === 'code');
  comprueba('F/toml: (ADR-016) fuera del comentario',
    !contiene(code.value, 'ADR-016') && contiene(code.value, 'thinking = "adaptive"') && contiene(code.value, 'dialecto de razonamiento'),
    `→ "${code.value}"`);
}
{
  // Flags con `--` en código NO son comentario que romper: sin tags, intactas.
  const md = ['```sh', 'nu --default-config -e "print(1)"', '```'].join('\n');
  const tree = procesa(md);
  const code = tree.children.find((n) => n.type === 'code');
  comprueba('F/flags -- intactas', code.value === 'nu --default-config -e "print(1)"', `→ "${code.value}"`);
}

// --- Regla B: blockquotes de estado -------------------------------------------

{
  const t = texto('> ✅ Implementado (P22 resuelto)\n\nContenido real.\n');
  comprueba('B/blockquote ✅', contiene(t, 'Contenido real') && !contiene(t, 'Implementado') && !contiene(t, '✅'), `→ "${t}"`);
}
{
  const t = texto('> Estado de implementación: pendiente.\n\nSigue.\n');
  comprueba('B/blockquote Estado', contiene(t, 'Sigue') && !contiene(t, 'Estado de implementación'), `→ "${t}"`);
}

// --- Regla A: rangos enu:interno -----------------------------------------------

{
  const md = [
    'Externo uno.', '',
    '<!-- enu:interno -->', '',
    '## Interno', '',
    'Párrafo interno con detalle sensible.', '',
    '<!-- /enu:interno -->', '',
    'Externo dos.', '',
  ].join('\n');
  const t = texto(md);
  comprueba('A/par simple multipárrafo',
    contiene(t, 'Externo uno') && contiene(t, 'Externo dos') && !contiene(t, 'Interno') && !contiene(t, 'sensible'),
    `→ "${t}"`);
}
{
  // Varios pares en el mismo documento.
  const md = [
    'A', '', '<!-- enu:interno -->', '', 'X1', '', '<!-- /enu:interno -->', '',
    'B', '', '<!-- enu:interno -->', '', 'X2', '', '<!-- /enu:interno -->', '', 'C', '',
  ].join('\n');
  const t = texto(md);
  comprueba('A/varios pares',
    contiene(t, 'A') && contiene(t, 'B') && contiene(t, 'C') && !contiene(t, 'X1') && !contiene(t, 'X2'),
    `→ "${t}"`);
}

// --- Jurisdicción --------------------------------------------------------------

{
  // Fuera de docs/*.md (p. ej. la referencia): el plugin no toca nada.
  const t = texto('Deja (G4) intacto.\n', join(RAIZ, 'web', 'src', 'content', 'docs', 'referencia', 'fs.md'));
  comprueba('jurisdicción/referencia intacta', contiene(t, '(G4)'), `→ "${t}"`);
}

// --- Invariante del gate: nada `(G#/(P#/(S#/(ADR-` ni ✅ tras el strip ----------

{
  const md = [
    '# Título (G14)', '',
    'Frase con (G4), y (ADR-003, más prosa) y (files/grep, G51).', '',
    'Un aside. *(✅ Implementado: P23.)*', '',
    '> ✅ Implementado (P22)', '',
    '<!-- enu:interno -->', '', 'oculto (G99)', '', '<!-- /enu:interno -->', '',
  ].join('\n');
  const t = texto(md);
  comprueba('gate/sin marcadores parentéticos',
    !/\((?:G|P|S)\d/.test(t) && !/\(ADR-\d/.test(t) && !t.includes('✅') && !t.includes('enu:interno') && !t.includes('oculto'),
    `→ "${t}"`);
}

// --- remark-enlaces-wiki: resolución de rutas y caso api.md -------------------

{
  const procesaEnlaces = (md, path) => {
    const tree = unified().use(remarkParse).parse(md);
    remarkEnlacesWiki()(tree, { path });
    const urls = [];
    (function anda(n) {
      if (n.type === 'link') urls.push(n.url);
      (n.children || []).forEach(anda);
    })(tree);
    return urls;
  };

  const desdeContracts = procesaEnlaces(
    '[a](api.md#8) [b](desconocido.md) [c](../findings/g42-x.md) [d](agente.md#2) [e](../audits/informe.md)',
    join(RAIZ, 'docs', 'contracts', 'agente.md'),
  );
  comprueba('enlaces/api.md → página /api', desdeContracts[0] === '/enu/api', `→ ${desdeContracts[0]}`);
  comprueba('enlaces/mismo-dir desconocido → blob con subcarpeta',
    desdeContracts[1] === 'https://github.com/dbareagimeno/enu/blob/main/docs/contracts/desconocido.md',
    `→ ${desdeContracts[1]}`);
  comprueba('enlaces/../findings → blob findings/',
    desdeContracts[2] === 'https://github.com/dbareagimeno/enu/blob/main/docs/findings/g42-x.md',
    `→ ${desdeContracts[2]}`);
  comprueba('enlaces/contrato publicado → página wiki con ancla',
    desdeContracts[3] === '/enu/docs/agente#2', `→ ${desdeContracts[3]}`);
  comprueba('enlaces/audits → blob resuelto',
    desdeContracts[4] === 'https://github.com/dbareagimeno/enu/blob/main/docs/audits/informe.md',
    `→ ${desdeContracts[4]}`);

  const desdeEn = procesaEnlaces(
    '[a](api.md) [b](../findings/g42-x.md) [c](chat.md)',
    join(RAIZ, 'web', 'src', 'content', 'en', 'wiki', 'agente.md'),
  );
  comprueba('enlaces/EN api.md → /en/api', desdeEn[0] === '/enu/en/api', `→ ${desdeEn[0]}`);
  comprueba('enlaces/EN findings → blob (fuente ES)',
    desdeEn[1] === 'https://github.com/dbareagimeno/enu/blob/main/docs/findings/g42-x.md',
    `→ ${desdeEn[1]}`);
  comprueba('enlaces/EN wiki → /en/docs', desdeEn[2] === '/enu/en/docs/chat', `→ ${desdeEn[2]}`);
}

// --- (opcional, tras --slugs) consistencia WIKI_SLUGS ↔ docmap ----------------

function slugsDeEnlacesWiki() {
  const src = readFileSync(join(RAIZ, 'web', 'src', 'lib', 'markdown', 'remark-enlaces-wiki.mjs'), 'utf8');
  const m = src.match(/WIKI_SLUGS\s*=\s*new Set\(\[([\s\S]*?)\]\)/);
  if (!m) throw new Error('no se encontró WIKI_SLUGS en remark-enlaces-wiki.mjs');
  return new Set([...m[1].matchAll(/'([^']+)'/g)].map((x) => x[1]));
}
function slugsDeDocmap() {
  const src = readFileSync(join(RAIZ, 'web', 'src', 'lib', 'docmap.ts'), 'utf8');
  const set = new Set();
  // Tolera ambas formas de docmap: la antigua `slugs: ['a', 'b']` y la nueva
  // por entrada `entradas: [{ slug: 'a', collection: 'wiki' }]`.
  for (const bloque of src.matchAll(/slugs:\s*\[([\s\S]*?)\]/g)) {
    for (const s of bloque[1].matchAll(/'([^']+)'/g)) set.add(s[1]);
  }
  for (const s of src.matchAll(/\bslug:\s*'([^']+)'/g)) set.add(s[1]);
  if (set.size === 0) throw new Error('no se encontraron slugs en docmap.ts');
  return set;
}

if (process.argv.includes('--slugs')) {
  try {
    const a = slugsDeEnlacesWiki();
    const b = slugsDeDocmap();
    const soloA = [...a].filter((s) => !b.has(s));
    const soloB = [...b].filter((s) => !a.has(s));
    comprueba('slugs/WIKI_SLUGS ↔ docmap',
      soloA.length === 0 && soloB.length === 0,
      `solo en enlaces-wiki: [${soloA}]; solo en docmap: [${soloB}]`);
  } catch (e) {
    comprueba('slugs/WIKI_SLUGS ↔ docmap', false, `no se pudo comparar: ${e.message}`);
  }
} else {
  console.log('  (aserción --slugs omitida: WIKI_SLUGS ↔ docmap se comprueba con `--slugs`)');
}

// --- Resultado ----------------------------------------------------------------

if (fallos.length > 0) {
  console.error(`\n${fallos.length} fallo(s) de ${ok + fallos.length} comprobaciones:\n`);
  for (const f of fallos) console.error(f);
  process.exit(1);
}
console.log(`✓ ${ok} comprobaciones en verde`);
