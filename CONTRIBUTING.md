# Contribuir a `enu`

Gracias por el interés. `enu` es software libre bajo la [Apache License
2.0](LICENSE) y las aportaciones son bienvenidas: issues, ideas, reproducciones
de bugs y parches.

Antes de nada, lee la guía del proyecto: [CLAUDE.md](CLAUDE.md) (flujo de
trabajo, idioma y estilo), [docs/core/filosofia.md](docs/core/filosofia.md) (lo que `enu`
es y lo que no) y, si tocas código, [docs/plan/implementacion.md](docs/plan/implementacion.md)
(el protocolo de construcción). Todo el repositorio está en español; la API y
los identificadores, en inglés `snake_case`.

## Cómo abrir una Pull Request

1. Haz fork del repositorio y clónalo:

   ```
   gh repo fork dbareagimeno/enu --clone
   cd enu
   ```

2. Crea una rama descriptiva para tu cambio **desde `develop`** (la rama de
   integración y por defecto del repo; `main` queda reservada para versiones
   estables):

   ```
   git checkout -b mi-cambio develop
   ```

3. Haz tus cambios y comitea. Sigue el idioma y estilo del repo (ver
   [CLAUDE.md](CLAUDE.md)): documentos y mensajes de commit en español, API e
   identificadores en inglés `snake_case`.

4. Antes de empujar, deja el repo en verde localmente (ver [Calidad](#calidad)
   más abajo).

5. Empuja tu rama y abre la PR contra `develop`:

   ```
   git push -u origin mi-cambio
   gh pr create --base develop --title "..." --body "..."
   ```

   (o, sin `gh`, desde la web de tu fork con el botón "Compare & pull
   request").

6. Las ramas `main` y `develop` están protegidas: tu PR no se puede fusionar hasta que los
   checks de CI (`.github/workflows/ci.yml`) estén en verde y haya al menos
   una revisión aprobada. Si tu PR viene de un fork, el mantenedor debe
   aprobar manualmente que corra el primer CI (política de seguridad de
   GitHub para workflows de forks).

7. Si el cambio es grande o toca la API (`docs/contracts/api.md`), abre antes un issue
   para discutirlo — evita trabajo perdido si la dirección no encaja (ver
   [Titularidad y licencia](#titularidad-y-licencia-de-las-contribuciones)
   más abajo).

## Calidad

Toda aportación de código debe dejar el repositorio en verde:

- `go build ./...`
- `go test -race ./...`
- `go vet ./...` y `gofmt` sin diferencias
- `golangci-lint run` con la versión fijada en la CI

La integración continua (ver [`.github/workflows/ci.yml`](.github/workflows/ci.yml))
comprueba esto en cada Pull Request, en Linux y macOS. La API del core es
**sagrada** ([docs/contracts/api.md](docs/contracts/api.md)): crece solo por adición; si crees que
falta algo, ábrelo como discusión antes de implementarlo.

## Titularidad y licencia de las contribuciones

`enu` lo mantiene su autor original, que conserva la titularidad del proyecto
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
principio: `enu` es libre para usar, estudiar, modificar y distribuir bajo Apache
2.0, y a la vez su autor preserva la opción de comercializarlo en el futuro.
