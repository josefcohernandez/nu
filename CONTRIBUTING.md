# Contribuir a `nu`

Gracias por el interés. `nu` es software libre bajo la [Apache License
2.0](LICENSE) y las aportaciones son bienvenidas: issues, ideas, reproducciones
de bugs y parches.

Antes de nada, lee la guía del proyecto: [CLAUDE.md](CLAUDE.md) (flujo de
trabajo, idioma y estilo), [docs/filosofia.md](docs/filosofia.md) (lo que `nu`
es y lo que no) y, si tocas código, [docs/implementacion.md](docs/implementacion.md)
(el protocolo de construcción). Todo el repositorio está en español; la API y
los identificadores, en inglés `snake_case`.

## Calidad

Toda aportación de código debe dejar el repositorio en verde:

- `go build ./...`
- `go test -race ./...`
- `go vet ./...` y `gofmt` sin diferencias

La integración continua (ver [`.github/workflows/ci.yml`](.github/workflows/ci.yml))
comprueba esto en cada Pull Request, en Linux y macOS. La API del core es
**sagrada** ([docs/api.md](docs/api.md)): crece solo por adición; si crees que
falta algo, ábrelo como discusión antes de implementarlo.

## Titularidad y licencia de las contribuciones

`nu` lo mantiene su autor original, que conserva la titularidad del proyecto
para poder, llegado el caso, ofrecer versiones comerciales o relicenciarlo. Para
que eso siga siendo posible, las aportaciones externas se gestionan **caso por
caso**:

- Por defecto, **abrir un issue para discutir** un cambio antes de enviar un
  Pull Request grande. Los parches pequeños y las correcciones evidentes pueden
  ir directos.
- Al fusionar código de terceros, el mantenedor **puede pedir una cesión de
  derechos o la firma de un acuerdo de contribución (CLA)** antes de
  incorporarlo. Esto mantiene la titularidad unificada del proyecto.
- Mientras no exista un CLA formal, no des por hecho que un Pull Request se
  fusionará tal cual: puede requerir ese paso. Si prefieres conservar el
  copyright de tu aportación sin cederlo, dilo en el Pull Request y se valorará
  (puede que se rechace, se reimplemente, o se acepte como excepción anotada).

Esto no busca poner barreras a la comunidad, sino dejar el marco claro desde el
principio: `nu` es libre para usar, estudiar, modificar y distribuir bajo Apache
2.0, y a la vez su autor preserva la opción de comercializarlo en el futuro.
