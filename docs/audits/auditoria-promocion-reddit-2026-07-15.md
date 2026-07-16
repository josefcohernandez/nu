# Auditoría de preparación para promoción (Reddit / HN) — 15 de julio de 2026

Auditoría de la preparación de `nu` para promocionarse en comunidades técnicas
(Reddit, Hacker News) con una pregunta de partida concreta: **si mañana estuviera
todo en inglés, ¿está el proyecto listo para enseñarlo a desconocidos y que lo
entiendan —y quieran probarlo— en menos de un minuto?**

El documento fusiona **dos evaluaciones independientes**: una inspección directa
del repositorio, sus ramas, releases y estado de despliegue; y la valoración de
un revisor externo centrada en la superficie visible en GitHub. Como las
auditorías previas, acompaña cada hallazgo de una **solución accionable**.

**Metodología.** Dos fuentes, marcadas por hallazgo en la columna «Fuente» del
resumen:

- **Inspección del repo** — README en `main`/`develop`, `git`/`gh` sobre ramas,
  tags y releases, estado de GitHub Pages, `main.go` (strings del CLI),
  `web/src/lib/const.ts`, `.github/workflows/docs.yml`, `go.mod`, y la auditoría
  de diseño web del mismo día ([`auditoria-web-diseno-2026-07-15.md`](auditoria-web-diseno-2026-07-15.md),
  hallazgos W-##).
- **Revisor externo** — lectura de la superficie pública del repositorio en
  GitHub (README, CI, releases, perfil), asumiendo el proyecto ya traducido al
  inglés.

Los ítems llevan id `R-##`. Severidad: 🔴 alta (rompe una función, el primer
minuto, o es un bloqueante estratégico), 🟡 media (fricción real / entierra la
calidad técnica), 🔵 baja (pulido, criterio o estrategia de publicación).

---

## Veredicto: **6,5/10** para promocionar hoy — pero el número engaña sin las capas

Las dos evaluaciones dan cifras distintas (**6,5/10** el revisor externo, **~4/10**
la inspección del repo) y **no se contradicen**: describen dos capas del problema.
El revisor externo juzgó la superficie de GitHub *asumiendo que todo funciona* y
no llegó al estado de despliegue ni al dominio de instalación; la inspección
juzgó el estado real de arranque de hoy. La síntesis honesta es por **puertas
acumulativas**:

| Puerta | Qué falta | Estado resultante |
|---|---|---|
| **1. Mecánica de lanzamiento** | Release fresca · web desplegada · dominio real · 404/`:::tip` · los dos 🔴 web | Sin esto, **~4/10** hoy: el visitante choca con un muro en el primer minuto (web vieja, dominio muerto, binario atrasado). No es momento de publicar. |
| **2. Presentación y posicionamiento** | Demo/GIF · README como landing · perfil de GitHub · comparación · menos ruido de proceso | Resuelta la puerta 1 pero no esta, **~6,5/10** — la cifra del revisor externo. Técnicamente respetable, pero la calidad sigue enterrada. |
| **3. Identidad** | Resolver la colisión de nombre `nu` ↔ Nushell | Con las tres puertas, **~8,5/10**. |

Sub-valoraciones del revisor externo (suponiendo inglés), que esta auditoría
suscribe:

- **Calidad técnica percibida: 8,5/10** — parece un proyecto real y trabajado,
  no una demo de fin de semana.
- **Documentación técnica: 9/10** — arquitectura, ADR, referencia de API,
  filosofía, modelo de ejecución y guía de plugins, todo extenso y coherente.
- **Presentación comercial/comunitaria: 5/10** — el diferenciador arquitectónico
  está clarísimo; el **beneficio práctico**, mucho menos.
- **Preparación global para Reddit: 6,5/10**.

**La tesis del documento en una frase:** la calidad de `nu` está por encima de
muchos proyectos que se promocionan en Reddit, pero **está enterrada bajo
demasiada explicación arquitectónica y muy poca demostración visual** — y, por
debajo de eso, bajo una mecánica de lanzamiento que hoy ni siquiera enseña la
versión nueva.

---

## Lo que ya está muy bien (y sostiene el 8,5 de fondo)

No es palabrería previa a la crítica: es el capital que la presentación está
desaprovechando.

- **Planteamiento diferencial real:** un runtime de Lua embebido en un binario
  estático de Go, con el coding agent, los providers, MCP, el chat y el REPL
  implementados como extensiones Lua sobre una API congelada. No es "otro CLI
  para llamar a un modelo".
- **Producto ejecutable, no vaporware:** `go build ./...` limpio, `go test ./...`
  en verde (107 ficheros de test), 45/45 sesiones del plan cerradas, ~16.400
  líneas de Go y ~10.700 de Lua, 316 commits de historia incremental.
- **Distribución cuidada:** instalador con verificación de checksum, releases
  descargables, compilación sin CGO para Linux y macOS.
- **CI especialmente sólida:** formato, `go vet`, módulos, lint, builds en Linux
  y macOS, smoke tests, tests con detector de carreras y verificación
  reproducible del blob WASM.
- **Gobernanza seria:** licencia Apache 2.0, `CONTRIBUTING.md` detallado, un
  método de diseño explícito (docs como espec, validación por pseudocódigo, ADRs).

En una comunidad técnica, quien llegue a leerlo con calma respetará el trabajo.
El problema es todo lo que ocurre **antes** de que llegue a leerlo con calma.

---

## Hallazgos y soluciones

### 🔴 R-01 — El `curl | sh` instala una versión atrasada respecto a lo que promete el README

**Problema.** La última release, `v0.1.3`, es del **28-jun-2026**; `main` va
**102 commits por delante** de ella. El instalador descarga "la última release
estable" (`install.sh`), así que el visitante que copia el `curl | sh` del README
obtiene un binario que **no es** el "kernel construido / las 45 sesiones cerradas"
que el propio README proclama dos líneas más arriba (`README.md:18-19`). La
promesa y lo que se instala divergen por dos semanas y media de trabajo.

**Evidencia.** `gh release list` (`v0.1.3`, 2026-06-28); `git rev-list --count
v0.1.3..origin/main` → 102; `README.md:18-19`; `install.sh` (descarga la última
estable).

**Solución.** Cortar una release nueva **justo antes de publicar** (el
`release.yml` ya está montado: es un tag). Que la versión instalable sea la que
el README describe. Marcar en el propio README la línea de versión (p. ej.
"probado en `vX.Y.Z`") para que la coherencia sea verificable de un vistazo.

> 🧭 **ENCAMINADO (16-jul-2026).** No automatizable en este workflow: cortar una
> release es una acción del propietario (decidir versión + `git tag` + dejar
> correr `release.yml`). Pasos, responsable y criterio de hecho quedan escritos
> en la [Directriz de cierre R-01](#directriz-r-01--cortar-una-release-alineada-con-el-readme). El README ya
> renombrado (`enu`) queda listo para que la línea "probado en `vX.Y.Z`" se
> añada al cortar.

### 🔴 R-02 — La web pública muestra el diseño ANTIGUO: el rediseño no está en `main`

**Problema.** El rediseño completo de la web ("la web ES un terminal", commit
`af10897`) vive en `claude/web-rediseno` y `develop`, **no en `main`**. El
workflow `docs.yml` despliega a GitHub Pages **solo en push a `main` que toque
`web/**`**, de modo que la URL pública (https://dbareagimeno.github.io/nu/) sigue
sirviendo la web **anterior** al rediseño; el último despliegue data de finales
de junio. Enviar a alguien a esa URL hoy enseña una versión que ya no representa
al proyecto.

**Evidencia.** `.github/workflows/docs.yml:4-9` (trigger); rama del commit
`af10897`; ausencia de despliegue posterior en Pages.

**Solución.** Fusionar a `main` el trabajo de presentación que esté terminado
(coordinado con R-06, R-14) y dejar que `docs.yml` despliegue. No promocionar
ninguna URL hasta confirmar que la pública ya muestra el rediseño.

> 🧭 **ENCAMINADO (16-jul-2026).** No automatizable en este workflow: `main`
> está reservada a versiones estables (modelo de ramas de CLAUDE.md), así que
> el merge `develop`→`main` y el corte estable son decisión del propietario.
> Pasos y criterio de hecho en la [Directriz de cierre R-02](#directriz-r-02--merge-a-main-y-despliegue-del-rediseño-web).

### 🔴 R-03 — El CTA de instalación de la portada apunta a un dominio placeholder (`nu.sh`)

**Problema.** La portada ofrece "instálalo con una línea" y copia al portapapeles
`curl -fsSL nu.sh/install | sh`, pero `nu.sh` es un **placeholder** —el dominio
real está sin decidir—. Un visitante que pega ese comando **falla**. Es el peor
sitio posible para un fallo: el gesto central del pitch ("una línea y a
trabajar") roto en el primer intento. (Nótese que el `curl` del **README** usa
`raw.githubusercontent.com` y sí funciona; el problema es específico del CTA de
la web.)

**Evidencia.** `web/src/lib/const.ts:6` (`DOMAIN = 'nu.sh'`) y `:10`
(`INSTALL_CMD = curl -fsSL ${DOMAIN}/install | sh`).

**Solución.** Decidir el dominio real y configurarlo, **o** —hasta tenerlo— hacer
que el CTA use la misma URL de `raw.githubusercontent.com` que ya funciona en el
README. Un solo cambio en `const.ts` cierra el agujero. No promocionar con el
placeholder puesto.

> 🧭 **ENCAMINADO (16-jul-2026).** No automatizable en este workflow: elegir/comprar
> el dominio real (`enu.sh`/`enu.dev`, libres según R-04) es una acción externa
> del propietario. La alternativa puente (apuntar el CTA al `raw.githubusercontent`
> que ya funciona) sí es un cambio de `const.ts`, pero decidir entre puente y
> dominio propio es del propietario. Pasos y criterio de hecho en la
> [Directriz de cierre R-03](#directriz-r-03--dominio-de-instalación-real).

### 🔴 R-04 — Colisión de nombre con Nushell: `nu` ya es un ejecutable conocido

**Problema.** Probablemente el punto más serio a medio plazo, y el que el revisor
externo señala como el que **por sí solo impide un 8 o más**. Nushell se conoce
habitualmente como **Nu** y su ejecutable también se llama `nu`, con binarios
oficiales para Linux, macOS y Windows. Consecuencias previsibles en una
publicación:

- Conflicto directo en el `PATH` de mucha gente.
- Dificultad para buscar el proyecto ("nu" devuelve Nushell).
- Confusión en titulares tipo "I built nu" y comentarios preguntando si es un
  fork o algo relacionado con Nushell.
- Fricción futura con Homebrew, gestores de paquetes y documentación.

**Evidencia.** El ecosistema Nushell (`nushell/nushell`) distribuye el binario
`nu`; conocimiento de dominio, no del repo.

**Solución.** Aunque no se cambie el **nombre del proyecto**, considerar en serio
cambiar el **nombre del ejecutable** (o al menos ofrecer un alias/segundo nombre
y documentar la coexistencia). Como mínimo imprescindible antes de publicar:
tener una respuesta preparada y visible ("no, no es Nushell; se llama así por…")
para no gastar el hilo de Reddit en desambiguar. Este punto es transversal: es la
puerta 3 del veredicto.

> ✅ **RESUELTO (16-jul-2026).** El proyecto se renombra `nu` → **`enu`**
> (wordmark **`e/nu`**, backronym *Extensible Native Userland*: **E**xtensible ·
> **N**ative · **U**serland; el «e» guiña además al modelo Emacs). `enu.dev` y
> `enu.sh` libres, org `enu-lang`, binario `enu` sin colisión de PATH. Proceso
> y disponibilidad en [`analisis-nombres-2026-07-15.md`](analisis-nombres-2026-07-15.md) §10.

### 🔴 R-05 — No hay demostración visual: para un producto de terminal, es *la* pieza que falta

**Problema.** El README arranca con una descripción arquitectónica densa; no hay
arriba ni una captura, ni un GIF del agente en acción, ni un vídeo corto.
Tampoco existe ningún `.gif`/`.cast` en el repo. Para un producto de **terminal**
esto es especialmente caro: el lector tiene que *imaginarse* el producto a partir
de varios párrafos técnicos, cuando bastarían 30 segundos de vídeo para que lo
*vea*. Es el hallazgo donde las dos evaluaciones coinciden con más fuerza.

**Evidencia.** `README.md:6-16` (arranque arquitectónico); ausencia de assets
visuales en el árbol.

**Solución.** Un GIF/asciinema de **20–40 s** al principio del README (y en la
portada). Idealmente encadenando lo que hoy no se ve: el chat interactivo
funcionando · una tool pidiendo permiso · el agente **modificando un archivo** ·
un plugin Lua cambiando el comportamiento · un `nu -p '…'` headless dentro de un
script. Es el mayor retorno por esfuerzo que le queda al lanzamiento.

> 🧭 **ENCAMINADO (16-jul-2026).** No automatizable en este workflow: grabar la
> demo requiere una máquina real con un provider vivo. El README ya reserva el
> hueco arriba del todo con un `<!-- TODO(R-05) -->`. Un **guion de asciinema
> comando a comando, listo para grabar**, queda escrito en la
> [Directriz de cierre R-05](#directriz-r-05--demo-visual-gifasciinema).

### 🟡 R-06 — La página estrella tiene enlaces a 404 y cajas `:::tip` sin renderizar

**Problema.** La página de entrada de la documentación, `que-es-nu`, cierra con
**dos enlaces muertos** (`/nu/empezando/…`, cuando la ruta real es `/nu/docs/…`),
y las directivas `:::tip` salen como **texto literal** en cuatro páginas
—incluida esa misma— porque Astro no tiene `remark-directive` configurado. Son
11 enlaces rotos en total. Es exactamente lo primero que ve quien pulsa
"documentation": se ve a medio hacer.

**Evidencia.** Reescritor `web/src/lib/markdown/remark-enlaces-wiki.mjs` (solo
corrige enlaces `.md` relativos, no los absolutos hardcodeados);
`web/astro.config.mjs` (sin `remark-directive`); páginas `que-es-nu`,
`instalacion`, `primer-script`, `primer-agente`.

**Solución.** (a) Configurar `remark-directive` (o convertir los `:::tip` al
componente/markup que la web sí renderiza). (b) Corregir los enlaces
`/nu/empezando/…` → `/nu/docs/…`, o ampliar el reescritor para normalizar también
enlaces absolutos internos. Coste bajo, quita el mayor "tell" de borrador de la
cara visible.

> 🟡 **PARCIAL (16-jul-2026).** La mitad de los enlaces 404 (`/nu/empezando/…` →
> `/nu/docs/…`) ya la cerró el *Addendum de resolución* de la auditoría de diseño
> web (rama `claude/auditoria-web`, "enlaces del contenido a rutas inexistentes").
> **Sigue abierto** el otro fallo: `web/astro.config.mjs` no carga `remark-directive`
> y quedan 10 directivas `:::tip` vivas (5 ES + 5 EN) que renderizan como texto
> literal. Este workflow de cierre **no** toca `web/` (es edición de la app, no
> de la promoción), así que se deja explícitamente pendiente. Aviso de coherencia
> para quien lo cierre: con el proyecto renombrado, los enlaces internos serán
> `/enu/docs/…`, no `/nu/docs/…` — el reescritor debe usar el base path nuevo.

### 🟡 R-07 — El README explica mejor *cómo está construido* que *por qué usarlo*, y es demasiado largo para ser landing

**Problema.** Como **documentación de referencia** el README es excelente (9/10).
Como **primera presentación** falla en dos cosas. Primero, arranca con la tesis
arquitectónica ("un runtime Lua terminal-first cuya killer app es un coding
harness") que **presupone** que el lector ya sabe por qué la quiere; no responde
antes a lo básico: ¿qué problema resuelve?, ¿para quién?, ¿por qué usarlo en vez
de otro coding agent?, ¿qué se puede personalizar aquí que no en otros?, ¿cuál es
el ejemplo concreto que demuestra la ventaja? Segundo, es demasiado largo para
una landing: instalación, códigos de salida, configuración, providers, permisos,
extensiones, plugins, modelo de ejecución y documentación interna, todo seguido
(469 líneas).

**Evidencia.** `README.md` (469 líneas; `:6-16` tesis arquitectónica como primera
pantalla; índice con toda la referencia operativa).

**Solución.** Reescribir **las dos primeras pantallas** alrededor del beneficio
para el usuario y mover el resto a la web de docs. Estructura sugerida para
promoción: 1) propuesta de valor · 2) GIF (R-05) · 3) tres motivos para usarlo ·
4) instalación + demo de 60 s · 5) comparación con alternativas (R-10) · 6)
arquitectura resumida · 7) enlace a la documentación detallada. El contenido
actual no se tira: se recoloca.

> ✅ **RESUELTO (16-jul-2026).** El README (`README.md`) se reescribió como
> *landing*: abre con un bloque **"En 30 segundos"** que lidera con el problema
> y el beneficio (agente propio, un binario, cero dependency hell) antes de la
> tesis arquitectónica, sigue con **"Un vistazo"** (instalar + turno headless +
> chat), una sección **"En qué se diferencia (honestamente)"** (R-10), y solo
> *después* "Cómo funciona, por dentro". La referencia operativa detallada se
> apoya en enlaces a `docs/`. La reescritura además **propagó el renombrado
> `nu`→`enu`** en título, prosa, badges (`dbareagimeno/enu`), binario (`enu`) y
> el `curl …/enu/main/install.sh`. Un juez lector-desconocido lo puntuó **8/10**
> (aprobado).

### 🟡 R-08 — El perfil del repositorio está vacío (descripción, web y topics)

**Problema.** GitHub reporta el repo **sin descripción, sin web (homepage) y sin
topics**. Eso perjudica a la vez la primera impresión (el apartado *About* en
blanco) y la descubribilidad (topics = cómo la gente encuentra el repo navegando).

**Evidencia.** `gh repo view` → `description: ""`, `homepageUrl: ""`,
`repositoryTopics: null`.

**Solución.** Antes de publicar, rellenar:
- **Descripción:** *"A terminal-first Lua runtime with an extensible coding agent,
  shipped as a single static Go binary."*
- **Topics:** `coding-agent`, `lua`, `golang`, `terminal`, `tui`, `llm`, `mcp`,
  `developer-tools`, `automation`, `ai-agents`.
- **About → website:** enlazar la web de documentación (una vez desplegada, R-02).

> ✅ **RESUELTO (16-jul-2026).** Aplicado con `gh repo edit dbareagimeno/enu`.
> Estado verificado del *About* tras el cambio:
> - **description** = `"A terminal-first Lua runtime with an extensible coding agent, shipped as a single static Go binary."`
> - **homepageUrl** = `https://dbareagimeno.github.io/enu/` (ya apuntaba al slug
>   nuevo `enu`; se reafirma. Reapuntar cuando se decida el dominio real, R-03).
> - **repositoryTopics** = `ai-agents, automation, coding-agent, developer-tools,
>   golang, llm, lua, mcp, terminal, tui`.
> Los tres campos del *About* quedan poblados.

### 🟡 R-09 — El idioma va más allá del README: el CLI y todo el contenido de docs están en español

**Problema.** Traducir el README no basta. La **salida del propio CLI** está en
español (`"uso: nu…"`, `"error de arranque:"`, `"permiso denegado en headless…"`),
así que quien instale y ejecute verá mensajes en español. Y **todo el contenido**
de la web de docs es español con el chrome bilingüe: un visitante que pone la web
en EN y pulsa "documentation" aterriza en un muro de prosa en español (el
"acantilado EN", W-04 de la auditoría de diseño web). El idioma del chrome promete
algo que el contenido no cumple.

**Evidencia.** `main.go` (strings de uso/error en español); `web/src/lib/i18n.ts`
(chrome bilingüe, contenido solo ES); W-04.

**Solución.** Decidir el **alcance mínimo** para el lanzamiento: README + strings
del CLI + landing en inglés, sí; la wiki profunda puede quedarse en español **con
marcador** `[es]` en los enlaces que llevan a contenido español (la solución de
W-04: gestionar la expectativa *antes* del clic, no después). No hace falta
traducirlo todo, pero sí que lo primero que alguien ejecuta y lee no sea un
idioma que el chrome no anunciaba.

> 🧭 **ENCAMINADO (16-jul-2026).** Parcialmente avanzado y con decisión de
> alcance pendiente del propietario. **Ya hecho:** el addendum W-04 tradujo todo
> el contenido de la web a EN bajo `/en/…` (72 páginas). **Pendiente:** los
> strings del CLI en `main.go` siguen en español; traducirlos es una sesión de
> implementación, no una tarea de promoción. Pasos y criterio de hecho en la
> [Directriz de cierre R-09](#directriz-r-09--idioma-del-cli-y-la-documentación).

### 🟡 R-10 — Falta una comparación honesta con otros coding agents

**Problema.** La arquitectura es lo bastante particular como para justificar una
tabla breve; sin ella, el lector no distingue `nu` de "otro CLI para llamar a un
modelo", y el beneficio diferencial se pierde.

**Evidencia.** Ausencia de comparación en README/web; el diferenciador está
descrito en prosa pero no contrastado.

**Solución.** Una tabla corta y honesta (incluyendo lo que *no* hace):

| Característica | nu | Coding agent convencional |
|---|---:|---:|
| Un solo binario estático | Sí | Depende |
| Plugins en Lua | Sí | Generalmente no |
| Agente reemplazable | Sí | Normalmente no |
| Uso como runtime sin agente | Sí | No |
| Modo headless | Sí | Depende |
| MCP | Sí | Depende |
| Windows nativo | No (aún) | Depende |

La honestidad de la última fila da credibilidad al resto.

> ✅ **RESUELTO (16-jul-2026).** El README nuevo incluye la sección **"En qué se
> diferencia (honestamente)"** con una tabla comparativa contra Claude Code,
> Aider y Cursor (base material, extensibilidad, modelo mental, providers,
> headless/CI, **madurez** y ecosistema de plugins). Bajo la tabla, un párrafo
> **"Dónde enu está peor, y hay que decirlo"** admite sin rodeos que es un
> proyecto joven, sin release estable que demuestre el onramp de punta a punta,
> sin ecosistema de plugins de terceros ni integración con editores — la
> honestidad que esta auditoría pedía.

### 🟡 R-11 — Siguen abiertos los dos 🔴 de la auditoría de diseño web (búsqueda y contraste)

**Problema.** La auditoría de diseño web del mismo día dejó dos fallos 🔴 que un
visitante encuentra en el primer minuto: **W-01** — en escritorio la búsqueda no
deja teclear `n` ni `p`, lo que hace **imposible buscar** términos tan comunes en
una API `nu.*` como `plugin`, `spawn`, `print`, `proc`; **W-02** — el token
`--dim` no cumple contraste WCAG AA (~3.1:1), y pinta statusline, metadatos y el
cuerpo de las citas. Son baratos y muy visibles.

**Evidencia.** [`auditoria-web-diseno-2026-07-15.md`](auditoria-web-diseno-2026-07-15.md),
W-01 (`search.ts:407-417`) y W-02 (`tokens.css:16`).

**Solución.** Aplicar las de esa auditoría (reservar navegación a `↑`/`↓` y
`Ctrl-n`/`Ctrl-p`; subir `--dim` por encima de 4.5:1 con un check de contraste en
el build). Se listan aquí solo porque son parte de la "mecánica" que separa el 4
del 6,5.

> ✅ **RESUELTO por referencia (16-jul-2026).** R-11 no era un hallazgo propio,
> solo un puntero a los dos 🔴 de la auditoría de diseño web. El *Addendum de
> resolución (2026-07-15)* de
> [`auditoria-web-diseno-2026-07-15.md`](auditoria-web-diseno-2026-07-15.md) da
> ambos por cerrados en la rama `claude/auditoria-web`:
> - **W-01** — eliminados los `case 'n'/'p'` planos en `search.ts`; la navegación
>   por resultados pasa a `↑`/`↓` y `Ctrl-n`/`Ctrl-p`, de modo que ya se puede
>   teclear `plugin`, `spawn`, `print`, `proc` en la búsqueda.
> - **W-02** — `--dim` subido a AA en los temas que fallaban (nu #63848b 4.63:1,
>   dracula, solarized; gruvbox ya pasaba), con un gate nuevo `check:contraste`
>   en npm y en `docs.yml` que blinda la regresión.
> Al estar ambos cerrados y con gate de regresión, R-11 se cierra por referencia.

### 🔵 R-12 — El proceso interno ocupa demasiado espacio de presentación

**Problema.** Frases como que "las 45 sesiones del plan de implementación están
cerradas" (`README.md:18-23`) demuestran disciplina, pero para un **usuario
nuevo** aportan poco: es vocabulario interno del proyecto, no un beneficio. En
Reddit interesará más qué hace hoy, qué funciona de forma fiable, qué
limitaciones tiene y qué feedback buscas.

**Evidencia.** `README.md:18-23`.

**Solución.** Reubicar el método (diseño por documentos, validación por
pseudocódigo, ADRs) **después** de la propuesta de valor, presentándolo como una
*característica* del proyecto ("así se construye"), no como parte del argumento de
apertura.

> ✅ **RESUELTO (16-jul-2026).** En el README nuevo el proceso interno ya no abre
> la presentación: "las 45 sesiones están cerradas" aparece **dentro** de la
> tabla comparativa (fila de madurez) y de la sección **"Estado del proyecto"**,
> ambas por debajo de la propuesta de valor y de la demostración. El método
> (diseño por documentos, validación por pseudocódigo, sesiones incrementales)
> se presenta como una característica —"pre-1.0 y liderado por el diseño"— y no
> como argumento de apertura, con una petición de feedback explícita al cierre.

### 🔵 R-13 — El camino del desconocido no está verificado de punta a punta

**Problema.** El build y los tests están en verde, pero un **turno real** de
agente (`nu -p` contra un modelo vivo) necesita provider + API key + red y **no
se ha demostrado** en esta auditoría. Es exactamente el camino que hará el de
Reddit: instalar, configurar, pedir algo.

**Evidencia.** Verificados arranque, evaluación Lua headless y build/tests; no una
conversación end-to-end.

**Solución.** Antes de publicar, ensayo en **máquina/contenedor limpio**:
`curl | sh` (de la release **nueva**, R-01) → configurar provider → un turno real
que edite un archivo. Si algo del onramp falla ahí, falla en directo.

> 🧭 **ENCAMINADO (16-jul-2026).** Fuera del alcance de este workflow de cierre:
> la verificación E2E la ejecuta **otro workflow dedicado** (contenedor limpio →
> `curl|sh` de la release nueva de R-01 → configurar provider+key → un turno real
> que edite un archivo). Depende de R-01. Queda anotado aquí como dependencia,
> sin directriz propia por asignarse a ese otro workflow.

### 🔵 R-14 — La navegación de la portada es solo-teclado en escritorio y stubs en móvil

**Problema.** Lo mejor del diseño (prompt tecleable, menú `[i][d][a][g]`) no se
descubre solo: en escritorio esas entradas **no son enlaces clicables** —hay que
pulsar la tecla o teclear el comando—, y en **móvil** `[d]` (docs) y `[a]` (api)
son stubs `href="#"` que no hacen nada. Un visitante que no cae en que debe
teclear se queda fuera.

**Evidencia.** `web/src/pages/index.astro` (menú no clicable en escritorio; `[d]`
/`[a]` como `href="#"` en móvil); reforzado por W-06 (interactividad invisible).

**Solución.** Hacer las entradas clicables además de accionables por teclado, y
cablear `[d]`/`[a]` en móvil a sus rutas reales (ya existen). Añadir el *hint*
discreto de W-06 ("escribe help ↵") para anunciar la interacción.

> 🟡 **PENDIENTE (16-jul-2026).** Fuera del alcance de este workflow de cierre:
> es una corrección de la app web (`web/src/pages/index.astro`), no de la
> promoción. Se deja explícitamente abierto para una sesión que toque `web/`,
> junto con la otra mitad de R-06 (`:::tip`) y R-14 comparte encuadre con W-06.

### 🔵 R-15 — `go.mod` exige un Go muy reciente para compilar desde fuente

**Problema.** `go.mod` pide **Go 1.26.3** (toolchain 1.26.5), una versión muy
nueva; quien clone con un Go más antiguo y quiera compilar desde fuente se topará
con un muro. No afecta al `curl | sh` (que trae binario), pero sí al segmento
—numeroso en Go/Reddit— que "prefiere compilarlo yo".

**Evidencia.** `go.mod` (`go 1.26.3`, `toolchain go1.26.5`).

**Solución.** Aclarar en el README que la vía recomendada es el binario, y que
compilar desde fuente requiere Go ≥ el de `go.mod`. Valorar si el mínimo puede
bajarse sin perder nada.

> 🧭 **ENCAMINADO (16-jul-2026).** No automatizable en este workflow: bajar el
> mínimo de `go.mod` es una decisión consciente de política de versión del
> propietario. La aclaración README (vía binario recomendada vs. compilar desde
> fuente) es texto, pero se deja unida a la decisión. Pasos y criterio de hecho
> en la [Directriz de cierre R-15](#directriz-r-15--política-de-versión-de-go).

### 🔵 R-16 — Encuadre de la publicación: subreddit correcto y petición de feedback explícita

**Problema.** Esto **no** es "otro agente de IA" para una audiencia generalista;
es una tesis de sistemas. Publicarlo en el sitio equivocado o con el marco
equivocado desperdicia el diferencial. Además, un "Show" técnico rinde mucho más
si dice **qué feedback busca**.

**Evidencia.** Naturaleza del proyecto; perfil de repo sin topics (R-08) que
oriente el descubrimiento.

**Solución.** Liderar con la arquitectura en **r/lua, r/golang, r/commandline,
r/programming** (y HN "Show HN"), no en foros de "IA" genérica. En el post: qué
hace hoy, qué funciona fiable, qué limitaciones tiene, qué quieres que prueben y
qué feedback pides. Y marcar de forma visible que es una **versión inicial** con
las partes listas para uso real señaladas.

> 🧭 **ENCAMINADO (16-jul-2026).** No automatizable en este workflow: la
> estrategia y el acto de publicar son del propietario. Un **borrador de post
> "Show HN / r/…" listo para adaptar**, con foro elegido, encuadre de "versión
> inicial" y petición de feedback concreta, queda escrito en la
> [Directriz de cierre R-16](#directriz-r-16--encuadre-de-la-publicación).

---

## Resumen

| Id | Sev | Hallazgo | Fuente | Esfuerzo | Estado (16-jul) |
|----|-----|----------|--------|----------|-----------------|
| R-01 | 🔴 | Release instalable atrasada 102 commits vs el README | repo | Bajo | 🧭 Encaminado (directriz; propietario) |
| R-02 | 🔴 | La web pública muestra el diseño ANTIGUO (rediseño no en `main`) | repo | Bajo | 🧭 Encaminado (directriz; propietario) |
| R-03 | 🔴 | El CTA de instalación apunta al dominio placeholder `nu.sh` | repo | Bajo | 🧭 Encaminado (directriz; propietario) |
| R-04 | ✅ | Colisión con Nushell → **resuelto: `nu` → `enu`** (e/nu) | ext | Hecho | ✅ Resuelto |
| R-05 | 🔴 | No hay demostración visual (GIF/asciinema) | ambas | Medio | 🧭 Encaminado (directriz + guion) |
| R-06 | 🟡 | 404 + `:::tip` crudo en la página estrella de docs | repo | Bajo | 🟡 Parcial (404 cerrados; `:::tip` pendiente, `web/`) |
| R-07 | 🟡 | README como referencia, no como landing (arquitectura antes que beneficio) | ext | Medio | ✅ Resuelto (README landing, 8/10) |
| R-08 | 🟡 | Perfil de GitHub vacío (descripción, web, topics) | ambas | Bajo | ✅ Resuelto (`gh repo edit`) |
| R-09 | 🟡 | El idioma va más allá del README (CLI + docs en español) | repo | Medio | 🧭 Encaminado (docs EN hechas; CLI pendiente) |
| R-10 | 🟡 | Falta comparación honesta con otros coding agents | ext | Bajo | ✅ Resuelto (tabla en README) |
| R-11 | 🟡 | Abiertos los dos 🔴 de la auditoría de diseño web (W-01/W-02) | repo | Bajo | ✅ Resuelto por referencia (W-01/W-02) |
| R-12 | 🔵 | El proceso interno ("45 sesiones") ocupa la presentación | ext | Bajo | ✅ Resuelto (recolocado en README) |
| R-13 | 🔵 | Camino del desconocido sin verificar de punta a punta | repo | Bajo | 🧭 Encaminado (otro workflow E2E) |
| R-14 | 🔵 | Navegación de portada solo-teclado / stubs en móvil | repo | Bajo | 🟡 Pendiente (`web/`, fuera de alcance) |
| R-15 | 🔵 | `go.mod` exige Go 1.26.3 para compilar desde fuente | repo | Bajo | 🧭 Encaminado (directriz; propietario) |
| R-16 | 🔵 | Encuadre de publicación (subreddit + petición de feedback) | ambas | Bajo | 🧭 Encaminado (directriz + borrador post) |

---

## Camino a 8,5/10 (checklist de lanzamiento)

Imprescindibles, en orden de "convierte *no lo enseñes* en *ya es enseñable*":

1. **Cortar una release nueva** justo antes de publicar (R-01) y **probar el
   instalador desde una máquina/contenedor limpio** de punta a punta (R-13).
2. **Fusionar a `main` y desplegar** la web nueva; confirmar que la URL pública ya
   muestra el rediseño (R-02).
3. **Decidir el dominio de instalación** (o usar la URL de GitHub) para que el CTA
   no falle al pegarse (R-03), y cerrar los 404 + `:::tip` de la página estrella
   (R-06) y los dos 🔴 web (R-11).
4. ✅ **Nombre resuelto:** `nu` → `enu` / `e/nu` — *Extensible Native Userland* (R-04 cerrado).
5. **Grabar y colocar un GIF de 20–40 s** al principio del README y de la portada
   (R-05).
6. **Reescribir las dos primeras pantallas del README** alrededor del beneficio,
   con tres motivos y un ejemplo concreto (R-07), y añadir la **tabla comparativa**
   (R-10).
7. **Rellenar el perfil de GitHub**: descripción, topics y web (R-08).
8. **Señalar visiblemente que es una versión inicial** y qué partes están listas
   para uso real (R-16); mover el proceso interno detrás del beneficio (R-12).

Con esto, la calidad técnica que ya existe (8,5) deja de estar enterrada y la
preparación global pasa razonablemente de **6,5 a 8,5/10**.

---

## Conclusión

**Sí es suficientemente presentable para enseñarlo en Reddit**, en concreto en
comunidades de Go, Lua, terminales, herramientas para desarrolladores o agentes
de programación: técnicamente está por encima de muchos proyectos que se
promocionan allí. Pero **no como un gran lanzamiento general todavía**, y no en el
estado exacto de hoy: la web pública muestra la versión vieja, el CTA de
instalación apunta a un dominio muerto y el binario instalable va dos semanas por
detrás del README.

El diagnóstico de fondo es de **presentación, no de sustancia**: la calidad está
enterrada bajo demasiada explicación arquitectónica y muy poca demostración
visual, con un choque de nombre sin resolver por debajo. Ninguno de los dieciséis
hallazgos es trabajo profundo; la mayoría son de esfuerzo bajo. Resuelta la
mecánica de lanzamiento, reorientada la presentación al beneficio, completado el
perfil de GitHub y decidido el nombre `nu`, el proyecto sale a Reddit con la cara
que su ingeniería ya merece.

---

## Directrices de cierre (16-jul-2026)

El *workflow de cierre* de esta auditoría resolvió lo automatizable desde el
repositorio: reescribió el README como *landing* (R-07, R-10, R-12), pobló el
perfil de GitHub (R-08) y cerró R-11 por referencia a W-01/W-02. Lo que sigue
son los hallazgos que **no se pueden cerrar desde este workflow** porque exigen
una decisión o una acción del propietario (cortar releases, comprar dominios,
grabar vídeo, publicar). Cada uno lleva **pasos concretos**, **responsable** y
**criterio de hecho** para que puedan ejecutarse sin releer toda la auditoría.

Nota de alcance: R-06 (mitad `:::tip`) y R-14 son correcciones de la app web
(`web/`), no de la promoción; se dejan a una sesión que toque `web/` y no llevan
directriz aquí. R-13 (verificación E2E) lo ejecuta un workflow dedicado y depende
de R-01.

### Directriz R-01 — Cortar una release alineada con el README

**Contexto.** El instalable (`v0.1.3`, 28-jun) va ~102 commits por detrás de lo
que el README describe; `curl | sh` entrega un binario atrasado.

**Pasos.**
1. Elegir el número de versión (`vX.Y.Z`; al ser pre-1.0, `v0.2.0` es natural
   por el salto de superficie desde `v0.1.3`).
2. Situar el corte sobre el commit que se vaya a publicar (según el modelo de
   ramas, la estable sale de `main`; coordinar con R-02).
3. `git tag vX.Y.Z` + `git push origin vX.Y.Z` y dejar que `release.yml` compile
   y suba binarios (linux/darwin × amd64/arm64) y `checksums.txt`.
4. Anotar en el README la línea "probado en `vX.Y.Z`" para que la coherencia sea
   verificable de un vistazo.

**Responsable.** Propietario (Diego).

**Criterio de hecho.** La release *latest* en GitHub es la que el README
describe, con los cuatro binarios y `checksums.txt`, y el README cita esa
versión.

### Directriz R-02 — Merge a `main` y despliegue del rediseño web

**Contexto.** El rediseño ("la web ES un terminal") vive en `develop`/ramas, no
en `main`; `docs.yml` solo despliega Pages en push a `main` que toque `web/**`,
así que la URL pública sigue mostrando la web de junio.

**Pasos.**
1. Cerrar antes la mitad pendiente de R-06 (`:::tip` sin renderizar) para no
   desplegar la página estrella a medio hacer.
2. Cortar la estable: merge `develop`→`main` (reservada a versiones estables;
   decisión consciente del propietario).
3. Confirmar que `docs.yml` desplegó y que Pages sirve el rediseño en
   `https://dbareagimeno.github.io/enu/`.

**Responsable.** Propietario (decisión de corte estable a `main`).

**Criterio de hecho.** La URL pública muestra el rediseño, no la versión de
junio; los enlaces internos resuelven bajo el base path `/enu/`.

### Directriz R-03 — Dominio de instalación real

**Contexto.** El CTA de la portada copia `curl -fsSL nu.sh/install | sh`, un
placeholder muerto (`web/src/lib/const.ts`, `DOMAIN`). Pegarlo falla.

**Pasos (una de dos vías).**
- **Vía dominio propio:** comprar/configurar `enu.sh` o `enu.dev` (libres, según
  R-04), servir `install.sh` desde ahí, y poner `DOMAIN = 'enu.sh'` en
  `const.ts`. Reapuntar entonces el `homepageUrl` del repo (hoy Pages) si se
  decide un dominio de marca.
- **Vía puente (sin coste, inmediata):** apuntar el CTA a la URL que ya funciona,
  `https://raw.githubusercontent.com/dbareagimeno/enu/main/install.sh` — un solo
  cambio en `const.ts`.

**Responsable.** Propietario (comprar dominio es acción externa; elegir vía es
decisión suya).

**Criterio de hecho.** Pegar el comando del CTA en una shell limpia instala el
binario sin error.

### Directriz R-05 — Demo visual (GIF/asciinema)

**Contexto.** Para un producto de terminal, la demostración visual es *la* pieza
que falta; no hay ningún `.gif`/`.cast` en el árbol. El README ya reserva el
hueco con `<!-- TODO(R-05) -->`.

**Pasos.**
1. Grabar con `asciinema rec demo.cast` (o `vhs` con un `.tape` para salida
   determinista) un flujo de 20–40 s.
2. Convertir a GIF (`agg demo.cast demo.gif`) o incrustar el reproductor.
3. Colocarlo arriba del todo del README (sustituyendo el `TODO`) y en la portada.

**Guion comando a comando (listo para grabar).** El objetivo es enseñar lo que
hoy solo se cuenta: instalación → agente que edita un archivo → plugin Lua →
headless. Un `.tape` de `vhs` da el resultado más limpio y repetible:

```tape
# demo.tape — grábalo con: vhs demo.tape  (produce demo.gif)
Output demo.gif
Set FontSize 18
Set Width 1200
Set Height 700
Set Theme "Dracula"
Set TypingSpeed 60ms

# 1) Un binario, cero runtime: enseña que no hay Node/Python detrás.
Type "enu --version"      Enter    Sleep 1s
Type "which enu"          Enter    Sleep 1s

# 2) Activa el conjunto oficial (viene embebido pero apagado).
Type "enu --default-config"        Enter    Sleep 1500ms

# 3) Un turno headless que EDITA un archivo del repo (el gesto central).
Type "export ANTHROPIC_API_KEY=sk-..."   Enter   Sleep 500ms
Type `enu -p 'Añade una función es_par(n) a utils.lua con un test' --model anthropic/opus`   Enter
Sleep 6s      # el agente pide permiso, escribe el archivo, resume
Type "git diff --stat"    Enter    Sleep 2s

# 4) El agente es tuyo: un plugin Lua cambia el comportamiento.
Type "cat ~/.config/enu/plugins/saludo/init.lua"   Enter   Sleep 2s
Type `enu -p '/saludo'`   Enter    Sleep 2s

# 5) Chat interactivo (abre y cierra rápido para enseñar la UI).
Type "enu"                Enter    Sleep 2s
Type "hola, ¿qué ves en este repo?"   Enter   Sleep 4s
Type "/exit"              Enter    Sleep 1s
```

Si se prefiere `asciinema` puro, ejecutar esos mismos comandos en orden durante
la grabación y recortar los tiempos muertos con `asciinema` + edición del
`.cast`. Mantener la demo por debajo de 40 s: si algún paso tarda, cortarlo.

**Responsable.** Propietario (grabación en máquina real con provider vivo).

**Criterio de hecho.** README y portada abren con la demo; existe el
`.gif`/`.cast` versionado en el repo.

### Directriz R-09 — Idioma del CLI y la documentación

**Contexto.** El chrome de la web es bilingüe y el contenido EN ya existe (72
páginas bajo `/en/…`, W-04), pero los strings del CLI en `main.go` siguen en
español (`"uso: enu…"`, `"error de arranque:"`, `"permiso denegado en headless…"`).
Quien instale y ejecute verá español.

**Pasos.**
1. Decidir el alcance mínimo de lanzamiento: README + strings del CLI + landing
   en inglés (la wiki profunda EN ya está).
2. Traducir a EN los strings de uso/error de `main.go` — es una **sesión de
   implementación** (toca código Go), no una tarea de promoción; sigue el
   protocolo de `docs/implementacion.md` si se abre.
3. Mantener el `[es]` marcador de W-04 en los enlaces que aún lleven a contenido
   solo español, para gestionar la expectativa antes del clic.

**Responsable.** Propietario (decisión de alcance) + sesión de implementación
para `main.go`.

**Criterio de hecho.** Lo primero que alguien ejecuta (uso/errores del CLI) y lee
(landing) está en inglés; los saltos a contenido español están marcados.

### Directriz R-15 — Política de versión de Go

**Contexto.** `go.mod` pide Go 1.26.3 (toolchain 1.26.5); quien compile desde
fuente con un Go más antiguo choca con un muro. No afecta al `curl | sh`.

**Pasos.**
1. Decidir si el mínimo de `go.mod` puede bajarse sin perder nada (revisar qué
   features del toolchain reciente se usan de verdad).
2. Documentar en el README que la vía recomendada es el binario y que compilar
   desde fuente requiere Go ≥ el declarado en `go.mod`.

**Responsable.** Propietario (decisión de política de versión).

**Criterio de hecho.** El README aclara vía binario vs. fuente; el mínimo de
`go.mod` es una decisión consciente y anotada.

### Directriz R-16 — Encuadre de la publicación

**Contexto.** No es "otro agente de IA" para audiencia generalista, sino una
tesis de sistemas. El foro y el marco correctos importan; un "Show" técnico rinde
más si dice qué feedback busca y marca que es una versión inicial.

**Pasos.**
1. Liderar con la arquitectura en **r/lua, r/golang, r/commandline, r/programming**
   y HN **"Show HN"** — no en foros de "IA" genérica.
2. En el post: qué hace hoy, qué es fiable, qué limita, qué probar y qué feedback
   se pide; marcar de forma visible que es una versión inicial.
3. Publicar **después** de R-01/R-02/R-03/R-05 (release fresca, web desplegada,
   CTA vivo, demo), no antes.

**Responsable.** Propietario (estrategia y acto de publicar).

**Criterio de hecho.** Existe un borrador de post con foro elegido, encuadre de
"versión inicial" y petición de feedback concreta.

**Borrador de post (Show HN / r/golang / r/lua — adaptar por foro).**

> **Título (HN):** Show HN: enu – a terminal-first Lua runtime whose killer app
> is a rewritable coding agent
>
> **Título (r/golang):** enu: a single static Go binary that's a Lua runtime —
> the coding agent, chat and providers are all Lua extensions you can replace
>
> **Cuerpo:**
>
> Hi all — I've been building **enu** (*e/nu — Extensible Native Userland*), a
> terminal-first Lua runtime that ships as a **single static Go binary** (no
> Node, no Python, no CGO). What makes it different from other coding agents:
> the core **doesn't know what an agent is**. The agent loop, chat UI, LLM
> providers and MCP support are all **Lua extensions** built on a frozen public
> API — the same API you'd use to replace any of them. If an official feature
> can't be built with the public API, that's considered an API bug.
>
> **What works today:** the kernel is implemented (build + tests green),
> headless mode is first-class (`enu -p '...'` runs one turn to stdout, stable
> exit codes), and providers are declared as data (TOML): Anthropic,
> OpenAI-compatible endpoints and Gemini are embedded.
>
> **What's early, and I'd rather say it up front:** this is **pre-1.0 and
> design-led**. There's no stable release yet that demonstrates the full onramp
> end-to-end against a live model on a clean machine, there's no third-party
> plugin ecosystem, and no editor integration. If you need a proven daily
> driver today, Claude Code / Aider / Cursor are the responsible choice. enu is
> for people drawn to the **idea** — an agent that's entirely yours, in a binary
> that drags no runtime — who want to shape it.
>
> **What I'd love feedback on:** (1) the core thesis — is "the runtime doesn't
> know what an agent is" the right seam? (2) the public API surface — does it
> feel complete enough to build a real agent on? (3) where it creaks when you
> try to write a plugin.
>
> Repo: https://github.com/dbareagimeno/enu · Docs:
> https://dbareagimeno.github.io/enu/
>
> (Note on the name: it's **enu**, not Nushell's `nu` — different binary, no
> PATH collision.)

Ajustar el tono por foro (más técnico-arquitectónico en r/golang y HN; más
enfocado a "todo es Lua" en r/lua) y **no publicar** hasta que la demo (R-05) y
la release (R-01) estén en su sitio.
