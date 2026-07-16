import { defineCollection, z } from 'astro:content';
import { glob } from 'astro/loaders';

// Cuatro colecciones de contenido, todas con el glob loader:
//
//  - `wiki`: los .md REALES del repo bajo docs/ (fuente de verdad de la
//    documentación), enumerados EXPLÍCITAMENTE: docs/ se organiza por capas
//    (core/, contracts/, findings/, worklog/…) y solo los contratos publicados
//    de la Capa 1 son páginas de la wiki — un glob con comodines cargaría los
//    registros internos (páginas fantasma). El generateId recorta la
//    subcarpeta para que el slug siga siendo el basename (filosofia, no
//    core/filosofia). Los .md llevan frontmatter title/description propio.
//  - `empezar`: las páginas de "empezar" locales, con frontmatter
//    title/description.
//  - `extensiones`: páginas locales de las extensiones oficiales que no tienen
//    contrato propio en docs/ (mcp, repl, toolkit) más el índice (extensiones),
//    con frontmatter title/description como `empezar`.
//  - `referencia`: los 16 .md de la referencia de la API, con frontmatter
//    title/description. NO se tocan: el detector check-drift y el CI dependen
//    de ellos.
const wiki = defineCollection({
  loader: glob({
    pattern: [
      'core/filosofia.md',
      'core/arquitectura.md',
      'core/modelo-ejecucion.md',
      'contracts/guia-plugins.md',
      'contracts/providers.md',
      'contracts/agente.md',
      'contracts/sesiones.md',
      'contracts/chat.md',
    ],
    base: '../docs',
    generateId: ({ entry }) => entry.split('/').pop()!.replace(/\.md$/, ''),
  }),
  // El frontmatter de docs/ trae más campos (type, layer, web, status…):
  // aquí solo se tipan los que la web usa; el resto se ignora.
  schema: z.object({
    title: z.string().optional(),
    description: z.string().optional(),
  }),
});

const empezar = defineCollection({
  loader: glob({ pattern: '*.md', base: './src/content/docs/empezando' }),
  schema: z.object({
    title: z.string(),
    description: z.string().optional(),
  }),
});

const extensiones = defineCollection({
  loader: glob({ pattern: '*.md', base: './src/content/docs/extensiones' }),
  schema: z.object({
    title: z.string(),
    description: z.string().optional(),
  }),
});

const referencia = defineCollection({
  loader: glob({ pattern: '*.md', base: './src/content/docs/referencia' }),
  schema: z.object({
    title: z.string(),
    description: z.string().optional(),
  }),
});

// Colecciones EN (W-04): instantánea traducida del contenido ES bajo
// src/content/en/. Mismos slugs y mismos schemas que sus gemelas: `wiki_en` sin
// frontmatter (los .md de docs/ traducidos no lo llevan), el resto con
// title/description ya traducido. Las páginas EN (/en/docs, /en/api) las
// consumen; check-drift sigue mirando SOLO la referencia ES.
const wiki_en = defineCollection({
  loader: glob({ pattern: ['*.md', '!README.md'], base: './src/content/en/wiki' }),
  schema: z.object({
    title: z.string().optional(),
    description: z.string().optional(),
  }),
});

const empezar_en = defineCollection({
  loader: glob({ pattern: '*.md', base: './src/content/en/empezando' }),
  schema: z.object({
    title: z.string(),
    description: z.string().optional(),
  }),
});

const extensiones_en = defineCollection({
  loader: glob({ pattern: '*.md', base: './src/content/en/extensiones' }),
  schema: z.object({
    title: z.string(),
    description: z.string().optional(),
  }),
});

const referencia_en = defineCollection({
  loader: glob({ pattern: '*.md', base: './src/content/en/referencia' }),
  schema: z.object({
    title: z.string(),
    description: z.string().optional(),
  }),
});

export const collections = {
  wiki,
  empezar,
  extensiones,
  referencia,
  wiki_en,
  empezar_en,
  extensiones_en,
  referencia_en,
};
