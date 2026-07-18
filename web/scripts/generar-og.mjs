#!/usr/bin/env node
// generar-og.mjs — genera public/og.png (1200×630) del wordmark sobre el fondo
// del theme nu. Script one-off reproducible: `node scripts/generar-og.mjs`.
// El PNG resultante se deja en public/ (no se comitea el binario aparte).

import sharp from 'sharp';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const __dirname = dirname(fileURLToPath(import.meta.url));
const OUT = join(__dirname, '..', 'public', 'og.png');

// Paleta del theme nu.
const BG = '#0a1416';
const BRIGHT = '#e8f4f6';
const DIM = '#4e686e';
const KEY = '#4fcadb';

// Wordmark centrado + slogan debajo, todo en IBM Plex Mono (fallback monospace).
const svg = `
<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="630" viewBox="0 0 1200 630">
  <rect width="1200" height="630" fill="${BG}"/>
  <rect x="510" y="210" width="180" height="90" fill="${BRIGHT}"/>
  <text x="600" y="256" font-family="monospace" font-size="64" font-weight="700"
        fill="${BG}" text-anchor="middle" dominant-baseline="middle">enu</text>
  <text x="600" y="370" font-family="monospace" font-size="34" font-weight="600"
        fill="${BRIGHT}" text-anchor="middle">Tu agente de código.</text>
  <text x="600" y="416" font-family="monospace" font-size="34" font-weight="600"
        fill="${KEY}" text-anchor="middle">Tus reglas.</text>
  <text x="600" y="486" font-family="monospace" font-size="20"
        fill="${DIM}" text-anchor="middle">curl -fsSL enu.sh/install | sh</text>
</svg>`;

await sharp(Buffer.from(svg)).png().toFile(OUT);
console.log('OG generada en', OUT);
