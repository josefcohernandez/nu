#!/usr/bin/env node
// check-contraste.mjs — gate de accesibilidad del color. Mecánico y determinista
// (mismo espíritu que check-drift.mjs): parsea los tokens de color de cada uno de
// los 4 themes en web/src/styles/tokens.css y verifica, con la fórmula de
// luminancia relativa de WCAG 2.1, que el texto cumple el contraste mínimo AA.
//
// Por qué existe: el chrome usa `--dim` en TEXTO real y pequeño (statusline a
// 10.5px, citas markdown, metadatos, footers). WCAG 2.1 exige 4.5:1 para texto
// normal (§1.4.3), sin excepción por debajo de 18px. Un `--dim` que "susurra"
// pero no se lee excluye a usuarios con baja visión (hallazgo W-02 de la
// auditoría de diseño 2026-07-15). Este check convierte esa regla en un gate:
// si un token de lectura baja de AA contra su fondo, el build rompe.
//
// Qué comprueba, para CADA theme (enu, dracula, gruvbox, solarized):
//   - --fg  sobre --bg  ≥ 4.5:1  (el cuerpo de texto principal)
//   - --dim sobre --bg  ≥ 4.5:1  (el texto secundario: es lectura, no adorno)
//
// Sin dependencias; corre con `node web/scripts/check-contraste.mjs` o
// `npm run check:contraste`.

import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const RAIZ = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");
const TOKENS = join(RAIZ, "web", "src", "styles", "tokens.css");

// Los 4 themes de terminal reales que expone la web. El orden es el de tokens.css.
const THEMES = ["enu", "dracula", "gruvbox", "solarized"];

// Pares (token de texto, fondo) que exigimos conformes a AA. Ambos son LECTURA:
// --fg es el cuerpo; --dim pinta statusline, citas, metadatos y footers.
const PARES = [
  ["--fg", "--bg"],
  ["--dim", "--bg"],
];

const AA_TEXTO_NORMAL = 4.5; // WCAG 2.1 §1.4.3, texto < 18px (todo el chrome lo es).

// --- Color -------------------------------------------------------------------

// #rgb o #rrggbb -> [r, g, b] en 0..255. Lanza si el token no es un hex sólido
// (no manejamos rgba ni var(): los tokens de tokens.css son hex literales).
function parseHex(hex) {
  const h = hex.trim().replace(/^#/, "");
  if (h.length === 3) {
    return [0, 1, 2].map((i) => parseInt(h[i] + h[i], 16));
  }
  if (h.length === 6) {
    return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16));
  }
  throw new Error(`color no reconocido: "${hex}"`);
}

// Luminancia relativa WCAG 2.1 (https://www.w3.org/TR/WCAG21/#dfn-relative-luminance).
function luminancia([r, g, b]) {
  const lin = [r, g, b].map((v) => {
    const c = v / 255;
    return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  });
  return 0.2126 * lin[0] + 0.7152 * lin[1] + 0.0722 * lin[2];
}

// Ratio de contraste WCAG (https://www.w3.org/TR/WCAG21/#dfn-contrast-ratio):
// (L_claro + 0.05) / (L_oscuro + 0.05), independiente de cuál sea texto o fondo.
function ratio(hexA, hexB) {
  const la = luminancia(parseHex(hexA));
  const lb = luminancia(parseHex(hexB));
  const [claro, oscuro] = la >= lb ? [la, lb] : [lb, la];
  return (claro + 0.05) / (oscuro + 0.05);
}

// --- Parser de tokens.css ----------------------------------------------------

// Extrae, para cada theme, el mapa { --token: #hex } de su bloque de reglas.
// El bloque de `nu` se declara bajo `html[data-theme='enu']` (y comparte selector
// con `html,`, pero basta con el atributo). Localizamos el selector del theme y
// leemos hasta el primer `}`.
function parseTokens() {
  const css = readFileSync(TOKENS, "utf8");
  const porTheme = {};
  for (const theme of THEMES) {
    const idx = css.indexOf(`data-theme='${theme}'`);
    if (idx === -1) throw new Error(`no encuentro el bloque del theme "${theme}" en tokens.css`);
    const abre = css.indexOf("{", idx);
    const cierra = css.indexOf("}", abre);
    if (abre === -1 || cierra === -1) throw new Error(`bloque mal formado para "${theme}"`);
    const cuerpo = css.slice(abre + 1, cierra);
    const mapa = {};
    for (const m of cuerpo.matchAll(/(--[\w-]+)\s*:\s*(#[0-9a-fA-F]{3,6})\s*;/g)) {
      mapa[m[1]] = m[2];
    }
    porTheme[theme] = mapa;
  }
  return porTheme;
}

// --- Verificación ------------------------------------------------------------

const porTheme = parseTokens();
const fallos = [];
const lineas = []; // informe legible aun cuando pasa todo

for (const theme of THEMES) {
  const tk = porTheme[theme];
  for (const [texto, fondo] of PARES) {
    const cTexto = tk[texto];
    const cFondo = tk[fondo];
    if (!cTexto || !cFondo) {
      fallos.push(`${theme}: falta ${!cTexto ? texto : fondo} en tokens.css`);
      continue;
    }
    const r = ratio(cTexto, cFondo);
    const ok = r >= AA_TEXTO_NORMAL;
    const marca = ok ? "✓" : "✗";
    lineas.push(
      `  ${marca} ${theme.padEnd(10)} ${texto} ${cTexto} sobre ${fondo} ${cFondo} = ${r.toFixed(2)}:1`
    );
    if (!ok) {
      fallos.push(
        `${theme}: ${texto} (${cTexto}) sobre ${fondo} (${cFondo}) = ${r.toFixed(2)}:1 — ` +
          `por debajo de AA (${AA_TEXTO_NORMAL}:1). Sube ${texto} conservando el matiz hasta pasar.`
      );
    }
  }
}

console.log(lineas.join("\n"));

if (fallos.length > 0) {
  console.error(`\nContraste AA no cumplido (${fallos.length} par(es)):\n`);
  for (const f of fallos) console.error("  " + f);
  console.error(
    "\nWCAG 2.1 §1.4.3 exige 4.5:1 para texto normal. --dim es texto de lectura " +
      "(statusline, citas, metadatos), no adorno. Ajusta el token en tokens.css."
  );
  process.exit(1);
}
console.log(`\n✓ contraste AA cumplido en los ${THEMES.length} themes (${PARES.length} pares por theme)`);
