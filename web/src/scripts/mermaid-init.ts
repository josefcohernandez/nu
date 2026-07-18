// Render client-side de los diagramas mermaid de la wiki (p. ej.
// docs/core/modelo-ejecucion.md). El plugin remark-enlaces-wiki convierte cada
// ```mermaid en `<pre class="mermaid">` con la fuente escapada; aquí se importa
// mermaid dinámicamente SOLO si la página contiene alguno, se configura con las
// custom properties del theme activo y se re-renderiza al cambiar data-theme.
//
// Fallback sin JS: el `<pre>` muestra la fuente del diagrama.

export async function initMermaid(): Promise<void> {
  const bloques = Array.from(document.querySelectorAll<HTMLElement>('pre.mermaid'));
  if (bloques.length === 0) return;

  // Guarda la fuente original: mermaid.run() sustituye el contenido por el SVG,
  // así que para re-renderizar (cambio de theme) hay que restaurarla.
  const fuentes = bloques.map((b) => b.textContent ?? '');

  const mermaid = (await import('mermaid')).default;

  function tokens() {
    const cs = getComputedStyle(document.documentElement);
    const g = (n: string) => cs.getPropertyValue(n).trim();
    return {
      bg: g('--bg'),
      fg: g('--fg'),
      bright: g('--bright'),
      dim: g('--dim'),
      border: g('--border'),
      key: g('--key'),
    };
  }

  function render(): void {
    const t = tokens();
    mermaid.initialize({
      startOnLoad: false,
      securityLevel: 'loose', // documentación propia y de confianza; permite <br/>
      theme: 'base',
      fontFamily: "'IBM Plex Mono', ui-monospace, monospace",
      themeVariables: {
        darkMode: true,
        background: t.bg,
        mainBkg: t.border,
        primaryColor: t.border,
        primaryTextColor: t.fg,
        primaryBorderColor: t.key,
        secondaryColor: t.border,
        secondaryTextColor: t.fg,
        secondaryBorderColor: t.dim,
        tertiaryColor: t.bg,
        tertiaryTextColor: t.fg,
        tertiaryBorderColor: t.border,
        lineColor: t.dim,
        textColor: t.fg,
        nodeBorder: t.key,
        clusterBkg: t.bg,
        clusterBorder: t.border,
        edgeLabelBackground: t.bg,
        labelBackground: t.bg,
        titleColor: t.bright,
        actorBkg: t.border,
        actorBorder: t.key,
        actorTextColor: t.fg,
        signalColor: t.fg,
        signalTextColor: t.fg,
        noteBkgColor: t.border,
        noteTextColor: t.bright,
        noteBorderColor: t.dim,
      },
    });
    // Restaura fuente y marca de no-procesado, luego re-renderiza.
    bloques.forEach((b, i) => {
      b.removeAttribute('data-processed');
      b.textContent = fuentes[i];
    });
    void mermaid.run({ nodes: bloques });
  }

  render();

  // Re-render instantáneo al cambiar el theme (data-theme en <html>).
  const obs = new MutationObserver((muts) => {
    for (const m of muts) {
      if (m.attributeName === 'data-theme') {
        render();
        break;
      }
    }
  });
  obs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
}
