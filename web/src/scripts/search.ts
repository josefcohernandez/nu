// Overlay de búsqueda — pantalla 14c. Vanilla TS, sin framework. Se carga bajo
// demanda (import dinámico desde pager.ts) al pulsar '/' en cualquier página
// interna, así el resto de páginas no lo pagan.
//
// Comportamiento (ver el handoff §"14c — Búsqueda" y §"Interactions"):
//   - Panel overlay (fondo bg, borde border) sobre el contenido, entre el
//     header y la statusline (su geometría se mide al abrir).
//   - La statusline izquierda (#enu-status-left) se convierte en `/término▊` y
//     la derecha en las teclas disponibles; ambas se restauran al cerrar.
//   - Mientras está abierto, ESTE módulo captura el teclado (keydown en window
//     con capture:true + stopPropagation): keyboard.ts no ve nada. Caracteres →
//     término (con debounce); Backspace borra (vacío → cierra); Escape cierra;
//     las flechas ↓/↑ (y Ctrl-n/Ctrl-p, estilo pager) saltan entre resultados;
//     Enter abre el activo. n/p planas se teclean como cualquier letra.
//   - Resultados de pagefind agrupados por documento; cada sección es una línea
//     `§n · <extracto>`. El activo lleva fondo border, término invertido y ◂.
//   - En móvil hay un <input> real (invoca el teclado virtual); keyboard.ts lo
//     ignora por diseño (foco en input → no captura).
//
// Ownership fase E: este fichero + search.css + los ganchos en pager.ts.

import '../styles/search.css';
import { setStatusLeft } from './keyboard';
import { i18n, type Lang } from '../lib/i18n';

const BASE: string = import.meta.env.BASE_URL; // p. ej. '/enu/'

// Límites de presentación (evitan listas kilométricas; N y M se cuentan sobre
// lo MOSTRADO, como pide el handoff).
const MAX_DOCS = 8;
const MAX_SUBS = 6;
const DEBOUNCE_MS = 120;

// ── Estado del módulo ─────────────────────────────────────────────────────────
interface LineRef {
  el: HTMLElement;
  url: string;
}

let abierto = false;
let term = '';
let activo = -1; // índice en `lineas` del resultado activo
let lineas: LineRef[] = [];
let seq = 0; // token anti-carrera para búsquedas asíncronas
let debounceTimer: ReturnType<typeof setTimeout> | null = null;

// DOM (singleton, se construye la primera vez).
let panel: HTMLElement | null = null;
let scrollBox: HTMLElement | null = null;
let headEl: HTMLElement | null = null;
let resultsEl: HTMLElement | null = null;
let inputEl: HTMLInputElement | null = null;

// Statusline previa, para restaurar al cerrar.
let prevLeft = '';
let prevRight = '';
let prevOverflow = '';
// Elemento enfocado antes de abrir, para devolverle el foco al cerrar (W-07).
let prevFocus: HTMLElement | null = null;

// pagefind cargado perezosamente (o null si falló, p. ej. en dev sin build).
type Pagefind = {
  search: (t: string) => Promise<{ results: { data: () => Promise<PfData> }[] }>;
  options?: (o: Record<string, unknown>) => Promise<void> | void;
  init?: () => Promise<void> | void;
};
interface PfSub {
  title?: string;
  url?: string;
  excerpt?: string;
}
interface PfData {
  url: string;
  excerpt?: string;
  meta?: { title?: string };
  sub_results?: PfSub[];
}
let pf: Pagefind | null = null;
let pfError = false;

function lang(): Lang {
  return document.documentElement.getAttribute('data-lang') === 'en' ? 'en' : 'es';
}

// ── Carga de pagefind ─────────────────────────────────────────────────────────
async function cargaPagefind(): Promise<Pagefind | null> {
  if (pf) return pf;
  if (pfError) return null;
  try {
    // Ruta variable + @vite-ignore: pagefind.js solo existe en el sitio
    // construido (BASE/pagefind/pagefind.js), nunca en el bundle de Vite.
    const mod = (await import(/* @vite-ignore */ `${BASE}pagefind/pagefind.js`)) as Pagefind;
    await mod.options?.({ excerptLength: 22 });
    await mod.init?.();
    pf = mod;
    return pf;
  } catch {
    pfError = true;
    return null;
  }
}

// ── Construcción del DOM ──────────────────────────────────────────────────────
function construye(): void {
  if (panel) return;

  panel = document.createElement('div');
  panel.className = 'nu-search';
  panel.setAttribute('role', 'dialog');
  panel.setAttribute('aria-modal', 'true');
  // Foco-trap (W-07): en escritorio el foco se mueve al PANEL (no al input),
  // porque el modelo de teclado compone el término desde teclas crudas vía el
  // switch de `onKeyDown` y sincroniza un input oculto; enfocar el input real
  // haría que el navegador escribiera por su cuenta y duplicaría la lógica. Con
  // tabindex="-1" el panel puede recibir foco programático (no tabulable) y así
  // `aria-modal` deja de mentir: el foco queda dentro del diálogo, no en la
  // página de fondo. En táctil sí se enfoca el input (levanta el teclado).
  panel.setAttribute('tabindex', '-1');

  scrollBox = document.createElement('div');
  scrollBox.className = 'nu-search-scroll';

  inputEl = document.createElement('input');
  inputEl.className = 'nu-search-input';
  inputEl.type = 'text';
  inputEl.setAttribute('autocomplete', 'off');
  inputEl.setAttribute('autocapitalize', 'off');
  inputEl.setAttribute('autocorrect', 'off');
  inputEl.setAttribute('spellcheck', 'false');
  inputEl.setAttribute('aria-label', 'buscar');
  inputEl.addEventListener('input', () => {
    term = inputEl!.value;
    renderStatusLeft();
    programaBusqueda();
  });

  headEl = document.createElement('div');
  headEl.className = 'nu-search-head';

  resultsEl = document.createElement('div');
  resultsEl.className = 'nu-search-results';

  scrollBox.append(inputEl, headEl, resultsEl);
  panel.append(scrollBox);
  document.body.append(panel);
}

// ── Geometría: el panel va entre el header y la statusline ────────────────────
function coloca(): void {
  if (!panel) return;
  const hdr = document.querySelector<HTMLElement>('.int-header');
  const sub = document.querySelector<HTMLElement>('.api-subbar');
  const sl = document.querySelector<HTMLElement>('.statusline');
  let top = 0;
  if (hdr) top = Math.max(top, hdr.getBoundingClientRect().bottom);
  if (sub) top = Math.max(top, sub.getBoundingClientRect().bottom);
  panel.style.top = Math.max(0, top) + 'px';
  panel.style.bottom = (sl ? Math.round(sl.getBoundingClientRect().height) : 0) + 'px';
}

// ── Statusline (izquierda = /término▊, derecha = teclas) ──────────────────────
function statusLeftEl(): HTMLElement | null {
  return document.getElementById('enu-status-left');
}
function statusRightEl(): HTMLElement | null {
  return document.querySelector<HTMLElement>('.statusline .right');
}

function renderStatusLeft(): void {
  const el = statusLeftEl();
  if (!el) return;
  el.textContent = '/' + term;
  const cur = document.createElement('span');
  cur.className = 'enu-cursor';
  el.appendChild(cur);
}

function renderStatusRight(): void {
  const el = statusRightEl();
  if (!el) return;
  const d = i18n[lang()];
  const k = (t: string): string => `<span style="color:var(--fg)">${t}</span>`;
  el.innerHTML = `${k('[↑↓]')} ${d.sJump} · ${k('[enter]')} ${d.sOpen} · ${k('[esc]')} ${d.sClose}`;
}

// ── Etiquetas ─────────────────────────────────────────────────────────────────
// Nombre del documento en estilo ruta (docs/contracts/agente.md, api/fs, plugins),
// derivado de la URL del resultado — coherente con el chrome del terminal.
function etiquetaDoc(url: string): string {
  let p = url;
  try {
    p = new URL(url, location.origin).pathname;
  } catch {
    /* url relativa: se usa tal cual */
  }
  if (p.startsWith(BASE)) p = p.slice(BASE.length);
  p = p
    .replace(/^\/+/, '')
    .replace(/index\.html$/, '')
    .replace(/\.html$/, '')
    .replace(/\/+$/, '');
  if (!p) return 'inicio';
  if (/^docs\//.test(p)) p += '.md';
  return p;
}

// §n de una línea: número real de sección si el título del sub_result empieza
// por dígito (nuestros docs titulan «## 5. Permisos»); si no, el índice de
// posición; si la línea no cuelga de ninguna sección (sin ancla), solo `·`.
function etiquetaSec(sub: PfSub, idx1: number): string {
  const t = (sub.title ?? '').trim();
  const m = t.match(/^(\d+)/);
  if (m) return `§${m[1]} ·`;
  const tieneAncla = typeof sub.url === 'string' && sub.url.includes('#');
  if (tieneAncla) return `§${idx1} ·`;
  return '·';
}

// ── Render de resultados ──────────────────────────────────────────────────────
function limpiaResultados(): void {
  if (resultsEl) resultsEl.innerHTML = '';
  if (headEl) headEl.textContent = '';
  lineas = [];
  activo = -1;
}

function mensaje(texto: string): void {
  if (!resultsEl) return;
  resultsEl.innerHTML = '';
  const div = document.createElement('div');
  div.className = 'nu-search-msg';
  div.textContent = texto;
  resultsEl.appendChild(div);
}

function renderCabecera(n: number, m: number): void {
  if (!headEl) return;
  headEl.textContent = '';
  const d = i18n[lang()];
  const hits = n === 1 ? d.sHitS : d.sHitP;
  const docs = m === 1 ? d.sDocS : d.sDocP;
  headEl.append(
    document.createTextNode(`${n} ${hits} ${d.sIn} ${m} ${docs} ${d.sFor} `),
  );
  const termSpan = document.createElement('span');
  termSpan.className = 'nu-search-term';
  termSpan.textContent = term;
  headEl.appendChild(termSpan);
}

function renderResultados(datos: PfData[]): void {
  if (!resultsEl) return;
  resultsEl.innerHTML = '';
  lineas = [];

  let totalLineas = 0;
  let totalDocs = 0;

  for (const d of datos) {
    const subs = (d.sub_results && d.sub_results.length ? d.sub_results : [
      { url: d.url, excerpt: d.excerpt },
    ]).slice(0, MAX_SUBS);
    if (subs.length === 0) continue;
    totalDocs++;

    const docEl = document.createElement('div');
    docEl.className = 'nu-doc';
    docEl.textContent = etiquetaDoc(d.url);
    resultsEl.appendChild(docEl);

    const grupo = document.createElement('div');
    grupo.className = 'nu-doc-lines';

    subs.forEach((sub, i) => {
      const linea = document.createElement('div');
      linea.className = 'nu-line';

      const sec = document.createElement('span');
      sec.className = 'nu-sec';
      sec.textContent = etiquetaSec(sub, i + 1) + ' ';
      linea.appendChild(sec);

      const ex = document.createElement('span');
      ex.className = 'nu-excerpt';
      ex.innerHTML = sub.excerpt ?? ''; // pagefind marca las coincidencias con <mark>
      linea.appendChild(ex);

      const caret = document.createElement('span');
      caret.className = 'nu-caret';
      caret.textContent = ' ◂';
      linea.appendChild(caret);

      grupo.appendChild(linea);
      lineas.push({ el: linea, url: sub.url ?? d.url });
      totalLineas++;
    });

    resultsEl.appendChild(grupo);
  }

  renderCabecera(totalLineas, totalDocs);
  activo = lineas.length > 0 ? 0 : -1;
  marcaActivo();
}

function marcaActivo(): void {
  lineas.forEach((l, i) => l.el.classList.toggle('activo', i === activo));
  if (activo >= 0 && lineas[activo]) {
    lineas[activo].el.scrollIntoView({ block: 'nearest' });
  }
}

function mueve(delta: number): void {
  if (lineas.length === 0) return;
  activo = Math.min(lineas.length - 1, Math.max(0, activo + delta));
  marcaActivo();
}

function abreActivo(): void {
  if (activo < 0 || !lineas[activo]) return;
  window.location.href = lineas[activo].url;
}

// ── Búsqueda ──────────────────────────────────────────────────────────────────
function programaBusqueda(): void {
  if (debounceTimer) clearTimeout(debounceTimer);
  debounceTimer = setTimeout(() => void ejecutaBusqueda(), DEBOUNCE_MS);
}

async function ejecutaBusqueda(): Promise<void> {
  const token = ++seq;
  const q = term.trim();
  if (!q) {
    limpiaResultados();
    return;
  }
  const motor = await cargaPagefind();
  if (token !== seq) return; // término cambió mientras cargaba
  if (!motor) {
    if (headEl) headEl.textContent = '';
    mensaje(i18n[lang()].sNoIndex);
    lineas = [];
    activo = -1;
    return;
  }
  try {
    const res = await motor.search(q);
    if (token !== seq) return;
    const primeros = res.results.slice(0, MAX_DOCS);
    const datos = (await Promise.all(primeros.map((r) => r.data()))) as PfData[];
    if (token !== seq) return;
    if (datos.length === 0) {
      renderCabecera(0, 0);
      if (resultsEl) resultsEl.innerHTML = '';
      lineas = [];
      activo = -1;
      return;
    }
    renderResultados(datos);
  } catch {
    if (token !== seq) return;
    mensaje(i18n[lang()].sNoIndex);
  }
}

// ── Teclado (capturado por este módulo mientras el overlay está abierto) ──────
function onKeyDown(e: KeyboardEvent): void {
  if (!abierto) return;
  const enInput = e.target === inputEl;

  // En el input real (móvil) dejamos que el navegador escriba; solo
  // interceptamos las teclas de control. No detenemos la propagación de los
  // caracteres para que lleguen al input.
  if (enInput) {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      cierra();
    } else if (e.key === 'Enter') {
      e.preventDefault();
      e.stopPropagation();
      abreActivo();
    }
    return;
  }

  // Desktop: capturamos TODO para que keyboard.ts no vea nada.
  e.stopPropagation();

  // Ctrl-n / Ctrl-p: navegación estilo pager, equivalentes a ↓/↑. Se manejan
  // antes del switch porque comparten `e.key` ('n'/'p') con la escritura: las
  // teclas n/p SIN modificador ya no navegan (caían al viejo case 'n'/'p'),
  // sino que se teclean como cualquier letra en el `default` — sin esto era
  // imposible buscar el grueso del vocabulario nu.* (plugin, spawn, print…,
  // y `nu` mismo empieza por 'n'). Ver W-01.
  if (e.ctrlKey && !e.metaKey && !e.altKey && (e.key === 'n' || e.key === 'p')) {
    e.preventDefault();
    mueve(e.key === 'n' ? 1 : -1);
    return;
  }

  switch (e.key) {
    case 'Escape':
      e.preventDefault();
      cierra();
      return;
    case 'Enter':
      e.preventDefault();
      abreActivo();
      return;
    case 'Backspace':
      e.preventDefault();
      if (term === '') {
        cierra();
      } else {
        term = term.slice(0, -1);
        sincronizaInput();
        renderStatusLeft();
        programaBusqueda();
      }
      return;
    case 'ArrowDown':
      e.preventDefault();
      mueve(1);
      return;
    case 'ArrowUp':
      e.preventDefault();
      mueve(-1);
      return;
    default:
      if (e.key.length === 1 && !e.metaKey && !e.ctrlKey && !e.altKey) {
        e.preventDefault();
        term += e.key;
        sincronizaInput();
        renderStatusLeft();
        programaBusqueda();
      }
  }
}

function sincronizaInput(): void {
  if (inputEl) inputEl.value = term;
}

// ── Abrir / cerrar ────────────────────────────────────────────────────────────
export function openSearch(): void {
  if (abierto) {
    // Ya abierto: reasegura el foco del input en móvil.
    if (inputEl && esTactil()) inputEl.focus();
    return;
  }
  construye();
  // Recuerda el foco actual ANTES de moverlo, para restaurarlo al cerrar (W-07).
  prevFocus = document.activeElement as HTMLElement | null;
  abierto = true;
  term = '';
  lineas = [];
  activo = -1;
  limpiaResultados();
  sincronizaInput();

  // Guarda la statusline actual para restaurarla al cerrar.
  const left = statusLeftEl();
  prevLeft = left ? left.textContent ?? '' : '';
  const right = statusRightEl();
  prevRight = right ? right.innerHTML : '';

  // Bloquea el scroll de la página de fondo (evita que se descoloque el panel y
  // que el scroll pise la statusline).
  prevOverflow = document.documentElement.style.overflow;
  document.documentElement.style.overflow = 'hidden';

  coloca();
  panel!.classList.add('abierto');
  renderStatusLeft();
  renderStatusRight();

  window.addEventListener('keydown', onKeyDown, true);
  window.addEventListener('resize', coloca);

  // Mueve el foco DENTRO del diálogo (W-07): en táctil al input (levanta el
  // teclado virtual), en escritorio al panel (el switch de teclado ya gobierna
  // la escritura; ver la nota de `construye`). Así el foco no se queda en la
  // página de fondo mientras el modal está abierto.
  if (esTactil() && inputEl) {
    inputEl.focus();
  } else {
    panel!.focus();
  }
}

function cierra(): void {
  if (!abierto) return;
  abierto = false;
  if (debounceTimer) {
    clearTimeout(debounceTimer);
    debounceTimer = null;
  }
  window.removeEventListener('keydown', onKeyDown, true);
  window.removeEventListener('resize', coloca);

  panel?.classList.remove('abierto');
  if (inputEl) inputEl.blur();

  // Restaura scroll y statusline.
  document.documentElement.style.overflow = prevOverflow;
  setStatusLeft(prevLeft); // restaura #enu-status-left vía keyboard.ts
  const right = statusRightEl();
  if (right) right.innerHTML = prevRight;

  // Devuelve el foco a quien lo tenía antes de abrir (cierra el foco-trap, W-07).
  prevFocus?.focus();
  prevFocus = null;

  window.dispatchEvent(new CustomEvent('nu:search-close'));
}

function esTactil(): boolean {
  return (
    window.matchMedia('(pointer: coarse)').matches ||
    window.matchMedia('(max-width: 767px)').matches
  );
}

// Por si algún día se cierra desde fuera (simetría con 'enu:search-open').
window.addEventListener('nu:search-close-request', () => cierra());
