// Constantes globales del sitio. Centralizadas aquí para que la web tenga una
// única fuente de verdad de dominio, versión y URLs.

// Dominio placeholder: el dominio real está pendiente de decisión (ver el
// handoff, "Decisiones abiertas"). Al fijarlo, solo se cambia aquí.
export const DOMAIN = 'enu.sh';

// La línea de instalación de una sola línea. La usan la portada ([i]) y el
// portapapeles.
export const INSTALL_CMD = `curl -fsSL ${DOMAIN}/install | sh`;

export const GITHUB_URL = 'https://github.com/dbareagimeno/enu';
export const RELEASES_URL = `${GITHUB_URL}/releases`;

// Versión visible (footer, chrome). La versión sin `v` la usa el REPL
// (`enu.version` → "0.2.0").
export const VERSION = 'v0.2.0';

export const LICENSE = 'apache-2.0';

// Primeras páginas de cada sección: destino de [d] (docs) y [a] (api) desde la
// portada. Las páginas se montan en fases posteriores.
export const DOCS_FIRST = 'que-es-enu'; // primera página del orden lineal del docmap
export const API_FIRST = 'fs';
