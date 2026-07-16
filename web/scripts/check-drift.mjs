#!/usr/bin/env node
// check-drift.mjs — detector de deriva entre docs/contracts/api.md (la fuente de verdad,
// la "superficie sagrada" v1) y web/src/content/docs/referencia/ (su
// presentación). Mecánico y determinista: extrae el inventario de callables
// {nombre, firma, ⏸, [W]} de ambos lados y falla listando cada discrepancia.
//
// Qué compara:
//   - Inventario: toda función/método de api.md aparece en alguna fence de la
//     referencia, y viceversa (nada sobra en la web que la espec no defina).
//   - Firma: argumentos y retorno idénticos módulo espacios en blanco.
//   - Marcadores: ⏸ (suspende) siempre; [W] (workers) solo en las firmas
//     primarias de api.md (las de la columna Firma), donde la convención de
//     marcadores es fiable.
//
// De dónde sale cada dato:
//   - api.md: filas de las tablas | Firma | Semántica |. La columna Firma da
//     los callables primarios (marcador por posición: el texto entre un span
//     y el siguiente). La columna Semántica aporta callables secundarios
//     (métodos de handles como Timer:stop) con su ⏸ pero sin [W] fiable.
//     El [W] efectivo de un primario = [W] del heading de sección (módulo
//     entero) o de la propia celda, salvo que la fila diga "solo estado
//     principal".
//   - referencia/*.md: líneas de las fences SIN etiqueta (las etiquetadas
//     ```lua/```sh son ejemplos, no firmas). Marcadores: inline en la línea,
//     o del heading más cercano SI ese heading nombra el callable (o un
//     prefijo de módulo, p. ej. `enu.json` cubre `enu.json.encode`). Un heading
//     que no nombra (p. ej. "Mensajes") no contagia marcadores: evita falsos
//     positivos en métodos agrupados bajo el heading de otra función.
//
// Sin dependencias; corre con `node web/scripts/check-drift.mjs` o
// `npm run check:drift`. Flag `--inventario` vuelca el índice derivado de
// api.md en JSON (artefacto generado, nunca fuente).

import { readFileSync, readdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const RAIZ = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");
const API_MD = join(RAIZ, "docs", "contracts", "api.md");
const REF_DIR = join(RAIZ, "web", "src", "content", "docs", "referencia");
// Páginas sin superficie de API: convenciones (la notación de ejemplo
// `enu.mod.fn(...)` parsearía como callable) y la CLI del binario.
const PAGINAS_SIN_API = new Set(["convenciones.md", "cli.md"]);

const SUSP = "⏸";
const W = "[W]";

// --- Parser de firmas -------------------------------------------------------

// Cabeza de un callable: nombre punteado bajo `enu.` o método `Handle:metodo`.
const RE_CABEZA = /^(enu\.[\w./]+|[A-Z][A-Za-z]*:[a-z_]\w*)/;

// Escanea paréntesis balanceados desde s[i] === "(". Devuelve el índice del
// cierre o -1 (necesario porque los tipos anidan: `(string | Span[])[]`).
function cierreBalanceado(s, i) {
  let prof = 0;
  for (; i < s.length; i++) {
    if (s[i] === "(") prof++;
    else if (s[i] === ")" && --prof === 0) return i;
  }
  return -1;
}

// Parsea UNA firma ("enu.fs.read(path) -> string", "Task:cancel()",
// "enu.version -> {...}"). Devuelve lista de callables (la forma compacta
// `enu.log.debug/info/warn/error(fmt, ...)` expande a varios) o [] si el
// texto no es una firma (prosa, salidas de ejemplo, opts sueltos).
function parseFirma(texto) {
  let s = texto.trim();
  const susp = s.includes(SUSP);
  const w = s.includes(W);
  s = s.replaceAll(SUSP, "").replaceAll(W, "").replaceAll("\\|", "|").trim();

  const m = s.match(RE_CABEZA);
  if (!m) return [];
  let cabeza = m[1];
  let resto = s.slice(cabeza.length);

  let args = null;
  if (resto.startsWith("(")) {
    const fin = cierreBalanceado(resto, 0);
    if (fin === -1) return [];
    args = resto.slice(1, fin);
    resto = resto.slice(fin + 1);
  }

  let ret = null;
  const mr = resto.match(/^\s*->\s*(.+)$/);
  if (mr) ret = mr[1].trim();
  else if (resto.trim() !== "") return []; // basura tras la firma: no es firma
  if (args === null && ret === null) return []; // nombre pelado: mención, no firma

  // Alternativas compactas en la cabeza: enu.log.debug/info/warn/error.
  let nombres = [cabeza];
  if (cabeza.includes("/")) {
    const partes = cabeza.split("/");
    const base = partes[0].slice(0, partes[0].lastIndexOf(".") + 1);
    nombres = [partes[0], ...partes.slice(1).map((p) => base + p)];
  }
  return nombres.map((nombre) => ({ nombre, args, ret, susp, w }));
}

const norm = (s) => (s === null ? null : s.replace(/\s+/g, ""));
const firmaCanonica = (c) =>
  c.nombre + (c.args === null ? "" : `(${c.args})`) + (c.ret === null ? "" : ` -> ${c.ret}`);

// --- Lado api.md -------------------------------------------------------------

function parseApi() {
  const texto = readFileSync(API_MD, "utf8");
  const inventario = new Map(); // nombre -> callable
  let moduloW = false;
  let numLinea = 0;

  const registrar = (c, extra) => {
    if (!inventario.has(c.nombre)) inventario.set(c.nombre, { ...c, ...extra });
  };

  for (const linea of texto.split("\n")) {
    numLinea++;
    if (/^## /.test(linea)) {
      moduloW = linea.includes(W); // [W] a nivel de módulo (p. ej. §5 enu.fs)
      continue;
    }
    const l = linea.trim();
    if (!l.startsWith("|") || /^\|\s*-/.test(l)) continue;

    // Celdas separadas por | no escapado (las firmas usan \| para uniones).
    const celdas = l.replaceAll("\\|", "\x00").split("|").map((c) => c.replaceAll("\x00", "\\|"));
    if (celdas.length < 3) continue;
    const celdaFirma = celdas[1];
    const celdaSem = celdas.slice(2).join("|");
    if (celdaFirma.trim() === "Firma") continue;
    const soloPrincipal = /solo estado principal/i.test(l);

    // Spans de una celda con su zona de marcadores (el texto hasta el span
    // siguiente: ahí viven el ⏸ y el [W] de ESA firma, no de la celda entera).
    const extraer = (celda, primaria) => {
      const spans = [...celda.matchAll(/`([^`]+)`/g)];
      let prev = null;
      spans.forEach((sp, i) => {
        const zona = celda.slice(sp.index + sp[0].length, i + 1 < spans.length ? spans[i + 1].index : celda.length);
        let cuerpo = sp[1];
        if (cuerpo.startsWith("...") && prev) {
          // Abreviatura `...recv()`: hereda el prefijo punteado del span anterior.
          cuerpo = prev.slice(0, prev.lastIndexOf(".") + 1) + cuerpo.slice(3);
        }
        for (const c of parseFirma(cuerpo)) {
          prev = c.nombre;
          const susp = c.susp || zona.includes(SUSP);
          if (primaria) {
            const efectivoW = (moduloW || c.w || zona.includes(W)) && !soloPrincipal;
            registrar(c, { susp, w: efectivoW, primaria: true, linea: numLinea });
          } else {
            registrar(c, { susp, w: null, primaria: false, linea: numLinea });
          }
        }
      });
    };
    extraer(celdaFirma, true);
    extraer(celdaSem, false);
  }
  return inventario;
}

// --- Lado web/referencia ------------------------------------------------------

// Divide una línea de fence por " / " a nivel superior (fuera de paréntesis):
// "Region:fill(style?) / Region:clear()" son dos firmas.
function segmentos(linea) {
  const out = [];
  let prof = 0, ini = 0;
  for (let i = 0; i < linea.length; i++) {
    const ch = linea[i];
    if ("([{".includes(ch)) prof++;
    else if (")]}".includes(ch)) prof--;
    else if (ch === "/" && prof === 0 && linea[i - 1] === " " && linea[i + 1] === " ") {
      out.push(linea.slice(ini, i));
      ini = i + 1;
    }
  }
  out.push(linea.slice(ini));
  return out;
}

function parseWeb() {
  const inventario = new Map(); // nombre -> {callable, pagina, linea, ...}
  for (const fichero of readdirSync(REF_DIR).sort()) {
    if (!fichero.endsWith(".md") || PAGINAS_SIN_API.has(fichero)) continue;
    const lineas = readFileSync(join(REF_DIR, fichero), "utf8").split("\n");

    let heading = { nombres: [], susp: false, w: false };
    let enFence = false, fenceFirmas = false;

    lineas.forEach((linea, idx) => {
      const abre = linea.match(/^```(\S*)\s*$/);
      if (abre) {
        if (!enFence) {
          enFence = true;
          fenceFirmas = abre[1] === ""; // solo las fences sin etiqueta son firmas
        } else enFence = false;
        return;
      }
      if (!enFence) {
        const h = linea.match(/^#{2,3}\s+(.*)$/);
        if (h) {
          heading = {
            nombres: [...h[1].matchAll(/`([^`]+)`/g)].map((m) => m[1]).filter((n) => RE_CABEZA.test(n)),
            susp: h[1].includes(SUSP),
            w: h[1].includes(W),
          };
        }
        return;
      }
      if (!fenceFirmas) return;

      // Quita el comentario de cola ("-- nil al cerrar") antes de parsear.
      const sinComentario = linea.split(/\s+--\s/)[0];
      for (const seg of segmentos(sinComentario)) {
        for (const c of parseFirma(seg)) {
          // El heading contagia marcadores solo si nombra el callable o un
          // prefijo de módulo suyo (`enu.json` cubre `enu.json.encode`).
          const nombrado = heading.nombres.some(
            (n) => n === c.nombre || c.nombre.startsWith(n + ".")
          );
          if (!inventario.has(c.nombre)) {
            inventario.set(c.nombre, {
              ...c,
              susp: c.susp || (nombrado && heading.susp),
              w: c.w || (nombrado && heading.w),
              pagina: fichero,
              linea: idx + 1,
            });
          }
        }
      }
    });
  }
  return inventario;
}

// --- Comparación ---------------------------------------------------------------

const api = parseApi();
const web = parseWeb();

if (process.argv.includes("--inventario")) {
  console.log(JSON.stringify([...api.values()].map((c) => ({
    nombre: c.nombre, firma: firmaCanonica(c), suspende: c.susp,
    worker: c.w, primaria: c.primaria,
  })), null, 2));
  process.exit(0);
}

const fallos = [];
const donde = (c) => (c.pagina ? `${c.pagina}:${c.linea}` : `api.md:${c.linea}`);

for (const [nombre, a] of api) {
  const b = web.get(nombre);
  if (!b) {
    fallos.push(`FALTA EN WEB      ${nombre} (api.md:${a.linea}) — sin firma en ninguna fence de referencia/`);
    continue;
  }
  if (norm(a.args) !== norm(b.args) || norm(a.ret) !== norm(b.ret)) {
    fallos.push(
      `FIRMA DISTINTA    ${nombre} (${donde(b)})\n` +
      `                  api.md: ${firmaCanonica(a)}\n` +
      `                  web:    ${firmaCanonica(b)}`
    );
  }
  if (a.susp !== b.susp) {
    fallos.push(`MARCADOR ⏸        ${nombre} (${donde(b)}) — api.md dice ${a.susp ? "⏸" : "síncrona"}, la web ${b.susp ? "⏸" : "síncrona"}`);
  }
  if (a.primaria && a.w !== b.w) {
    fallos.push(`MARCADOR [W]      ${nombre} (${donde(b)}) — api.md dice ${a.w ? "[W]" : "solo estado principal"}, la web ${b.w ? "[W]" : "sin [W]"}`);
  }
}
for (const [nombre, b] of web) {
  if (!api.has(nombre)) {
    fallos.push(`SOBRA EN WEB      ${nombre} (${donde(b)}) — no existe en docs/contracts/api.md (¿deriva o errata?)`);
  }
}

if (fallos.length > 0) {
  console.error(`Deriva entre docs/contracts/api.md y web/referencia (${fallos.length} discrepancias):\n`);
  for (const f of fallos) console.error("  " + f);
  console.error("\ndocs/contracts/api.md es la fuente de verdad: corrige la web (o registra un hallazgo si la espec está mal).");
  process.exit(1);
}
console.log(`✓ web/referencia coherente con docs/contracts/api.md (${api.size} callables comprobados, ${web.size} en la web)`);
