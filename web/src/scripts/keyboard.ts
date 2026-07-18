// Módulo de teclado compartido — vanilla TS, sin framework.
//
// Portado fielmente de la clase Component del prototipo (onKeyEvent para la
// portada, onWikiKeyEvent para el pager) y generalizado en initKeyboard({mode}).
// Cada página lo llama con sus parámetros. La portada lo usa ya; las páginas
// internas (docs/api/plugins) se montan en fases posteriores y reutilizan el
// modo 'pager'.
//
// CONTRATO para las fases siguientes:
//   initKeyboard(opts) instala el listener global de teclado. Opciones:
//     - mode: 'home' | 'pager'
//     - nextUrl / prevUrl (solo pager): URLs para [n]/[p]; null si no hay.
//   El módulo busca en el DOM sus elementos por id (ver más abajo); si no
//   están, degrada sin fallar.
//
//   Home (portada) espera estos ids:
//     #enu-prompt   span con la etiqueta del prompt ('>' o 'lua>')
//     #enu-typed    span donde se pinta el texto tecleado
//     #enu-feedback línea de feedback (dim, min-height fija)
//
//   Pager (páginas internas) espera:
//     #enu-status-left  el hueco izquierdo de la statusline; al pulsar ':' se
//                      convierte en prompt de comando, y se restaura al salir.
//     [data-scroll]    (opcional) el contenedor scrolleable para j/k; si falta,
//                      hace scroll de window.
//
//   Búsqueda: en pager, '/' emite el CustomEvent 'enu:search-open' en window y
//   delega. El overlay de búsqueda (otra fase) lo escucha; también puede
//   escuchar 'nu:search-close'. Este módulo NO dibuja el overlay.
//
//   Helpers exportados que otras fases reutilizarán: setFeedback, setTheme,
//   setLang, applyLang.

import {
  INSTALL_CMD,
  GITHUB_URL,
  DOCS_FIRST,
  API_FIRST,
  VERSION,
} from '../lib/const';
import { i18n, type Lang } from '../lib/i18n';

const THEMES = ['enu', 'dracula', 'gruvbox', 'solarized'] as const;
type Theme = (typeof THEMES)[number];

const LS_THEME = 'enu-theme';
const LS_LANG = 'enu-lang';

const BASE: string = import.meta.env.BASE_URL; // p. ej. '/enu/'

// ── Estado ──────────────────────────────────────────────────────────────────
type CommandMode = '' | 'cmd' | 'search' | 'repl';

let commandMode: CommandMode = '';
let buffer = '';
let lang: Lang = 'es';

function currentLang(): Lang {
  const v = document.documentElement.getAttribute('data-lang');
  return v === 'en' ? 'en' : 'es';
}

// ── Helpers de theme / lang (exportados) ─────────────────────────────────────
export function setTheme(name: string): boolean {
  if (!(THEMES as readonly string[]).includes(name)) return false;
  document.documentElement.setAttribute('data-theme', name);
  try {
    localStorage.setItem(LS_THEME, name);
  } catch {
    /* almacenamiento no disponible: se ignora */
  }
  return true;
}

// URL homóloga de la página actual en el idioma `l`, si la hay (páginas de
// contenido: Base la publica en data-lang-home-es/en). En la portada y el 404 no
// existe → el cambio de idioma es en cliente.
function langHome(l: Lang): string | null {
  const v = document.documentElement.getAttribute(`data-lang-home-${l}`);
  return v && v.length > 0 ? v : null;
}

export function setLang(l: Lang): void {
  // En páginas de contenido el idioma lo fija la URL: cambiarlo es NAVEGAR a la
  // página homóloga, no un swap de chrome (W-04). Fuera de ellas (portada, 404)
  // se mantiene el toggle en cliente.
  const home = langHome(l);
  if (home && l !== currentLang()) {
    goHref(home);
    return;
  }
  lang = l;
  document.documentElement.setAttribute('data-lang', l);
  document.documentElement.setAttribute('lang', l);
  try {
    localStorage.setItem(LS_LANG, l);
  } catch {
    /* ignore */
  }
  applyLang();
}

// Sustituye todos los elementos de chrome marcados con data-i18n="clave" por su
// traducción actual, y actualiza los pickers (activo entre corchetes).
export function applyLang(): void {
  const d = i18n[lang];
  document.querySelectorAll<HTMLElement>('[data-i18n]').forEach((el) => {
    const key = el.getAttribute('data-i18n') as keyof typeof d;
    if (key && d[key] != null) el.textContent = d[key] as string;
  });
  // Picker de idioma: activo entre corchetes en key, resto dim.
  document.querySelectorAll<HTMLElement>('[data-lang-pick]').forEach((el) => {
    const l = el.getAttribute('data-lang-pick');
    const active = l === lang;
    el.textContent = active ? `[${l}]` : (l ?? '');
    el.style.color = active ? 'var(--key)' : 'var(--dim)';
    el.style.fontWeight = active ? '600' : '400';
  });
}

// Picker de theme: activo entre corchetes en key, resto dim.
function refreshThemePickers(): void {
  const cur = document.documentElement.getAttribute('data-theme') ?? 'enu';
  document.querySelectorAll<HTMLElement>('[data-theme-pick]').forEach((el) => {
    const t = el.getAttribute('data-theme-pick');
    const active = t === cur;
    el.textContent = active ? `[${t}]` : (t ?? '');
    el.style.color = active ? 'var(--key)' : 'var(--dim)';
    el.style.fontWeight = active ? '600' : '400';
  });
}

// ── Feedback (portada) ───────────────────────────────────────────────────────
export function setFeedback(text: string): void {
  const el = document.getElementById('enu-feedback');
  if (el) el.textContent = text;
}

function renderTyped(): void {
  const t = document.getElementById('enu-typed');
  if (t) t.textContent = buffer;
  const p = document.getElementById('enu-prompt');
  if (p) {
    p.textContent = commandMode === 'repl' ? 'lua>' : '>';
    p.style.color = commandMode === 'repl' ? 'var(--key)' : 'var(--dim)';
  }
  // Hint tecleable (W-06): visible solo con el prompt vacío y fuera del REPL, de
  // modo que guía al primer tecleo, desaparece en cuanto hay texto y reaparece
  // si el buffer se vacía. Ocultado con display (sin transición: prohibidas).
  const h = document.getElementById('enu-hint');
  if (h) h.style.display = buffer === '' && commandMode !== 'repl' ? '' : 'none';
}

// ── Mini-REPL Lua (easter egg) ───────────────────────────────────────────────
// Evalúa lo mismo que evalLua del prototipo: aritmética (^ como potencia),
// print("…"), concatenación .. de dos strings literales, enu.version (leído de
// VERSION, sin literal duplicado — W-08), os.date(); el resto → nil; error
// aritmético → syntax error.
function evalLua(src: string): string {
  const s = src.trim();
  if (!s) return '';
  // La versión sale de const.ts (VERSION es "v0.2.0"); el REPL la muestra sin la
  // `v`, como enu.version en el binario real, y así sigue a la versión de verdad.
  if (s === 'enu.version' || s === 'enu.version.api')
    return '"' + VERSION.replace(/^v/, '') + '"';
  if (s === 'os.date()') return '"' + new Date().toString() + '"';
  const pr = s.match(/^print\((["'])(.*)\1\)$/);
  if (pr) return pr[2];
  const rt = s.replace(/^return\s+/, '');
  if (/^[0-9+\-*/%^(). ]+$/.test(rt)) {
    try {
      const v = Function('"use strict";return (' + rt.replace(/\^/g, '**') + ')')();
      return String(v);
    } catch {
      return 'stdin: syntax error';
    }
  }
  if (/^(["']).*\1\s*\.\.\s*(["']).*\2$/.test(rt)) {
    const parts = rt.split('..').map((x) => x.trim().slice(1, -1));
    return '"' + parts.join('') + '"';
  }
  return 'nil';
}

// ── Acciones i/d/a/g ─────────────────────────────────────────────────────────
function goHref(url: string): void {
  window.location.href = url;
}

function actionInstall(): void {
  const d = i18n[lang];
  try {
    navigator.clipboard?.writeText(INSTALL_CMD);
  } catch {
    /* sin portapapeles: al menos mostramos la línea */
  }
  setFeedback(d.fb_i);
}

// Prefijo de idioma para las rutas de contenido: 'en/' si el idioma activo es
// EN, '' si ES. Así [d]/[a] (y el comando `open`) llevan a /en/docs · /en/api
// cuando el visitante está en inglés (W-04).
function langPrefix(): string {
  return currentLang() === 'en' ? 'en/' : '';
}

function actionDocs(): void {
  setFeedback(i18n[lang].fb_d);
  goHref(`${BASE}${langPrefix()}docs/${DOCS_FIRST}`);
}

function actionApi(): void {
  setFeedback(i18n[lang].fb_a);
  goHref(`${BASE}${langPrefix()}api/${API_FIRST}`);
}

function actionGithub(): void {
  setFeedback(i18n[lang].fb_g);
  goHref(GITHUB_URL);
}

// help: dos líneas exactas (la segunda es el único anuncio del easter egg).
function fbHelp(): string {
  return i18n[lang].fb_help;
}

// Resuelve un comando `theme <nombre>`; devuelve feedback y aplica si existe.
function applyThemeCmd(rest: string): string {
  const d = i18n[lang];
  const name = rest.replace(/^=/, '').trim();
  if (!name) {
    const cur = document.documentElement.getAttribute('data-theme') ?? 'enu';
    return d.fb_theme + '"' + cur + '"';
  }
  if (setTheme(name)) {
    refreshThemePickers();
    return d.fb_theme + '"' + name + '"';
  }
  return d.fb_theme_nf;
}

function applyLangCmd(rest: string): string {
  const d = i18n[lang];
  const lg = rest.replace(/^=/, '').trim();
  if (lg === 'es' || lg === 'en') {
    setLang(lg);
    return i18n[lg].fb_lang + '"' + lg + '"';
  }
  return d.fb_lang + '"' + lang + '"';
}

// ── Portada (mode 'home') ────────────────────────────────────────────────────
function homeEnter(): void {
  const d = i18n[lang];
  const cmd = buffer.trim();

  if (commandMode === 'repl') {
    if (cmd === 'salir' || cmd === 'exit' || cmd === 'q') {
      commandMode = '';
      buffer = '';
      setFeedback(d.fb_repl_out);
      renderTyped();
      return;
    }
    setFeedback(evalLua(cmd));
    buffer = '';
    renderTyped();
    return;
  }

  buffer = '';
  renderTyped();

  if (cmd === 'i' || cmd === 'install' || cmd === 'instalar') return actionInstall();
  if (cmd === 'd' || cmd === 'docs') return actionDocs();
  if (cmd === 'a' || cmd === 'api') return actionApi();
  if (cmd === 'g' || cmd === 'github') return actionGithub();
  if (cmd === 'help' || cmd === 'ayuda' || cmd === '?') return setFeedback(fbHelp());
  if (cmd.startsWith('theme')) return setFeedback(applyThemeCmd(cmd.slice(5).trim()));
  if (cmd.startsWith('lang')) return setFeedback(applyLangCmd(cmd.slice(4).trim()));
  if (cmd === 'lua') {
    commandMode = 'repl';
    setFeedback(d.fb_repl);
    renderTyped();
    return;
  }
  // Cualquier otro texto: nu: «X» no encontrado — prueba [i], [d], [g] o help
  setFeedback('nu: «' + cmd + '» ' + d.fb_nf);
}

function homeKey(e: KeyboardEvent): void {
  if (e.key === 'Enter') {
    homeEnter();
    e.preventDefault();
    return;
  }
  if (e.key === 'Backspace') {
    buffer = buffer.slice(0, -1);
    renderTyped();
    e.preventDefault();
    return;
  }
  if (e.key === 'Escape') {
    buffer = '';
    commandMode = '';
    setFeedback('');
    renderTyped();
    return;
  }
  if (e.key.length === 1 && !e.metaKey && !e.ctrlKey && !e.altKey) {
    // Atajo directo: i/d/a/g con buffer vacío (fuera del REPL) ejecutan.
    if (commandMode !== 'repl' && buffer === '') {
      if (e.key === 'i') {
        actionInstall();
        e.preventDefault();
        return;
      }
      if (e.key === 'd') {
        actionDocs();
        e.preventDefault();
        return;
      }
      if (e.key === 'a') {
        actionApi();
        e.preventDefault();
        return;
      }
      if (e.key === 'g') {
        actionGithub();
        e.preventDefault();
        return;
      }
    }
    buffer += e.key;
    if (commandMode !== 'repl') setFeedback('');
    renderTyped();
    e.preventDefault();
  }
}

// ── Pager (mode 'pager') ─────────────────────────────────────────────────────
let statusLeftDefault = '';

// Actualiza el texto idle de la statusline (contexto · % · n/m). Si hay un
// prompt de comando/búsqueda activo no lo pisa: el texto nuevo aparecerá al
// salir del modo. Lo usa pager.ts para reflejar el % de lectura al hacer scroll.
export function setStatusLeft(text: string): void {
  statusLeftDefault = text;
  if (!commandMode) {
    const el = pagerStatusEl();
    if (el) el.textContent = statusLeftDefault;
  }
}

function pagerStatusEl(): HTMLElement | null {
  return document.getElementById('enu-status-left');
}

function renderPagerPrompt(): void {
  const el = pagerStatusEl();
  if (!el) return;
  if (!commandMode) {
    el.textContent = statusLeftDefault;
    return;
  }
  const char = commandMode === 'search' ? '/' : ':';
  el.textContent = char + buffer;
  // Cursor bloque al final del prompt.
  const cur = document.createElement('span');
  cur.className = 'enu-cursor';
  el.appendChild(cur);
}

function scrollBy(delta: number): void {
  const box = document.querySelector<HTMLElement>('[data-scroll]');
  if (box) box.scrollBy({ top: delta });
  else window.scrollBy({ top: delta });
}

function pagerEnter(): void {
  const d = i18n[lang];
  const cmd = buffer.trim();
  commandMode = '';
  buffer = '';

  if (cmd === 'q') {
    renderPagerPrompt();
    goHref(BASE);
    return;
  }
  if (cmd === 'help' || cmd === 'ayuda' || cmd === '?') {
    statusLeftDefault = fbHelp().split('\n')[0];
    renderPagerPrompt();
    return;
  }
  if (cmd.startsWith('theme')) {
    statusLeftDefault = applyThemeCmd(cmd.slice(5).trim());
    renderPagerPrompt();
    return;
  }
  if (cmd.startsWith('lang')) {
    statusLeftDefault = applyLangCmd(cmd.slice(4).trim());
    renderPagerPrompt();
    return;
  }
  if (cmd.startsWith('open ')) {
    const slug = cmd.slice(5).trim().replace(/\.md$/, '');
    goHref(`${BASE}${langPrefix()}docs/${slug}`);
    return;
  }
  if (cmd === 'i' || cmd === 'install' || cmd === 'instalar') return actionInstall();
  if (cmd === 'd' || cmd === 'docs') return actionDocs();
  if (cmd === 'a' || cmd === 'api') return actionApi();
  if (cmd === 'g' || cmd === 'github') return actionGithub();
  // Comando desconocido en el pager (guiño a vim).
  statusLeftDefault = 'E492: ' + d.e492 + ': ' + cmd;
  renderPagerPrompt();
}

function pagerKey(e: KeyboardEvent, opts: PagerOpts): void {
  if (!commandMode) {
    // Modo idle: navegación de lectura.
    switch (e.key) {
      case ':':
        commandMode = 'cmd';
        buffer = '';
        renderPagerPrompt();
        e.preventDefault();
        return;
      case '/':
        // Delega el overlay de búsqueda a otra fase.
        window.dispatchEvent(new CustomEvent('enu:search-open'));
        e.preventDefault();
        return;
      case '?':
        // Atajo directo a la ayuda (W-06): equivalente a `:help`, muestra la
        // primera línea del texto de ayuda en el hueco de contexto.
        statusLeftDefault = fbHelp().split('\n')[0];
        renderPagerPrompt();
        e.preventDefault();
        return;
      case 'j':
        scrollBy(60);
        e.preventDefault();
        return;
      case 'k':
        scrollBy(-60);
        e.preventDefault();
        return;
      case 'n':
        if (opts.nextUrl) goHref(opts.nextUrl);
        return;
      case 'p':
        if (opts.prevUrl) goHref(opts.prevUrl);
        return;
      case 'q':
        goHref(BASE);
        return;
      default:
        return;
    }
  }
  // Modo comando activo.
  if (e.key === 'Enter') {
    pagerEnter();
    e.preventDefault();
    return;
  }
  if (e.key === 'Backspace') {
    if (buffer === '') commandMode = '';
    else buffer = buffer.slice(0, -1);
    renderPagerPrompt();
    e.preventDefault();
    return;
  }
  if (e.key === 'Escape') {
    commandMode = '';
    buffer = '';
    renderPagerPrompt();
    return;
  }
  if (e.key.length === 1 && !e.metaKey && !e.ctrlKey && !e.altKey) {
    buffer += e.key;
    renderPagerPrompt();
    e.preventDefault();
  }
}

// ── API pública ──────────────────────────────────────────────────────────────
export interface PagerOpts {
  nextUrl?: string | null;
  prevUrl?: string | null;
}

export interface InitKeyboardOptions extends PagerOpts {
  mode: 'home' | 'pager';
}

// Ignora el teclado si el foco está en un input/textarea real del navegador.
function focusInField(): boolean {
  const el = document.activeElement;
  if (!el) return false;
  const tag = el.tagName;
  return tag === 'INPUT' || tag === 'TEXTAREA' || (el as HTMLElement).isContentEditable;
}

export function initKeyboard(opts: InitKeyboardOptions): void {
  lang = currentLang();

  // Pickers de theme/lang clicables (portada y páginas internas).
  document.querySelectorAll<HTMLElement>('[data-theme-pick]').forEach((el) => {
    el.style.cursor = 'pointer';
    el.addEventListener('click', () => {
      const t = el.getAttribute('data-theme-pick');
      if (t && setTheme(t)) refreshThemePickers();
    });
  });
  document.querySelectorAll<HTMLElement>('[data-lang-pick]').forEach((el) => {
    el.style.cursor = 'pointer';
    el.addEventListener('click', () => {
      const l = el.getAttribute('data-lang-pick');
      if (l === 'es' || l === 'en') setLang(l);
    });
  });
  refreshThemePickers();
  applyLang();

  if (opts.mode === 'pager') {
    const el = pagerStatusEl();
    statusLeftDefault = el ? el.textContent ?? '' : '';
  } else {
    renderTyped();
  }

  window.addEventListener('keydown', (e) => {
    if (focusInField()) return;
    if (opts.mode === 'home') homeKey(e);
    else pagerKey(e, opts);
  });
}
