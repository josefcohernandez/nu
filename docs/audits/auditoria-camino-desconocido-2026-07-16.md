---
title: "Auditoría del «camino del desconocido» — 16 de julio de 2026"
type: "auditoria"
date: "2026-07-16"
status: "cerrada"
---
# Auditoría del «camino del desconocido» — 16 de julio de 2026

Auditoría **end-to-end empírica** del recorrido que hará un desconocido que
descubra `enu` y quiera probarlo: encuentra el proyecto → lee el README → corre
el `curl | sh` (o compila desde fuente) → abre la web → ejecuta su primer
comando de agente. A diferencia de la [auditoría de promoción del
15-jul](auditoria-promocion-reddit-2026-07-15.md) —que juzgó la *superficie*
(README, releases, estado de Pages)—, esta **ejecuta cada tramo del camino** en
esta máquina y contra la infraestructura pública real, y contrasta lo observado
con lo que aquella auditoría dio por hecho o por pendiente.

Es, en concreto, la **verificación empírica de R-13** («el camino del
desconocido no está verificado de punta a punta») que la auditoría del 15-jul
dejó como pendiente 🔵 «no se ha demostrado». Aquí sí se demuestra — y el
resultado es peor de lo que se asumía.

**Metodología.** Todo lo de esta auditoría se reprodujo con comandos concretos
cuya salida se cita: `curl`/`gh` contra la API de GitHub y GitHub Pages, `git
rev-list`/`git diff` sobre ramas y tags, `go build` desde un caché limpio, y —lo
central— **compilación del binario y ejecución real** de `enu --default-config`,
`enu -p '…'`, `enu --version` y `enu` sin TTY en un `HOME` limpio y aislado
(`env -i`). Cada afirmación de estado ("sigue abierto", "matizado") lleva su
comando + salida como prueba.

Los ítems llevan id `C-##`, agrupados por **tramo del camino**: instalador · web
· compilar · primer-uso · README-landing. Severidad: 🔴 alta/crítica (rompe una
función o el primer minuto), 🟡 media (fricción real), 🔵 baja (pulido o
constatación no-negativa).

---

## Veredicto: **el camino del desconocido NO funciona de punta a punta hoy (16-jul)**

Recorrido tramo a tramo, ejecutado, no supuesto:

| Tramo | ¿Funciona hoy? | Punto de fallo |
|---|---|---|
| **Encontrar / instalar** | ⚠️ Parcial | El `curl \| sh` **sí ejecuta** (los redirects de GitHub del rename `nu`→`enu` aguantan, C-02), pero instala **v0.1.3**, 150 commits por detrás de `develop` / 102 por detrás de `main` (C-01). Se instala un binario que **no es** el que el README describe. |
| **Abrir la web** | ❌ Roto | La URL pública real (`/enu/`) sirve el **Starlight antiguo** (C-03) y, encima, **todo** su CSS y su navegación apuntan a `/nu/` y dan **404** (C-04): la portada carga sin estilos y ningún enlace navega. |
| **Compilar desde fuente** | ✅ Funciona | `go build ./...` limpio en ~8-9 s *si* se tiene Go ≥ el de `go.mod` (1.26.x). En esta máquina, sin fricción (C-06). |
| **Primer comando del agente** | ❌ Roto en silencio | `enu -p '…'` sin API key válida sale con **exit 0, stdout y stderr vacíos**, sin rastro del error ni en pantalla ni en el JSONL (C-07). El desconocido cree que "no pasó nada". |
| **Diagnosticar** | ❌ Sin asideros | `enu --version` falla con `flag provided but not defined` (C-08); no hay ninguna pista de por qué el turno no respondió. |

**La tesis en una frase:** de los cinco tramos, **dos están rotos de forma que
un desconocido percibe** (web y primer turno del agente), **uno sirve el
artefacto equivocado** (instalador) y **uno no ofrece asidero de diagnóstico**
cuando algo falla. El único tramo verde de punta a punta es *compilar desde
fuente* — precisamente el que menos gente recorre. **No es momento de
promocionar:** el recorrido feliz que hará quien venga de Reddit choca con un
muro silencioso justo en el gesto que se le pide («instala y pídele algo»).

Lo grave no es la cantidad de fallos —la auditoría del 15-jul ya los anticipaba
casi todos— sino que **el más caro (C-07, el turno del agente) es exactamente el
que aquella auditoría no llegó a ejecutar** y dejó como 🔵 «sin verificar». Al
ejecutarlo, resulta ser un fallo silencioso de primer minuto.

---

## Hallazgos por tramo

### Tramo: instalador

#### 🔴 C-01 — El `curl | sh` sigue resolviendo v0.1.3 (28-jun) mientras `develop` va 150 commits por delante — R-01 no solo sigue abierto: ha empeorado

**Problema.** El instalador (`install.sh`, `latest_stable_tag()`) descarga la
última release estable, y hoy sigue siendo **v0.1.3**, del 28-jun-2026. La
auditoría del 15-jul denunciaba (R-01) que `main` iba 102 commits por delante de
ese tag; hoy, un día después, **no se ha cortado ninguna release nueva** y la
distancia sólo crece: `develop` va **150 commits** por delante y `main` **102**.
Quien pega el `curl | sh` del README instala un binario de **18 días atrás**, no
el "kernel construido / 45 sesiones cerradas" que el propio README proclama.

**Evidencia.**

```
$ gh release list --repo dbareagimeno/enu
v0.1.3   Latest   v0.1.3   2026-06-28T01:31:38Z
v0.1.2 · v0.1.1 · v0.1.0   (todas prerelease:false, ninguna posterior)

$ git rev-list --count v0.1.3..origin/develop → 150
$ git rev-list --count v0.1.3..origin/main    → 102
```

Simulando el `awk` exacto de `latest_stable_tag()` (install.sh:90-100) contra el
JSON real de la API de releases, resuelve `v0.1.3`: el script instalaría hoy la
misma versión atrasada que R-01 ya denunciaba. `install.sh:22` fija
`REPO="dbareagimeno/nu"`, que por el redirect de GitHub (ver C-02) sigue
golpeando el repo renombrado.

**Solución.** La de R-01, ahora más urgente: **cortar una release nueva justo
antes de publicar** y marcar en el README la versión probada. Mientras no exista,
`curl | sh` es una promesa incumplida por dos semanas y media de trabajo.

#### 🔵 C-02 — El rename `nu → enu` NO rompió el instalador: `curl -fsSL` sigue los redirects 301 de GitHub (constatación, no defecto)

**Problema.** Tras el rename del repo a `dbareagimeno/enu` (R-04, resuelto el
15-jul), tanto `install.sh:22` (`REPO="dbareagimeno/nu"`) como
`web/src/lib/const.ts:12` (`GITHUB_URL = '…/dbareagimeno/nu'`) quedaron con el
**nombre viejo**. Cabía temer que el instalador público se hubiera roto. **No es
el caso:** GitHub mantiene redirects 301/302 permanentes de `nu` → `enu` en
`raw.githubusercontent.com`, `api.github.com` y los assets de release, y
`install.sh` usa `curl -fsSL` (con `-L`) en `fetch()` y `fetch_to()`, que **sí
sigue** esos redirects.

**Evidencia.**

```
$ curl -sI https://raw.githubusercontent.com/dbareagimeno/nu/main/install.sh → HTTP/2 200
$ curl -s  https://api.github.com/repos/dbareagimeno/nu/releases  (SIN -L)     → 301 "Moved Permanently"
$ curl -sL …/repos/dbareagimeno/nu/releases                      (CON -L)     → 200; html_url apunta ya a …/dbareagimeno/enu/…
$ curl -sIL …/releases/download/v0.1.3/nu-v0.1.3-darwin-arm64.tar.gz → 2 redirects, HTTP 200, EFFECTIVE_URL = release-assets.githubusercontent.com (blob firmado)
```

El end-to-end de descarga del binario funciona hoy.

**Consecuencia (riesgo latente, no fallo).** El instalador vive de un redirect
que GitHub *podría* dejar de honrar si alguien crea un repo nuevo llamado
`dbareagimeno/nu`. Es una dependencia frágil **no documentada como tal**.
Convendría actualizar `REPO` en `install.sh:22` y `GITHUB_URL`/`RELEASES_URL` en
`const.ts:12-13` al nombre nuevo, para no depender del redirect. Severidad baja
porque hoy no rompe nada.

---

### Tramo: web pública

#### 🔴 C-03 — La web pública sigue sirviendo el diseño ANTIGUO (Starlight), no el rediseño «la web es un terminal» — R-02 confirmado, sigue abierto

**Problema.** El rediseño completo de la web («la web ES un terminal») vive en
`develop`, no en `main`, y `docs.yml` sólo despliega a Pages en push a `main`. La
URL pública, por tanto, sigue sirviendo el **Starlight anterior al rediseño**,
exactamente como describía R-02 el 15-jul.

**Evidencia.**

```
$ curl -s https://dbareagimeno.github.io/enu/ | grep generator
  <meta name="generator" content="Starlight v0.36.3">   (+ chrome sl-*: sl-flex, sl-link-button, sl-badge…)

$ git log origin/main -1 --format=%h,%ci -- web/       → f457d8e, 2026-06-26
$ git diff origin/main origin/develop --stat -- web/    → 115 ficheros, 22779(+) / 1501(−)
$ wc -l  (origin/develop:web/src/pages/index.astro)     → 313    (no existe en origin/main)
$ grep 'branches' .github/workflows/docs.yml (en main)  → branches: [main]   (develop nunca despliega)
```

El rediseño (index.astro de 313 líneas, keyboard.ts, pager.ts, search.ts,
tokens.css…) está íntegro en `develop` y **sin mergear a `main`**.

**Solución.** La de R-02: fusionar a `main` lo que esté terminado (coordinado con
C-04, R-06) y no promocionar ninguna URL hasta confirmar que la pública ya
muestra el rediseño.

#### 🔴 C-04 — La URL pública real es `/enu/`, pero TODO enlace y asset del HTML servido apunta a `/nu/` y da 404: la web desplegada está rota de forma sistémica (efecto colateral del rename, no anticipado por R-02)

**Problema.** Éste es un hallazgo **nuevo**, más grave que R-02 por sí solo. Tras
el rename `nu → enu` (R-04, 15-jul), la URL pública pasó a ser
`https://dbareagimeno.github.io/**enu**/`. Pero el sitio desplegado se construyó
con `base: '/nu/'` (`web/astro.config.mjs:17`) y **no se ha redeployado** con el
`base` corregido. Resultado: el HTML que sirve `/enu/` trae `<link
rel="canonical" href=".../nu/">` y **todos** sus `href`/assets con prefijo
`/nu/…`, que bajo el dominio real dan **404**. La hoja de estilos ni siquiera
carga desde la portada, y cualquier clic en la navegación cae en un 404.

**Evidencia.**

```
$ curl -sI https://dbareagimeno.github.io/nu/   → HTTP/2 404 ("Site not found · GitHub Pages")
$ curl -sI https://dbareagimeno.github.io/enu/  → HTTP/2 200
$ gh api repos/dbareagimeno/enu/pages | grep html_url → "https://dbareagimeno.github.io/enu/"

  HTML servido en /enu/: 13 de 13 href/src internos usan prefijo /nu/, 0 usan /enu/
  (CSS, favicon, sitemap, enlaces de navegación a empezando/, referencia/…)

$ curl -sI https://dbareagimeno.github.io/nu/_astro/index.BK102pm3.css   → 404
$ curl -sI https://dbareagimeno.github.io/enu/_astro/index.BK102pm3.css  → 200
$ curl -sI https://dbareagimeno.github.io/nu/empezando/que-es-nu/        → 404
$ curl -sI https://dbareagimeno.github.io/enu/empezando/que-es-nu/       → 200
```

Causa raíz confirmada en el repo: `web/astro.config.mjs:17` → `base: '/nu/'`, sin
actualizar tras el rename.

**Solución.** Actualizar `base` (y `site`/canonical) a `/enu/` y redeployar. Es
condición previa a C-03: aunque se mergeara el rediseño a `main`, si el `base`
sigue en `/nu/` la web pública seguiría con CSS y navegación en 404.

#### 🔵 C-05 — El `:::tip` crudo (R-06) NO se reproduce en la versión desplegada hoy (matiz de trazabilidad)

**Problema / matiz.** R-06 reportaba directivas `:::tip` saliendo como texto
literal en la página estrella. Verificado hoy sobre la web **en producción**: no
aparece ningún `:::tip` crudo — porque la producción actual es el **Starlight
antiguo**, que sí renderiza esas cajas. R-06 describe un defecto del **rediseño**
(`remark-directive` no configurado en `web/astro.config.mjs` de `develop`), que
**todavía no está desplegado**.

**Evidencia.**

```
$ curl -s https://dbareagimeno.github.io/enu/empezando/que-es-nu/ | grep -o ':::tip[^<]*'  → (sin coincidencias)
  (la página en vivo contiene la cadena 'starlight'; astro.config.mjs de develop no tiene remark-directive)
```

**Consecuencia.** R-06 sigue siendo un defecto **latente** del rediseño: se
manifestará en cuanto C-03/C-04 se resuelvan y el rediseño llegue a producción.
No hay que darlo por cerrado; hay que arreglarlo **antes** de desplegar el
rediseño, no después. La página estrella en producción, eso sí, sufre el problema
más grave de C-04 (enlaces/CSS `/nu/` en 404), que no es específico de ella sino
de todo el sitio.

---

### Tramo: compilar desde fuente

#### 🔵 C-06 — `go.mod` exige Go 1.26.x y compila limpio en ~8 s cuando el requisito se cumple — R-15 técnicamente vigente, mitigación documental ya aplicada

**Problema / constatación.** R-15 señalaba que `go.mod` exige un Go muy reciente
(`go 1.26.3` / `toolchain go1.26.5`), un muro para quien compile desde fuente con
un Go más antiguo. Sigue siendo cierto en abstracto. En esta máquina, con la
toolchain exacta que exige, **la compilación no tiene ninguna fricción**: limpia,
sin warnings, ~8-9 s desde caché frío.

**Evidencia.**

```
$ cat go.mod  → go 1.26.3 / toolchain go1.26.5
$ go version  → go1.26.5 darwin/arm64
$ go clean -cache && time go build ./...  → sin errores, 8-9 s wall (≈400-447% cpu: paralelo real)
$ time go build ./...  (caché caliente)   → ~0.5-1.5 s, también limpio
```

La mitigación que R-15 pedía (**documentar** el requisito) ya está aplicada:
`README.md:79` dice literalmente «Compilar desde el código (necesitas Go ≥ la
versión de `go.mod`)». No hay error de compilación, sólo el requisito de versión;
por eso severidad baja. Este es el único tramo del camino que funciona de punta a
punta hoy.

---

### Tramo: primer uso (el primer comando del agente)

#### 🔴 C-07 — `enu -p '<prompt>'` sin API key válida falla en TOTAL SILENCIO: exit 0, stdout y stderr vacíos, sin rastro en el JSONL — R-13 ejecutado y encontrado roto (peor de lo que se asumía)

**Problema.** Éste es el hallazgo central de la auditoría, y la razón de su
veredicto. R-13 dejó el camino end-to-end como 🔵 «no se ha demostrado». Al
demostrarlo hoy, **está roto**: un desconocido que instala, no configura bien la
key y ejecuta `enu -p '...'` recibe **exit 0 con stdout y stderr vacíos** — ni un
carácter de error, ni en pantalla ni en el JSONL de la sesión. Cree que "no pasó
nada" o que su prompt se perdió, sin ninguna pista de qué falló.

**Evidencia.** (build limpio → `enu --default-config` en `HOME` aislado → dos
ejecuciones)

```
$ go build -o .../enu .                                                  → exit 0
$ env -i HOME=$W/home PATH=$PATH enu --default-config                     → genera nu.toml/agent.toml/providers.toml (anthropic/opus, ANTHROPIC_API_KEY)

$ env -i HOME=$W/home PATH=$PATH enu -p "hola"                            → EXIT 0, stdout 0 bytes, stderr 0 bytes, ~0.3 s
$ env -i HOME=… ANTHROPIC_API_KEY="sk-ant-clave-invalida" enu -p "hola"   → EXIT 0, stdout 0 bytes, stderr 0 bytes  (idéntico)

  # la red y Anthropic SÍ responden — el silencio no es de red:
$ curl -sS -o /dev/null -w "HTTP:%{http_code}" https://api.anthropic.com/v1/messages … → HTTP:401 en ~0.2 s

  # el JSONL de la sesión sólo tiene el mensaje de usuario, sin rastro del error ni del turno fallido:
$ cat ~/.local/share/nu/sessions/…/*.jsonl → {"t":"message", … usuario …}   (y nada más)
```

**Causa en código (verificada, no sólo la salida).**
- `internal/runtime/embedded/agent/lua/agent/init.lua:1004-1020` —
  `Session:_turn_loop()` envuelve el cuerpo del turno en `pcall`; si falla (401
  `EPROVIDER`, `ENET`…) **emite el evento `agent:error`** (comentario del propio
  código: «para que la UI lo pinte … en vez de morir EN SILENCIO») en lugar de
  relanzar. `Session:send()` devuelve sin texto utilizable.
- `main.go:374-444` (`agentDriver`, el driver Lua del modo headless `nu -p`) sólo
  se suscribe a `agent:permission.denied`, **nunca a `agent:error`**. El único
  suscriptor de `agent:error` en todo el repo es
  `chat/lua/chat/init.lua:528` — la UI interactiva, no el camino headless.
- Con `state.denied` en `false` y `text == ""`, el driver retorna `("", "OK")`,
  que en `main.go` produce **exit 0 sin imprimir nada**. No hay flag
  `--verbose`/`--debug` ni variable de log que ofrezca una vía de escape.

La ironía es exacta: el evento existe para que el error **no** muera en silencio,
pero en el único camino que un desconocido recorre por defecto (`nu -p`) nadie lo
escucha, y muere en silencio.

**Solución.** El driver headless de `main.go` debe **suscribirse a
`agent:error`** y volcarlo a stderr con exit ≠ 0 (y, coherentemente, registrarlo
en el JSONL). Sin esto, el primer minuto del desconocido termina en un fallo
mudo — el peor tipo de fallo para una primera impresión.

#### 🟡 C-08 — El binario no soporta ningún flag de versión (`--version`, `-version`, `-v` → «flag provided but not defined»)

**Problema.** Un desconocido que hace troubleshooting prueba `--version` lo
primero. En `enu` falla con un error de flag no reconocido y exit 2, en vez de
imprimir una versión — justo cuando más necesita un asidero (p. ej. tras el
silencio de C-07).

**Evidencia.**

```
$ enu --version  → stderr: "flag provided but not defined: -version" + usage; exit 2
$ enu -version   → idéntico
$ enu -v         → "flag provided but not defined: -v"; exit 2
```

`main.go:96-104` registra sólo `-e`, `-p`, `-continue/-c`, `-auto-permissions`,
`-model`, `-default-config`; no hay flag de versión.

**Matiz de trazabilidad.** No es un blind spot total: **ADR-013**
(`docs/decisions/adr/README.md:~610-618`) ya documenta la ausencia como decisión consciente y
**pendiente** («un flag `--version` sería un nice-to-have de producto … pendiente
del dueño del proyecto»; la fuente de verdad observable hoy es `enu -e 'return
nu.version.api'`). El flag sigue sin existir —la evidencia lo confirma— pero el
gap está registrado, no ignorado. Debe etiquetarse «relacionado con ADR-013 /
R-13», no «nuevo».

**Solución.** Cablear `--version` a `nu.version` (packaging de S45, ya
contemplado en ADR-013). Coste bajo, alto valor para el primer minuto de
diagnóstico.

#### 🔵 C-09 — El mensaje de `--default-config` sugiere `enu` (chat) como onramp, pero sin TTY el binario imprime un uso genérico — correcto, pero potencialmente confuso en pipe/CI

**Problema / constatación.** `enu --default-config` termina sugiriendo «…luego
ejecuta `nu` (chat) o `nu -p '<prompt>'` (headless)». Si el desconocido prueba
`enu` en un pipe / CI / script (sin TTY), el binario imprime el uso genérico y
sale con exit 2 — comportamiento **documentado e intencional** (`main.go:152-157`:
«Sin TTY … no hay superficie: se imprime el uso»; también `README.md:140-141`),
no un bug. Pero el mensaje de éxito de `--default-config` no aclara que `chat`
requiere terminal interactiva, así que puede reproducir la misma sensación de "no
pasó nada".

**Evidencia.**

```
$ enu --default-config          → stdout incluye "…luego ejecuta `nu` (chat) o `nu -p '<prompt>'` (headless)"
$ enu </dev/null   (sin TTY)     → stderr: "uso: nu [--default-config] | [-e '<lua>'] | [-p '<prompt>' …]"; exit 2
```

**Solución (opcional, bajo).** Que el mensaje de `--default-config` matice que
`chat` necesita TTY, o que oriente al desconocido en pipe hacia `-p` de forma
explícita. Mitigado en parte porque el propio uso subsiguiente ya muestra `-p`.

---

### Tramo: README como landing

Los cuatro hallazgos siguientes reverifican R-07 y R-12. El README **no ha
cambiado desde el 15-jul** (último commit que lo tocó: `239b358`, 14-jul,
anterior a la propia auditoría de promoción), así que todos siguen literalmente
vigentes.

#### 🟡 C-10 — El README arranca con la tesis arquitectónica, no con el problema/beneficio para el lector (R-07, sigue abierto)

**Problema.** El H1 es `# nu`, seguido de badges, y el primer párrafo de contenido
(líneas 6-16) abre con «Un runtime de Lua orientado a terminal cuya killer app es
un coding harness… El core no sabe lo que es un agente… (modelo Emacs/Textadept,
no Neovim)». No hay ningún punto del documento que responda «¿qué problema
resuelve?» o «¿por qué usar esto en vez de otro coding agent?» **antes** de la
arquitectura.

**Evidencia.**

```
$ grep -n -i -E "por qué|ventaja|beneficio|en vez de|en lugar de|comparad" README.md → 0 coincidencias (en 469 líneas)
```

Texto de las líneas 6-16 idéntico al citado por R-07.

#### 🟡 C-11 — El README mide 469 líneas / ~2.819 palabras (~14 min a 200 wpm), muy por encima de lo que un desconocido lee en 60 s (R-07, sigue abierto)

**Evidencia.**

```
$ wc -l README.md            → 469
$ (conteo python3)           → 2819 palabras ≈ 14,1 min a 200 wpm
```

El índice (línea 27) lista ~17 entradas de nivel `##`/`###` (instalación, 4
subsecciones de «Uso», 7 de «Configuración», extensiones, plugins…) **antes** de
cualquier argumento de «por qué». Último commit sobre README.md: `239b358`,
14-jul (sin cambios desde la auditoría de promoción).

#### 🔵 C-12 — El primer ejemplo ejecutable del agente (`nu -p …`) no aparece hasta la línea 119, tras la sección de instalación completa (R-07, sigue abierto)

**Evidencia.**

```
$ grep -n 'nu -p' README.md → primera aparición en línea 119 (dentro de "## Inicio rápido", que empieza en 92)
```

Precedido por `## Instalación` completa (líneas 52-90: `curl | sh`, manual con
`sha256sum`, y compilación con `go build`). Un lector que escanea "qué hace esto
por mí" pasa por tres métodos de instalación antes de ver un comando de agente
funcionando. (Atenuante menor: el índice ofrece un ancla directa a
`#inicio-rápido`.)

#### 🔵 C-13 — El vocabulario de proceso interno («45 sesiones del plan de implementación») sigue en la apertura, antes de la propuesta de valor (R-12, sigue abierto)

**Evidencia.**

```
$ grep -n '45 sesiones' README.md → línea 18: "**Estado: kernel construido.** Las 45 sesiones del [plan de implementación] están cerradas…"
```

La frase citada por R-12 el 15-jul es **idéntica carácter por carácter**, en la
misma posición (líneas 18-23, tercer párrafo, antes de instalación o uso). Ocupa
espacio de "primera pantalla" con vocabulario de proceso del proyecto en lugar de
con qué hace el runtime por un usuario nuevo.

#### 🔵 C-14 — No hay tabla comparativa ni mención de otros coding agents en el README (R-10, sigue abierto)

**Evidencia.**

```
$ grep -i 'comparad' README.md → 0 coincidencias
  (lectura completa de 469 líneas: sin mención a Claude Code / Aider / Cursor, ni tabla de comparación)
```

Confirma la queja de R-10 (falta comparación honesta con otros coding agents); no
se ha añadido nada desde el 15-jul.

---

## Reverificación de los R-## de la auditoría de promoción (para el workflow de cierre)

Estado **verificado empíricamente hoy (16-jul)** de cada hallazgo de la
[auditoría de promoción del 15-jul](auditoria-promocion-reddit-2026-07-15.md) que
esta auditoría tocó. Esta tabla es el entregable para el workflow de cierre de
promoción: cada fila lleva el comando + salida que prueba el estado.

| R-## | Estado real hoy | C-## | Prueba (comando → salida) |
|---|---|---|---|
| **R-01** | 🔴 **Sigue abierto — EMPEORADO** | C-01 | `gh release list --repo dbareagimeno/enu` → `v0.1.3 Latest 2026-06-28`; `git rev-list --count v0.1.3..origin/develop` → **150** (era 102 en `main` el 15-jul; hoy `main`=102, `develop`=150). Ninguna release nueva cortada. |
| **R-02** | 🔴 **Sigue abierto** | C-03 | `curl -s .../enu/ \| grep generator` → `Starlight v0.36.3`; `git log origin/main -1 -- web/` → `f457d8e 2026-06-26`; `git diff origin/main origin/develop --stat -- web/` → 115 ficheros / 22779(+). Rediseño sólo en `develop`; `docs.yml` despliega sólo desde `main`. |
| **R-02 (agravado)** | 🔴 **Nuevo: web rota sistémicamente** | C-04 | `curl -sI .../nu/` → 404; `.../enu/` → 200; pero HTML de `/enu/` trae 13/13 assets con prefijo `/nu/` → CSS y navegación en 404. Causa: `web/astro.config.mjs:17` `base:'/nu/'` sin actualizar tras el rename. |
| **R-03** | 🔴 **Sigue abierto (en `develop`); moot en producción** | — | `web/src/lib/const.ts:6` → `DOMAIN='nu.sh'` (placeholder), `INSTALL_CMD` lo usa; `i18n.ts:94,157` repiten `curl -fsSL nu.sh/install \| sh`. El CTA `nu.sh` sigue sin dominio real en el rediseño de `develop`; en la web **desplegada** (Starlight antiguo) ese CTA ni existe, así que hoy no es lo que rompe la portada pública — la rompe C-04. Se manifestará al desplegar el rediseño. |
| **R-06** | 🟡 **Matizado — latente, no visible hoy** | C-05 | `curl -s .../enu/empezando/que-es-nu/ \| grep ':::tip'` → sin coincidencias. Es defecto del **rediseño** (`remark-directive` ausente en `astro.config.mjs` de `develop`), no desplegado aún. Arreglar antes de desplegar el rediseño. |
| **R-07** | 🟡 **Sigue abierto (sin cambios)** | C-10, C-11, C-12 | `grep -iE "por qué\|ventaja\|beneficio\|en vez de\|comparad" README.md` → 0; `wc -l README.md` → 469 (~2819 palabras); `grep -n 'nu -p' README.md` → línea 119. Último commit del README: `239b358`, 14-jul (anterior a la auditoría). |
| **R-12** | 🔵 **Sigue abierto (sin cambios)** | C-13 | `grep -n '45 sesiones' README.md` → línea 18, idéntica carácter por carácter a la citada el 15-jul. |
| **R-13** | 🔴 **EJECUTADO → roto en silencio (peor que 🔵 «sin verificar»)** | C-07, C-08, C-09 | `env -i HOME=… enu -p "hola"` → exit 0, stdout/stderr 0 bytes; `curl … api.anthropic.com` → HTTP:401 (la red sí responde); JSONL sólo con el mensaje de usuario. El driver headless (`main.go:374-444`) no se suscribe a `agent:error`. |
| **R-15** | 🔵 **Vigente en abstracto — mitigación documental ya aplicada** | C-06 | `cat go.mod` → `go 1.26.3 / toolchain go1.26.5`; `go version` → `go1.26.5`; `go clean -cache && go build ./...` → limpio, ~8-9 s. `README.md:79` ya documenta «necesitas Go ≥ go.mod», que era la solución que R-15 pedía. |

**R-04** (rename `nu → enu`), aunque marcado ✅ el 15-jul, tiene **cola pendiente**
detectada aquí: dejó referencias al nombre viejo que hoy funcionan por redirect
(C-02: `install.sh:22`, `const.ts:12-13`) o directamente rompen la web (C-04:
`astro.config.mjs:17`). El rename está decidido, pero su propagación a la
configuración de build **no está completa**.

---

## Bloqueantes reales del lanzamiento

De todo lo ejecutado, lo que **impide** enseñar el camino a un desconocido hoy
(en orden de "sin esto, el primer minuto falla en directo"):

1. **C-07 / R-13 — El primer turno del agente muere en silencio.** `enu -p '…'`
   sin key válida sale con exit 0 y salida vacía. Es el gesto exacto que hará
   quien venga de Reddit, y termina sin ninguna pista. **Bloqueante nº 1:** el
   driver headless debe escuchar `agent:error` y reportarlo con exit ≠ 0.

2. **C-04 (+ C-03) / R-02 — La web pública está rota, no sólo vieja.** La URL real
   (`/enu/`) sirve Starlight antiguo **y**, encima, con CSS y navegación en 404
   por el `base:'/nu/'` sin actualizar. Corregir `base` a `/enu/`, mergear el
   rediseño a `main` y redeployar — en ese orden — antes de enlazar ninguna URL.

3. **C-01 / R-01 — El instalador sirve un binario de 18 días atrás.** Cortar una
   release nueva justo antes de publicar y marcar la versión probada en el README.

4. **R-03 (`const.ts:6` `nu.sh`) — El CTA de instalación del rediseño apunta a un
   dominio placeholder.** No bloquea hoy (el rediseño no está desplegado), pero
   **se vuelve bloqueante en el mismo momento** en que se resuelva C-04 y el
   rediseño llegue a producción. Fijar el dominio (o usar la URL de
   `raw.githubusercontent.com`) en el mismo lote que el redeploy.

Fuera de la ruta crítica pero con alto retorno para el primer minuto: **C-08**
(flag `--version`, hoy el único asidero de diagnóstico y hoy roto) y la
reorientación del README al beneficio (**C-10..C-14 / R-07, R-12, R-10**).

**Conclusión.** El camino del desconocido **no funciona de punta a punta hoy**.
La causa no es falta de sustancia —el kernel compila limpio y el tramo de
compilación desde fuente es sólido— sino tres roturas en los tramos que un
desconocido sí percibe: instalador desactualizado, web pública rota por partida
doble, y un primer turno de agente que falla mudo. Las tres son de esfuerzo
bajo-medio y ninguna es trabajo profundo, pero **las tres tienen que caer antes
de promocionar**: hoy, el recorrido feliz choca con un muro silencioso justo
donde se le pide al visitante que dé el primer paso.
