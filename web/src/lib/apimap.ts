// Orden y agrupación del sidebar de /api. La sección /api/<slug> se genera desde
// las 16 páginas de la colección `referencia` (presentación derivada de
// docs/api.md, la superficie sagrada). Aquí vive SOLO la estructura de
// navegación; el contenido sale de los .md tal cual.
//
// Decisiones ante el mock (que listaba módulos inexistentes: http, ws, re,
// workers): manda el contenido REAL. El grupo `nu.*` ordena por la §N de
// docs/api.md (nu raíz §2 … log §15); `convenciones` va suelta arriba y `cli`
// suelta al final (es superficie CLI, no `nu.*`, pero tiene página). El grupo
// `extensiones/` enlaza a las páginas de la WIKI que las especifican —no hay
// páginas /api para ellas y no se inventan—.

export interface ApiItem {
  /** etiqueta visible en el sidebar */
  label: string;
  /** slug de la página /api/<slug> (ítems internos) */
  slug?: string;
  /** slug de la página /docs/<docSlug> de la wiki (ítems de extensiones) */
  docSlug?: string;
}

export interface ApiGroup {
  /** nombre del grupo (dim, con `/`), o null para ítems sueltos */
  label: string | null;
  items: ApiItem[];
}

// El grupo `nu.*` en el orden de las secciones de docs/api.md.
const NU_MODULOS: ApiItem[] = [
  { label: 'nu', slug: 'nu' }, // §2 raíz
  { label: 'task', slug: 'task' }, // §3
  { label: 'events', slug: 'events' }, // §4
  { label: 'fs', slug: 'fs' }, // §5
  { label: 'proc', slug: 'proc' }, // §6
  { label: 'sys', slug: 'sys' }, // §7
  { label: 'red', slug: 'red' }, // §8 (http + ws)
  { label: 'ui', slug: 'ui' }, // §9
  { label: 'text', slug: 'text' }, // §10
  { label: 'search', slug: 'search' }, // §11
  { label: 'codecs', slug: 'codecs' }, // §12
  { label: 'worker', slug: 'worker' }, // §13
  { label: 'plugin', slug: 'plugin' }, // §14
  { label: 'log', slug: 'log' }, // §15
];

export const API_SIDEBAR: ApiGroup[] = [
  { label: null, items: [{ label: 'convenciones', slug: 'convenciones' }] },
  { label: 'nu.*', items: NU_MODULOS },
  { label: null, items: [{ label: 'cli', slug: 'cli' }] },
  {
    label: 'extensiones/',
    items: [
      { label: 'agent.tool', docSlug: 'agente' },
      { label: 'chat.command', docSlug: 'chat' },
      { label: 'providers', docSlug: 'providers' },
      { label: 'mcp', docSlug: 'mcp' },
    ],
  },
];

// Secuencia lineal de las páginas /api (alimenta [n]/[p] y la posición n/16 de
// la statusline). Las extensiones enlazan fuera de /api y no cuentan.
export const API_ORDER: string[] = [
  'convenciones',
  ...NU_MODULOS.map((m) => m.slug!),
  'cli',
];

export const API_TOTAL = API_ORDER.length; // 16

/** Posición 1-indexada de un slug en la secuencia, o 0 si no está. */
export function apiPos(slug: string): number {
  return API_ORDER.indexOf(slug) + 1;
}

/** Vecinos anterior/siguiente en la secuencia lineal (null en los extremos). */
export function apiVecinos(slug: string): { prev: string | null; next: string | null } {
  const i = API_ORDER.indexOf(slug);
  return {
    prev: i > 0 ? API_ORDER[i - 1] : null,
    next: i >= 0 && i < API_ORDER.length - 1 ? API_ORDER[i + 1] : null,
  };
}
