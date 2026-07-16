// El mapa de la wiki: fuente única del orden lineal y de los grupos. Alimenta el
// sidebar (grupos), la navegación n/p (prev/next), la posición `n/18` de la
// statusline y —vía la ruta git de cada entrada— el bloque «última edición» del
// carril derecho. Fases posteriores (404) lo reutilizan como manifiesto.
//
// Orden y grupos EXACTOS. El slug es el fichero sin `.md`. Las seis primeras
// salen de la colección `empezar` (páginas locales con frontmatter); la capa
// conceptual (espec) sale de `wiki` (los .md reales del repo bajo docs/); el
// grupo extensiones MEZCLA colecciones: `wiki` (guia-plugins, providers,
// agente, sesiones, chat) y `extensiones` (páginas locales: extensiones, mcp,
// repl, toolkit). Por eso la colección es propiedad POR SLUG, no de grupo.

import type { Lang } from './i18n';

export type GrupoId = 'empezar' | 'espec' | 'extensiones';
export type Coleccion = 'wiki' | 'empezar' | 'extensiones';

export interface DocEntry {
  /** fichero sin `.md`, y segmento de la URL /docs/<slug> */
  slug: string;
  /** colección de contenido de la que sale */
  collection: Coleccion;
  /** grupo al que pertenece en el sidebar */
  grupo: GrupoId;
  /** ruta del fichero fuente relativa a la raíz del repo (para gitMeta) */
  gitPath: string;
}

export interface Grupo {
  id: GrupoId;
  /** clave i18n de la etiqueta del grupo (s1..s3) */
  i18nKey: 's1' | 's2' | 's3';
  slugs: string[];
}

/** Una entrada del mapa: slug + colección de la que sale. */
interface DefEntrada {
  slug: string;
  collection: Coleccion;
}

// Definición declarativa: grupo → clave i18n → entradas (slug + colección) en
// orden. La colección va por entrada porque el grupo extensiones mezcla `wiki`
// y `extensiones`.
const DEF: { id: GrupoId; i18nKey: Grupo['i18nKey']; entradas: DefEntrada[] }[] = [
  {
    id: 'empezar',
    i18nKey: 's1',
    entradas: [
      { slug: 'que-es-enu', collection: 'empezar' },
      { slug: 'instalacion', collection: 'empezar' },
      { slug: 'inicio-rapido', collection: 'empezar' },
      { slug: 'primer-script', collection: 'empezar' },
      { slug: 'primer-agente', collection: 'empezar' },
      { slug: 'conceptos', collection: 'empezar' },
    ],
  },
  {
    id: 'espec',
    i18nKey: 's2',
    entradas: [
      { slug: 'filosofia', collection: 'wiki' },
      { slug: 'arquitectura', collection: 'wiki' },
      { slug: 'modelo-ejecucion', collection: 'wiki' },
    ],
  },
  {
    id: 'extensiones',
    i18nKey: 's3',
    entradas: [
      { slug: 'extensiones', collection: 'extensiones' },
      { slug: 'guia-plugins', collection: 'wiki' },
      { slug: 'providers', collection: 'wiki' },
      { slug: 'agente', collection: 'wiki' },
      { slug: 'sesiones', collection: 'wiki' },
      { slug: 'chat', collection: 'wiki' },
      { slug: 'mcp', collection: 'extensiones' },
      { slug: 'repl', collection: 'extensiones' },
      { slug: 'toolkit', collection: 'extensiones' },
    ],
  },
];

// El `base` del sitio es '/enu' SIN barra final (astro.config). Se normaliza
// aquí para construir URLs correctas (`/enu/docs/<slug>`) sea cual sea la forma
// del valor: robustece frente a un `base` con o sin barra final. Igual que hace
// el plugin remark de la wiki con sus enlaces.
const BASE = import.meta.env.BASE_URL.replace(/\/$/, '');

// Prefijo de idioma para la URL: '' en ES, 'en/' en EN. Las páginas EN cuelgan
// de rutas estáticas paralelas (`/enu/en/docs/<slug>`) — W-04.
function prefijoLang(lang: Lang): string {
  return lang === 'en' ? 'en/' : '';
}

/** URL absoluta (con base) de la página de un slug, en el idioma dado. */
export function urlDoc(slug: string, lang: Lang = 'es'): string {
  return `${BASE}/${prefijoLang(lang)}docs/${slug}`;
}

// Ruta del fichero fuente (para gitMeta / «última edición»), consciente del
// idioma: el ES sale de docs/ o del contenido local ES; el EN de su instantánea
// traducida bajo web/src/content/en/. Así el bloque «última edición» de las
// páginas EN es veraz respecto al fichero que realmente rinden.
export function gitPathLang(collection: Coleccion, slug: string, lang: Lang = 'es'): string {
  if (lang === 'en') {
    switch (collection) {
      case 'wiki':
        return `web/src/content/en/wiki/${slug}.md`;
      case 'empezar':
        return `web/src/content/en/empezando/${slug}.md`;
      case 'extensiones':
        return `web/src/content/en/extensiones/${slug}.md`;
    }
  }
  switch (collection) {
    case 'wiki':
      // docs/ se organiza por capas (core/ y contracts/); el slug de la web
      // sigue siendo el basename, así que aquí se recupera la subcarpeta.
      return `docs/${WIKI_DIR[slug] ?? 'contracts'}/${slug}.md`;
    case 'empezar':
      return `web/src/content/docs/empezando/${slug}.md`;
    case 'extensiones':
      return `web/src/content/docs/extensiones/${slug}.md`;
  }
}

// Subcarpeta de docs/ de cada página wiki publicada (el resto de contratos
// publicados vive en contracts/).
const WIKI_DIR: Record<string, string> = {
  filosofia: 'core',
  arquitectura: 'core',
  'modelo-ejecucion': 'core',
};

// Compat: la ruta ES baked en cada DocEntry (la usa el wrapper ES; el EN pasa
// por gitPathLang con lang='en').
function gitPath(collection: Coleccion, slug: string): string {
  return gitPathLang(collection, slug, 'es');
}

// Grupos con sus slugs, para el sidebar/drawer.
export const GRUPOS: Grupo[] = DEF.map((d) => ({
  id: d.id,
  i18nKey: d.i18nKey,
  slugs: d.entradas.map((e) => e.slug),
}));

// Lista lineal en orden de lectura: alimenta prev/next y la posición n/18.
export const DOCS: DocEntry[] = DEF.flatMap((d) =>
  d.entradas.map((e) => ({
    slug: e.slug,
    collection: e.collection,
    grupo: d.id,
    gitPath: gitPath(e.collection, e.slug),
  })),
);

export const TOTAL = DOCS.length; // 18

const PORSLUG = new Map(DOCS.map((d, i) => [d.slug, i]));

/** Índice lineal (0-based) de un slug, o -1 si no existe. */
export function indiceDe(slug: string): number {
  return PORSLUG.has(slug) ? (PORSLUG.get(slug) as number) : -1;
}

/** Entrada del mapa para un slug. */
export function docDe(slug: string): DocEntry | undefined {
  const i = indiceDe(slug);
  return i >= 0 ? DOCS[i] : undefined;
}
