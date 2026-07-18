#!/usr/bin/env node
// check-limpieza-html.mjs — gate de la limpieza de marcadores de proceso. Dos
// modos, ambos mecánicos y deterministas (como check-drift.mjs):
//
//   (por defecto) — POST-BUILD: recorre `web/dist/docs/**/*.html` y falla si
//     encuentra un marcador de proceso que el plugin `remark-limpieza-interno`
//     debería haber eliminado: un tag parentético `(G#/(P#/(S#/(ADR-`, un `✅`,
//     o el propio comentario `enu:interno`. Lista fichero + patrón + fragmento.
//
//   --fuente — PRE-BUILD: sobre los `.md` de `docs/` verifica el BALANCEO de los
//     pares `<!-- enu:interno -->` / `<!-- /enu:interno -->`: cada apertura tiene su
//     cierre, sin anidar. Un descuadre dejaría una sección interna publicada (o
//     comería contenido bueno). Lista fichero:línea del descuadre.
//
// Sin dependencias; corre con `node web/scripts/check-limpieza-html.mjs [--fuente]`
// o `npm run check:limpieza[:fuente]`.

import { readFileSync, readdirSync } from 'node:fs';
import { dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const RAIZ = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..');
// La wiki publicada en ambos idiomas: /docs (ES) y /en/docs (EN). La instantánea
// EN conserva los marcadores de proceso en la fuente y los limpia el mismo
// plugin (remark-limpieza-interno), así que el gate cubre las dos.
const DIST_DOCS = [join(RAIZ, 'web', 'dist', 'docs'), join(RAIZ, 'web', 'dist', 'en', 'docs')];
const DOCS = join(RAIZ, 'docs');

// --- Modo --fuente: balanceo de los pares en docs/*.md ------------------------

// Un marcador ESTRUCTURAL es un comentario en flujo: la única cosa de su línea
// (así lo ve el mdast, y así lo elimina Rule A del plugin). Una mención inline en
// prosa —p. ej. `<!-- enu:interno -->` entre backticks en docs/README.md— NO es un
// marcador y no cuenta para el balanceo.
const RE_APERTURA_SOLA = /^<!--\s*enu:interno\s*-->$/;
const RE_CIERRE_SOLA = /^<!--\s*\/enu:interno\s*-->$/;

// docs/ se organiza en subcarpetas por capas: el barrido es recursivo.
function mdRecursivo(dir) {
  const out = [];
  for (const e of readdirSync(dir, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
    const p = join(dir, e.name);
    if (e.isDirectory()) out.push(...mdRecursivo(p));
    else if (e.name.endsWith('.md')) out.push(p);
  }
  return out;
}

function verificaFuente() {
  const fallos = [];
  for (const ruta of mdRecursivo(DOCS)) {
    const fichero = relative(DOCS, ruta);
    const lineas = readFileSync(ruta, 'utf8').split('\n');
    let abiertaEn = 0; // nº de línea (1-based) de la apertura sin cerrar, o 0
    lineas.forEach((linea, i) => {
      const n = i + 1;
      const l = linea.trim();
      if (RE_CIERRE_SOLA.test(l)) {
        if (abiertaEn === 0) fallos.push(`${fichero}:${n} — cierre enu:interno sin apertura`);
        abiertaEn = 0;
      } else if (RE_APERTURA_SOLA.test(l)) {
        if (abiertaEn !== 0) fallos.push(`${fichero}:${n} — apertura enu:interno anidada (la de :${abiertaEn} sigue abierta)`);
        abiertaEn = n;
      }
    });
    if (abiertaEn !== 0) fallos.push(`${fichero}:${abiertaEn} — apertura enu:interno sin cierre al EOF`);
  }
  return fallos;
}

// --- Modo por defecto: marcadores prohibidos en el HTML final -----------------

// Patrones que NO deben sobrevivir al render. `\((?:G|P|S)\d` y `\(ADR-\d` cazan
// el tag pegado al paréntesis (los enlazados a GitHub blob no lo están, y son
// referencias navegacionales legítimas: no disparan). `✅` y `enu:interno` nunca.
const PROHIBIDOS = [
  { nombre: 'tag (G#/(P#/(S#', re: /\((?:G|P|S)\d/ },
  { nombre: 'tag (ADR-#', re: /\(ADR-\d/ },
  { nombre: 'marca ✅', re: /✅/ },
  { nombre: 'comentario enu:interno', re: /enu:interno/ },
];

function ficherosHtml(dir) {
  let entradas;
  try {
    entradas = readdirSync(dir, { withFileTypes: true, recursive: true });
  } catch {
    console.error(`No existe ${dir}: ¿se ha ejecutado el build (npm run build) antes del gate?`);
    process.exit(1);
  }
  return entradas
    .filter((e) => e.isFile() && e.name.endsWith('.html'))
    .map((e) => join(e.parentPath || e.path, e.name));
}

function verificaHtml() {
  const fallos = [];
  const ficheros = DIST_DOCS.flatMap((d) => ficherosHtml(d));
  for (const ruta of ficheros) {
    const html = readFileSync(ruta, 'utf8');
    const rel = ruta.slice(RAIZ.length + 1);
    for (const { nombre, re } of PROHIBIDOS) {
      const m = html.match(re);
      if (m) {
        const ini = Math.max(0, m.index - 30);
        const frag = html.slice(ini, m.index + 30).replace(/\s+/g, ' ');
        fallos.push(`${rel} — ${nombre} — …${frag}…`);
      }
    }
  }
  return { fallos, total: ficheros.length };
}

// --- Ejecución ----------------------------------------------------------------

if (process.argv.includes('--fuente')) {
  const fallos = verificaFuente();
  if (fallos.length > 0) {
    console.error(`Pares enu:interno descuadrados en docs/ (${fallos.length}):\n`);
    for (const f of fallos) console.error('  ' + f);
    process.exit(1);
  }
  console.log('✓ pares enu:interno balanceados en docs/');
} else {
  const { fallos, total } = verificaHtml();
  if (fallos.length > 0) {
    console.error(`Marcadores de proceso filtrados al HTML publicado (${fallos.length}):\n`);
    for (const f of fallos) console.error('  ' + f);
    console.error('\nEl plugin remark-limpieza-interno debe eliminarlos; si es prosa load-bearing, arréglalo en la fuente.');
    process.exit(1);
  }
  console.log(`✓ sin marcadores de proceso en dist/docs y dist/en/docs (${total} HTML comprobados)`);
}
