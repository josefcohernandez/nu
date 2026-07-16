// Plugin remark de admonitions. El contenido de la web usa la sintaxis de
// contenedores `:::note[Título] … :::` (tipos: note/tip/caution), pero el sitio
// es Astro puro sin Starlight: sin este plugin (y sin `remark-directive`, que
// añade la gramática al parser) los `:::` llegaban al HTML como texto literal.
// Aquí se transforma cada contenedor en un `<aside class="admonition …">` con
// su título; los estilos viven en `src/styles/admonitions.css`.
//
// RED DE SEGURIDAD (deliberada): activar `remark-directive` hace que cualquier
// `:palabra` / `::palabra` de la prosa parsee como textDirective/leafDirective
// y, sin handler, remark-rehype los descartaría en silencio — la wiki
// sincronizada de `docs/` está llena de `algo:algo` en prosa. Por eso todo
// directive que NO sea un contenedor note/tip/caution se reconstruye como el
// texto literal que era, en vez de perderse.

import { visit } from 'unist-util-visit';

// Tipos soportados y su título por defecto cuando el autor no pone `[Título]`.
// El idioma se decide por la ruta del fichero (misma técnica que
// remark-limpieza-interno): la instantánea EN vive bajo `content/en/`.
const TITULOS = {
  es: { note: 'Nota', tip: 'Consejo', caution: 'Cuidado' },
  en: { note: 'Note', tip: 'Tip', caution: 'Caution' },
};

function textoLiteral(valor) {
  return { type: 'text', value: valor };
}

// Reconstruye un textDirective/leafDirective como los nodos literales que el
// autor escribió: `:name` / `::name`, con el `[label]` (los children) entre
// corchetes si lo había. Los atributos `{…}` no se reconstruyen: no aparecen
// en el contenido y reconstruirlos exacto es imposible desde el mdast.
function reconstruyeInline(nodo, prefijo) {
  const hijos = nodo.children || [];
  if (hijos.length === 0) return [textoLiteral(`${prefijo}${nodo.name}`)];
  return [textoLiteral(`${prefijo}${nodo.name}[`), ...hijos, textoLiteral(']')];
}

// Un containerDirective de nombre desconocido se devuelve como bloque literal:
// párrafo `:::name`, su contenido intacto, párrafo `:::` de cierre.
function reconstruyeBloque(nodo) {
  const abre = { type: 'paragraph', children: [textoLiteral(`:::${nodo.name}`)] };
  const cierra = { type: 'paragraph', children: [textoLiteral(':::')] };
  return [abre, ...(nodo.children || []), cierra];
}

export function remarkAdmonitions() {
  return (tree, file) => {
    const ruta = (file?.path || file?.history?.[0] || '').replace(/\\/g, '/');
    const idioma = ruta.includes('/content/en/') ? 'en' : 'es';

    visit(tree, (nodo, indice, padre) => {
      if (!padre || indice === undefined) return;
      if (
        nodo.type !== 'containerDirective' &&
        nodo.type !== 'leafDirective' &&
        nodo.type !== 'textDirective'
      ) {
        return;
      }

      // Directives que no son admonitions: de vuelta a texto literal.
      if (nodo.type === 'textDirective') {
        padre.children.splice(indice, 1, ...reconstruyeInline(nodo, ':'));
        return indice;
      }
      if (nodo.type === 'leafDirective') {
        const literal = { type: 'paragraph', children: reconstruyeInline(nodo, '::') };
        padre.children.splice(indice, 1, literal);
        return indice;
      }
      if (!(nodo.name in TITULOS.es)) {
        padre.children.splice(indice, 1, ...reconstruyeBloque(nodo));
        return indice;
      }

      // Admonition: <aside class="admonition admonition-<tipo>"> con título.
      // El `[Título]` llega como primer paragraph marcado con directiveLabel;
      // si no lo hay, se inserta el título por defecto del idioma de la página.
      const etiqueta = nodo.children?.[0];
      if (etiqueta?.data?.directiveLabel) {
        etiqueta.data.hName = 'p';
        etiqueta.data.hProperties = { className: ['admonition-title'] };
      } else {
        nodo.children = nodo.children || [];
        nodo.children.unshift({
          type: 'paragraph',
          data: { hName: 'p', hProperties: { className: ['admonition-title'] } },
          children: [textoLiteral(TITULOS[idioma][nodo.name])],
        });
      }

      nodo.data = nodo.data || {};
      nodo.data.hName = 'aside';
      nodo.data.hProperties = { className: ['admonition', `admonition-${nodo.name}`] };
    });
  };
}
