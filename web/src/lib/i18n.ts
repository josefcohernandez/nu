// Diccionarios de i18n del CHROME (headers, statusline, menú, feedback). El
// CONTENIDO de docs solo existe en español por ahora: en EN las páginas
// internas muestran "in spanish for now" en el header (langNote).
//
// Copiado fielmente de la clase Component del prototipo (strings es/en y el
// objeto W del chrome interno) y del copy del README del handoff. No inventar
// copy: cualquier texto visible sale de aquí.

export type Lang = 'es' | 'en';

export interface Dict {
  // Portada — slogan y cuerpo
  sloganA: string;
  sloganB: string;
  body: string;
  // Portada — menú [i][d][a][g]
  k1: string; // instalar
  k2: string; // documentación
  k2d: string; // la wiki de nu (· in spanish for now en EN)
  kApiD: string; // referencia nu.*
  // Feedback de la línea de comandos
  fb_i: string;
  fb_d: string;
  fb_g: string;
  fb_a: string;
  fb_nf: string; // sufijo de "no encontrado"
  fb_help: string; // dos líneas (\n)
  fb_theme: string;
  fb_theme_nf: string;
  fb_lang: string;
  fb_repl: string;
  fb_repl_out: string;
  // Error de comando desconocido en el pager (guiño a vim)
  e492: string;
  // Chrome interno (páginas de docs/api/plugins)
  langNote: string; // solo visible en EN: "in spanish for now"
  s1: string; // grupo sidebar: empezar
  s2: string; // espec
  s3: string; // extensiones
  onPage: string; // "en esta página"
  commit: string; // "última edición"
  prev: string; // "← anterior"
  next: string; // "siguiente:"
  // Teclas de la statusline (pager)
  kScroll: string;
  kDoc: string;
  kMod: string;
  kCmd: string;
  kSearch: string;
  kBack: string;
  // Statusline táctil (móvil)
  mIndex: string; // "índice"
  // Chrome de la referencia /api
  apiStable: string; // "estable" (api v1 · estable)
  ctaTitle: string; // "¿tu primer plugin?"
  ctaBody: string; // cuerpo del CTA
  ctaLink: string; // "guia-plugins.md →"
  genFrom: string; // "generada desde docs/api.md · "
  sagrada: string; // "la API v1 es sagrada: crece solo por adición"
  adrLink: string; // "adr-003 →"
  firstMod: string; // "(primer módulo)"
  lastMod: string; // "(último)"
  drawerClose: string; // "cerrar" (drawer móvil)
  // fase D — 404: es la única pieza de chrome nueva de esta fase que necesita
  // bilingüismo real (el resto de /404 y todo /plugins es contenido, no chrome).
  e404Msg: string; // "no es un documento del editor" (antes de ": <ruta>")
  e404Sug: string; // "¿querías decir? →" (antes del link de sugerencia)
  e404Index: string; // "índice de docs" (etiqueta del [d])
  // fase E — overlay de búsqueda (pantalla 14c)
  sHitS: string; // "coincidencia" (singular)
  sHitP: string; // "coincidencias" (plural)
  sDocS: string; // "documento" (singular)
  sDocP: string; // "documentos" (plural)
  sIn: string; // "en" ("N coincidencias en M documentos")
  sFor: string; // "para" ("…para <término>")
  sJump: string; // "saltar" (hint [n/p])
  sOpen: string; // "abrir" (hint [enter])
  sClose: string; // "cerrar" (hint [esc])
  sNoIndex: string; // "índice no disponible en dev — npm run build"
}

export const i18n: Record<Lang, Dict> = {
  es: {
    sloganA: 'Tu agente de código.',
    sloganB: 'Tus reglas.',
    body: 'Instálalo con una línea. Úsalo tal cual, o cámbialo entero escribiendo Lua. nu es el coding harness que es tuyo de verdad.',
    k1: 'instalar',
    k2: 'documentación',
    k2d: 'la wiki de nu',
    kApiD: 'referencia nu.*',
    fb_i: '→ copia y pega: curl -fsSL nu.sh/install | sh',
    fb_d: '→ abriendo la wiki de nu…',
    fb_g: '→ github.com/dbareagimeno/nu',
    fb_a: '→ abriendo la referencia de la api…',
    fb_nf: 'no encontrado — prueba [i], [d], [g] o help',
    fb_help:
      'comandos: i · d · g · theme <nu|dracula|gruvbox|solarized> · lang <es|en>\n…y si sabes lua, ya sabes qué hacer',
    fb_theme: 'theme = ',
    fb_theme_nf: 'theme no encontrado: nu · dracula · gruvbox · solarized',
    fb_lang: 'lang = ',
    fb_repl: 'lua 5.4 (embebido en nu) — escribe salir para volver',
    fb_repl_out: 'hasta luego',
    e492: 'no es un comando del editor',
    langNote: '',
    s1: 'empezar',
    s2: 'espec',
    s3: 'extensiones',
    onPage: 'en esta página',
    commit: 'última edición',
    prev: '← anterior',
    next: 'siguiente:',
    kScroll: 'desplazar',
    kDoc: 'doc sig/ant',
    kMod: 'módulo sig/ant',
    kCmd: 'comando',
    kSearch: 'buscar',
    kBack: 'portada',
    mIndex: 'índice',
    apiStable: 'estable',
    ctaTitle: '¿tu primer plugin?',
    ctaBody: 'plugin.toml + init.lua. La guía te lleva de cero a un comando slash propio.',
    ctaLink: 'guia-plugins.md →',
    genFrom: 'generada desde docs/api.md · ',
    sagrada: 'la API v1 es sagrada: crece solo por adición',
    adrLink: 'adr-003 →',
    firstMod: '(primer módulo)',
    lastMod: '(último)',
    drawerClose: 'cerrar',
    // fase D
    e404Msg: 'no es un documento del editor',
    e404Sug: '¿querías decir? →',
    e404Index: 'índice de docs',
    // fase E
    sHitS: 'coincidencia',
    sHitP: 'coincidencias',
    sDocS: 'documento',
    sDocP: 'documentos',
    sIn: 'en',
    sFor: 'para',
    sJump: 'saltar',
    sOpen: 'abrir',
    sClose: 'cerrar',
    sNoIndex: 'índice no disponible en dev — npm run build',
  },
  en: {
    sloganA: 'Your coding agent.',
    sloganB: 'Your rules.',
    body: 'Install it with one line. Use it as is, or rewrite the whole thing in Lua. nu is the coding harness that is truly yours.',
    k1: 'install',
    k2: 'documentation',
    k2d: 'the nu wiki · in spanish for now',
    kApiD: 'nu.* reference',
    fb_i: '→ copy & paste: curl -fsSL nu.sh/install | sh',
    fb_d: '→ opening the nu wiki…',
    fb_g: '→ github.com/dbareagimeno/nu',
    fb_a: '→ opening the api reference…',
    fb_nf: 'not found — try [i], [d], [g] or help',
    fb_help:
      'commands: i · d · g · theme <nu|dracula|gruvbox|solarized> · lang <es|en>\n…and if you know lua, you know what to do',
    fb_theme: 'theme = ',
    fb_theme_nf: 'theme not found: nu · dracula · gruvbox · solarized',
    fb_lang: 'lang = ',
    fb_repl: 'lua 5.4 (embedded in nu) — type exit to leave',
    fb_repl_out: 'see you',
    e492: 'not an editor command',
    langNote: 'in spanish for now',
    s1: 'start',
    s2: 'spec',
    s3: 'extensions',
    onPage: 'on this page',
    commit: 'last edited',
    prev: '← previous',
    next: 'next:',
    kScroll: 'scroll',
    kDoc: 'next/prev doc',
    kMod: 'next/prev module',
    kCmd: 'command',
    kSearch: 'search',
    kBack: 'home',
    mIndex: 'index',
    apiStable: 'stable',
    ctaTitle: 'your first plugin?',
    ctaBody: 'plugin.toml + init.lua. The guide takes you from zero to your own slash command.',
    ctaLink: 'guia-plugins.md →',
    genFrom: 'generated from docs/api.md · ',
    sagrada: 'the v1 API is sacred: it grows only by addition',
    adrLink: 'adr-003 →',
    firstMod: '(first module)',
    lastMod: '(last)',
    drawerClose: 'close',
    // fase D
    e404Msg: 'not a document known to the editor',
    e404Sug: 'did you mean? →',
    e404Index: 'docs index',
    // fase E
    sHitS: 'match',
    sHitP: 'matches',
    sDocS: 'document',
    sDocP: 'documents',
    sIn: 'in',
    sFor: 'for',
    sJump: 'jump',
    sOpen: 'open',
    sClose: 'close',
    sNoIndex: 'index not available in dev — npm run build',
  },
};
