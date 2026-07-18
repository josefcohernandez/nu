// Metadatos de git por fichero, resueltos en build (sitio estático):
// el hash y la fecha del último commit que tocó la ruta. La wiki y la
// referencia los muestran en el carril derecho ("última edición").
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';

// Raíz del repo: este fichero vive en web/src/lib/, la raíz está tres niveles arriba.
const RAIZ_REPO = resolve(fileURLToPath(new URL('.', import.meta.url)), '../../..');

export interface GitMeta {
  /** hash corto del último commit que tocó el fichero */
  hash: string;
  /** fecha del commit en ISO 8601 */
  fechaISO: string;
  /** días transcurridos desde el commit hasta el momento del build */
  diasDesde: number;
}

/**
 * Último commit que tocó `ruta` (relativa a la raíz del repo, p. ej.
 * 'docs/contracts/api.md' o 'web/src/content/docs/empezando/instalacion.md').
 * Devuelve null si git no conoce el fichero (sin commits todavía).
 */
export function gitMeta(ruta: string): GitMeta | null {
  try {
    const salida = execFileSync(
      'git',
      ['log', '-1', '--format=%h|%cI', '--', ruta],
      { cwd: RAIZ_REPO, encoding: 'utf8' },
    ).trim();
    if (!salida) return null;
    const [hash, fechaISO] = salida.split('|');
    const diasDesde = Math.max(
      0,
      Math.floor((Date.now() - new Date(fechaISO).getTime()) / 86_400_000),
    );
    return { hash, fechaISO, diasDesde };
  } catch {
    return null;
  }
}
