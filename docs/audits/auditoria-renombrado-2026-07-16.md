---
title: "Auditoría del renombrado `nu` → `enu` — 16 de julio de 2026"
type: "auditoria"
date: "2026-07-16"
status: "cerrada"
---
# Auditoría del renombrado `nu` → `enu` — 16 de julio de 2026

Ejecución del renombrado decidido en
[análisis de nombres §10](analisis-nombres-2026-07-15.md#10-resolución-nu--enu-16-jul-2026),
que a su vez cierra **R-04** de la
[auditoría de promoción](auditoria-promocion-reddit-2026-07-15.md): el binario
`nu` colisiona con Nushell (mismo ejecutable en el `PATH`) y con el Lisp *Nu*.
El proyecto pasa a llamarse **`enu`** (wordmark **`e/nu`**, backronym *Extensible
Native Userland*).

Esta auditoría **aplica** el subconjunto **mecánico y desacoplado** del
renombrado —prosa, ejemplos de CLI, dominio, URLs de repo, títulos— y **deja
inventariadas** las superficies **estructurales** que no se pueden tocar con un
`find/replace` porque acoplan con disco, imports publicados, URLs vivas o la
**API sagrada**. Los ítems llevan id `N-##`.

**Metodología.** Un solo `perl` por fichero con tres reglas y una batería de
salvaguardas, revisando el `git diff` resultante línea a línea:

- **Regla de dominio** — `nu.sh` → `enu.sh`.
- **Regla de URL de repo** — `dbareagimeno/nu` → `dbareagimeno/enu` (el remoto
  ya es `enu`; las URLs viejas apuntaban a un repo que ya no es el canónico).
- **Regla de palabra suelta** — `nu` como binario/proyecto/CLI → `enu`, con
  *lookarounds* que **excluyen**: el namespace Lua `nu.*` (`nu.fs`, `nu.task`,
  `nu.version`, …), el comodín `nu.*`, los ficheros `nu.toml`/`nu.md`/`nu.wasm`,
  las rutas `~/.config/nu`, `~/.local/share/nu`, `.nu/…`, `refs/nu/…` y la URL
  ya reescrita. Se probó primero en `filosofia.md` en seco.

Tras el barrido se hizo una **pasada adversarial** buscando dos clases de falso
positivo, que se corrigieron a mano:

1. **Namespace Lua suelto en prosa** — `` `nu` `` (entre comillas invertidas,
   sin punto) que designa la **tabla global** `nu`, no el binario. Seis
   ocurrencias revertidas a `nu` en la bitácora (el `nu` global de un worker, el
   que comparten las tasks, "inyección del global `nu`", "namespace `nu`", el de
   `nu.ui`). El criterio: `` `nu` `` = tabla Lua → **se queda `nu`**; `nu`
   binario/proceso/producto → `enu`.
2. **Module path de Go en bitácora histórica** — `github.com/dbareagimeno/nu`
   (implementacion.md S01) y "el módulo `nu` de 1.24.7 a 1.25"
   (decisiones-implementacion.md) revertidos: son referencias al **módulo Go**,
   que es estructural (N-13) y, además, bitácora que no se reescribe
   retroactivamente.
3. **Marcador de proceso documental** — `<!-- nu:interno -->` se había volteado
   a `enu:interno`, descuadrando el par con su cierre `/nu:interno` y rompiendo
   el gate de publicación web (docs.yml). Revertido en `agente.md`,
   `arquitectura.md`, `chat.md` (marcadores balanceados de nuevo).

Dos ocurrencias fronterizas se dejaron **como `enu`** por designar el *proceso*
(no la tabla): "el `nu` actual nunca cambia su entorno" y "NO al `nu` actual"
(ambas sobre `setenv`/entorno del proceso).

**Verificación.** `go build ./...` **verde** (no se tocó ni un `.go`).
`astro build` **verde** (72 páginas; deps de `web/` instaladas desde caché
offline). Balance del diff: **168 inserciones / 168 borrados** en 23 ficheros —
puras sustituciones, sin cambios estructurales colados.

---

## Veredicto: renombrado **parcial y coherente** — capa cosmética CERRADA, capa estructural INVENTARIADA

El renombrado queda **aplicado en todo lo que no rompe nada**: la prosa de
`docs/`, los ejemplos de CLI, el título de `api.md`, `CONTRIBUTING.md`, las URLs
de repo y el dominio en `web/`, y la constante `DOMAIN`. **204** apariciones de
`enu` introducidas, ninguna dentro de la API sagrada, de una ruta de disco, de
un ref de git ni de un module path.

Lo que **falta** no es olvido sino diseño: son **doce decisiones de propietario**
(N-13…N-25) que comparten una propiedad —cambiarlas rompe algo *vivo* (imports
publicados, config en disco de usuarios, URLs indexadas, preferencias en
`localStorage`) o toca la **superficie sagrada**— y por eso exigen una decisión
explícita de compatibilidad/migración, no un `sed`. Varias están **acopladas
entre sí** (el nombre del binario arrastra CI, release, instalador y build.sh; el
`base` web arrastra slugs y el plugin de enlaces) y deben moverse en **un único
commit atómico** cada grupo.

`README.md` se deja **intacto a propósito**: lo gestiona el workflow de la web de
promoción (N-23).

---

## Inventario por superficie

| # | Superficie | Clase | Estado |
|---|---|---|---|
| N-01 | `docs/` prosa + CLI (12 ficheros: problemas, providers, guia-plugins, malla, chat, sesiones, decisiones-implementacion, arquitectura, filosofia, agente, pseudocodigo, implementacion) | trivial | ✅ aplicado |
| N-02 | `docs/api.md` — título (solo la línea 1; el resto es `nu.*` sagrado) | trivial | ✅ aplicado |
| N-03 | `docs/sesiones.md` — ejemplo ilustrativo de slug (`/home/diego/enu`) | trivial | ✅ aplicado |
| N-04 | `CONTRIBUTING.md` — prosa, `cd enu`, fork URL | trivial | ✅ aplicado |
| N-05 | `install.sh` — `REPO` + URL de comentario (solo el slug del repo) | trivial | ✅ aplicado |
| N-06 | `web/src/lib/const.ts` — `DOMAIN='enu.sh'`, `GITHUB_URL` | trivial | ✅ aplicado |
| N-07 | `web/src/lib/i18n.ts` — dominio (×2) + URL de repo (×2) | trivial | ✅ aplicado |
| N-08 | `web/src/layouts/Base.astro` — `title`/`description` por defecto | trivial | ✅ aplicado |
| N-09 | `web/src/lib/markdown/remark-enlaces-wiki.mjs` — `GH_BLOB` | trivial | ✅ aplicado |
| N-10 | `web/package.json` + `package-lock.json` — `enu-docs` (lock regenerado) | trivial | ✅ aplicado |
| N-11 | `web/src/content/.../instalacion.md` (es + en) — URLs de repo | trivial | ✅ aplicado |
| N-12 | Marcador `<!-- nu:interno -->` y rutas/refs (`.nu/`, `refs/nu/`, `nu.md`) | — | 🛡 protegido (no se toca) |
| N-13 | Module path Go `github.com/dbareagimeno/nu` + 27 imports | estructural | ⏳ pendiente |
| N-14 | Namespace Lua `nu.*` (API sagrada) | estructural | ⏳ pendiente |
| N-15 | Nombre del binario (`-o nu`) → CI, release, instalador, `build.sh`, `main.go` | estructural | ⏳ pendiente |
| N-16 | Fichero de config `nu.toml` → `enu.toml` | estructural | ⏳ pendiente |
| N-17 | Rutas de disco `~/.config/nu`, `~/.local/share/nu`, `.nu/`, `nu.md` | estructural | ⏳ pendiente |
| N-18 | Refs de git `refs/nu/…` (malla, pseudocodigo) | estructural | ⏳ pendiente |
| N-19 | `base` web `/nu/` + slug `que-es-nu` + `WIKI_SLUGS` | estructural | ⏳ pendiente |
| N-20 | Theme id `nu` + claves `localStorage` `nu-theme`/`nu-lang` | estructural | ⏳ pendiente |
| N-21 | Wordmark `e/nu` + `og.png` (`generar-og.mjs`) | branding | ⏳ pendiente |
| N-22 | `DOMAIN` real (`enu.sh`/`enu.dev`) + DNS | infra | ⏳ pendiente |
| N-23 | `README.md` — portada completa | trivial | ⏳ otro WF (promoción) |
| N-24 | `docs/adr.md` — menciones históricas del nombre | editorial | ⏳ pendiente |

---

## Detalle de lo aplicado (N-01 … N-11)

Todo lo aplicado comparte una garantía: **no toca ni un byte de código Go ni de
la API**. Son cadenas de texto (prosa, comentarios, títulos, literales de
dominio/URL) cuya sustitución no cambia ninguna firma ni layout. El
`go build ./...` verde y el `astro build` verde lo confirman.

- **N-01** es el grueso: ~145 líneas de bitácora, contratos y pseudocódigo donde
  `nu` aparece como binario (`enu -e`, `enu -p`, `enu --default-config`,
  `enu --continue`), como proceso ("dos procesos `enu`", "reiniciar `enu`") o
  como nombre del producto en prosa ("`enu` es un coding harness", "la cara
  visible de `enu`"). El namespace `nu.*` que convive en esas mismas líneas
  quedó **intacto** por construcción del *lookaround*.
- **N-05 / N-11** cambian **solo el slug del repo** en el instalador y en la
  página de instalación. El patrón de artefacto (`nu-vX.Y.Z-…`), el nombre del
  binario descargado y las variables `NU_INSTALL_DIR`/`NU_VERSION` **no** se
  tocan: son N-15 (acoplan con `release.yml`).
- **N-06 / N-07** fijan el dominio a `enu.sh` (disponible y verificado, §10). La
  coordinación DNS real es N-22.

---

## Decisiones estructurales pendientes (N-13 … N-24)

Cada una explica **por qué no es mecánica** y **el camino propuesto**.

### N-13 · Module path de Go
`module github.com/dbareagimeno/nu` en `go.mod`, replicado en el `import` de
**27 ficheros** `.go`. **Por qué no es un `sed` suelto:** el module path es una
**identidad publicada**; cambiarlo obliga a reescribir *todos* los imports en
cascada y, si alguien ya hace `go get github.com/dbareagimeno/nu`, rompe su
build. **Camino:** decidir el destino (`github.com/dbareagimeno/enu` o la org
nueva `enu-lang/enu`, ya reservada, §10) y ejecutarlo en **un commit atómico**:
`go mod edit -module …` + `grep -rl 'dbareagimeno/nu' --include='*.go' | xargs
sed -i '' 's#dbareagimeno/nu#dbareagimeno/enu#g'` (excluyendo `.claude/worktrees`
y `spike/`) + `gofmt -w` + **`go build ./...` verde** como gate. La bitácora de
S01 **no** se reescribe: se añade una fila nueva documentando el rename (regla de
la bitácora histórica).

### N-14 · Namespace Lua `nu.*` (API sagrada)
Toda la API vive bajo el global `nu` (`nu.fs`, `nu.task`, `nu.version.api`, …),
consagrado en **ADR-001**. **Por qué no se toca aquí:** "la API del core es
sagrada, crece solo por adición; romper una firma rompe el mundo"
([CLAUDE.md](../../CLAUDE.md); `docs/api.md`). Renombrar el namespace a `enu.*`
sería el cambio más disruptivo posible —invalida cada plugin, cada snippet de
`pseudocodigo.md`, cada test— y **no lo exige el rebranding**: el nombre del
*producto* y el nombre del *namespace del runtime* pueden divergir sin coste (el
usuario teclea `enu` y programa contra `nu.*`, igual que Neovim se invoca `nvim`
y programa contra `vim.*`). **Camino propuesto:** **no renombrarlo**. Si algún
día se quiere, es un `G##` + ADR nuevo (que reemplace ADR-001) con un plan de
alias/deprecación, jamás un barrido. **Recomendación de esta auditoría:
mantener `nu.*`** como decisión explícita y anotarla.

### N-15 · Nombre del binario compilado
`-o nu` en `ci.yml` (línea 115) y `release.yml` (`dist/nu`, artefacto
`nu-vX.Y.Z-<os>-<arch>`), el binario esperado por `install.sh`, el blob
`internal/vmwasm/nu.wasm` + su shim `nu_shim.c`, y las cadenas de ayuda/uso +
comentario de cabecera de `main.go`. **Por qué no es mecánico:** el nombre del
artefacto de `release.yml` y el que espera `install.sh` deben coincidir **1:1** o
el instalador rompe; y renombrar las cadenas de ayuda de `main.go` a `enu`
mientras el `go build` sigue emitiendo `nu` deja un binario que se anuncia con un
nombre que no tiene. Es **un solo cambio acoplado**. **Camino:** en el mismo
commit — `-o enu`, patrón `enu-vX.Y.Z-…`, binario esperado `enu`, blob
`enu.wasm` (mover el fichero comiteado + shim), y las cadenas de `main.go`;
validar con un `install.sh` de prueba contra un release *dry-run*. Va **después
o junto a N-13** (comparten el commit de "renombrado del ejecutable").

### N-16 · Fichero de configuración `nu.toml`
`config.dir()/nu.toml` en `main.go` (múltiples líneas) y citado en contratos y
`mcp.md`. **Por qué no es cosmético:** es una ruta **persistida en disco de
usuarios reales**; cambiarla a `enu.toml` sin plan de migración deja a todo el
que ya tenga config existente con un fichero que el binario nuevo ignora.
**Camino:** decidir `enu.toml` + una regla de compatibilidad (leer `enu.toml` y,
si no existe, caer a `nu.toml` con aviso de *deprecation*), implementada en el
resolutor de config del binario. Acopla con N-17 (viven en el mismo directorio).

### N-17 · Rutas de disco y ficheros de proyecto
`~/.config/nu/`, `~/.local/share/nu/` (api.md §, arquitectura.md), el directorio
de confianza del repo `.nu/skills/`, `.nu/mesh/`, y el fichero `nu.md` (contexto
de repo, contrato de `agente.md`). **Por qué no es mecánico:** igual que N-16
(config en disco) más un agravante: `.nu/` y `nu.md` viven **en repos de
terceros**; pasar a `.enu/` / `enu.md` rompe la compatibilidad con cualquier
repo que ya los use. **Camino:** decisión de mapeo (`~/.config/enu`, `.enu/`,
`enu.md`) + fallback de lectura hacia los nombres viejos durante una ventana de
*deprecation*; aplicar a la vez en el binario y en los contratos
(`agente.md`, `arquitectura.md`, `api.md`).

### N-18 · Refs de git de la malla
`refs/nu/mesh/claims/<id>`, `refs/nu/claims/<id>` (`malla.md`, `pseudocodigo.md`).
**Por qué no es mecánico:** son una **convención de protocolo** del contrato
`mesh` (borrador v0.1, **§11 aún abierta**); cambiar el prefijo a `refs/enu/…`
solo tiene sentido cuando se cierre §11, y coordinado con `.nu/mesh/` (N-17).
**Camino:** resolverlo dentro del cierre de `malla.md` §11, no antes.

### N-19 · `base` de la web y slugs públicos
`base: '/nu/'` en `astro.config.mjs`, duplicado como `const BASE = '/nu'` en el
plugin de enlaces, más el slug público `que-es-nu` (`const.ts` `DOCS_FIRST`,
`docmap.ts`, `WIKI_SLUGS`). **Por qué no es mecánico:** el `base` es la **ruta
pública** del sitio (GitHub Pages *project page*); `/nu/docs/x` está indexado y
enlazado. Cambiarlo depende de la **decisión de hosting**: si el repo GH Pages
pasa a `enu`, el `base` es `/enu/` de forma natural; si se sirve en dominio
propio `enu.sh`, el `base` se **vacía**. Y renombrar el slug `que-es-nu` rompe
`/nu/docs/que-es-nu`. **Camino:** decidir hosting (acopla con N-22) y, en un
commit, mover `astro.config.mjs` + `remark-enlaces-wiki.mjs` (`BASE`) + —si se
renombra el slug— `docmap.ts` + `WIKI_SLUGS` + `const.ts` + el fichero de
contenido, con un *redirect* del slug viejo.

### N-20 · Theme id y claves de `localStorage`
El id de tema `"nu"` (uno de `nu`/`dracula`/`gruvbox`/`solarized`) usado como
`data-theme`, selector CSS y en `check-contraste.mjs`; y las claves
`localStorage` `nu-theme`/`nu-lang` (`Base.astro`). **Por qué no es mecánico:**
renombrar la clave de `localStorage` **resetea la preferencia guardada** de cada
visitante actual; y el theme id tiene *blast radius* por CSS/JS/script de
contraste. **Camino:** decidir si el tema se rebautiza `enu` (barrido coordinado
de `Base.astro` + `check-contraste.mjs` + estilos + `generar-og.mjs`) o se
conserva como **id técnico interno** (recomendado: desacopla branding de
implementación). Para las claves: migrar leyendo la vieja una vez, o aceptar el
reset como coste del rebrand.

### N-21 · Wordmark y `og.png`
`generar-og.mjs` dibuja el wordmark `nu` y el slogan `nu.sh/install` **dentro**
de `public/og.png`. **Por qué no es una edición de texto:** cambiar el `.mjs` sin
**regenerar** el PNG deja la imagen social mostrando `nu`; y el wordmark es una
decisión de **diseño** (`e/nu`, §10) acoplada al theme (N-20). **Camino:** fijar
el wordmark a `e/nu` y el slogan a `enu.sh/install` en el script y **correr
`node scripts/generar-og.mjs`** (necesita `sharp`, ya en `node_modules`) en el
mismo commit que actualiza el PNG; hacerlo junto al rebrand del theme.

### N-22 · Dominio real y DNS
`DOMAIN` ya apunta a `enu.sh` en el código (N-06), pero la **publicación real**
exige registrar el DNS y decidir el primario (`enu.sh` vs `enu.dev`, ambos
disponibles §10). Acopla con N-19 (si hay dominio propio, el `base` web se
vacía). **Camino:** decisión de infraestructura fuera del repo; al cerrarla,
revisar N-19.

### N-23 · `README.md`
Portada completa (título/wordmark, badges de CI, ejemplos de CLI, rutas de
config, `nu.toml`, `.nu/`, `nu.md`). **No tocado a propósito:** lo gestiona el
**workflow de la web de promoción**, que además debe decidir el wordmark de
portada y coordina con N-15/N-16/N-17. Se deja para ese flujo.

### N-24 · `docs/adr.md`
Menciones a `nu` como binario/proyecto en ADRs **ya aceptados** (ADR-001,
ADR-010, ADR-013, …). **Por qué no se reescribe:** regla del flujo —"las
entradas de ADR **nunca se reescriben** in-place". **Camino:** si se quiere
dejar rastro del rename, un **ADR nuevo** ("Renombrado del proyecto a `enu`") que
lo registre y referencie, no un `find/replace` sobre el texto histórico. ADR-001
(namespace `nu.*`) se mantiene vigente per N-14.

---

## Cierre

Con esta pasada, **R-04** de la
[auditoría de promoción](auditoria-promocion-reddit-2026-07-15.md) —el choque de
`PATH` con Nushell— queda **operativamente encarrilado**: el nombre público, la
prosa, el dominio y los enlaces ya dicen `enu`, y el único paso que *de verdad*
elimina la colisión de `PATH` —renombrar el **binario** de `nu` a `enu`
(N-15)— está inventariado con su camino atómico y sus acoplamientos (N-13, N-16,
N-17). La API sagrada `nu.*` se recomienda **intacta** (N-14): el producto es
`enu`, el runtime programa contra `nu.*`, y esa divergencia es deliberada, no una
deuda.
