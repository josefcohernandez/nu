// Contrato común de las páginas internas (docs, api, plugins, 404): cablea el
// teclado en modo pager, deriva el % de lectura del scroll para la statusline
// y alterna el drawer móvil. Las páginas lo llaman una vez en un <script>.
//
//   initPager({
//     context: 'docs/core/filosofia.md',  // parte izquierda de la statusline
//     pos: '1/22',                   // posición en la colección; opcional
//     withPercent: true,             // intercala '· N%' derivado del scroll
//     nextUrl, prevUrl,              // destinos de [n]/[p]; null si no hay
//   })
//
// Statusline izquierda resultante: `context · N% · pos` (los tramos opcionales
// se omiten). El drawer móvil se cablea por atributos: los botones
// [data-drawer-toggle] alternan la clase 'abierto' del panel [data-drawer].
import { initKeyboard, setStatusLeft } from './keyboard';

export interface InitPagerOptions {
  context: string;
  pos?: string;
  withPercent?: boolean;
  nextUrl?: string | null;
  prevUrl?: string | null;
}

function porcentajeLectura(): number {
  const doc = document.documentElement;
  const total = doc.scrollHeight - doc.clientHeight;
  if (total <= 0) return 100;
  return Math.min(100, Math.max(0, Math.round((doc.scrollTop / total) * 100)));
}

export function initPager(opts: InitPagerOptions): void {
  initKeyboard({ mode: 'pager', nextUrl: opts.nextUrl, prevUrl: opts.prevUrl });

  const refrescar = (): void => {
    const partes = [opts.context];
    if (opts.withPercent) partes.push(porcentajeLectura() + '%');
    if (opts.pos) partes.push(opts.pos);
    setStatusLeft(partes.join(' · '));
  };
  refrescar();
  if (opts.withPercent) {
    window.addEventListener('scroll', refrescar, { passive: true });
  }

  // Drawer móvil: [≡] abre/cierra el índice. Lo disparan tanto los botones
  // marcados [data-drawer-toggle] (botón flotante, backdrop, cerrar) como el
  // `[≡] índice` de la statusline táctil (Statusline.astro, [data-touch=index]).
  const panel = document.querySelector<HTMLElement>('[data-drawer]');
  const alterna = (): void => {
    panel?.classList.toggle('abierto');
  };
  document
    .querySelectorAll<HTMLElement>('[data-drawer-toggle], [data-touch="index"]')
    .forEach((btn) => btn.addEventListener('click', alterna));

  // Búsqueda (fase E): el overlay se carga bajo demanda (import dinámico) para
  // no pesar en el resto de páginas internas. Lo abren tanto '/' (keyboard.ts
  // emite 'enu:search-open') como el botón táctil `[/] buscar` de la statusline.
  const abreBusqueda = (): void => {
    void import('./search').then((m) => m.openSearch());
  };
  window.addEventListener('enu:search-open', abreBusqueda);
  document
    .querySelectorAll<HTMLElement>('[data-touch="search"]')
    .forEach((btn) => btn.addEventListener('click', abreBusqueda));
}
