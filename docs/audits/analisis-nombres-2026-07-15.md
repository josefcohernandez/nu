# Análisis de renombrado del proyecto — 15 de julio de 2026

Estudio para resolver **R-04** de la
[auditoría de promoción](auditoria-promocion-reddit-2026-07-15.md): el nombre
`nu` colisiona con **Nushell** (mismo binario `nu`) y, además, con un **Lisp
llamado Nu** (sobre el runtime de Objective-C). Este documento recoge el porqué,
los criterios, la metodología de búsqueda y verificación, y **todos los
resultados**: el cementerio de descartes, los 77 nombres vírgenes rankeados, el
análisis profundo de los cinco finalistas y el estado de la decisión.

Es un documento vivo: la búsqueda sigue abierta (§8).

---

## 1. El problema

`nu` es un nombre excelente —cortísimo, polisémico, con guiño a la letra griega
ν (como π)— pero **el binario `nu` ya es de Nushell**, distribuido para Linux,
macOS y Windows. Consecuencias: choque de `PATH`, búsqueda sepultada ("nu"
devuelve Nushell), confusión en titulares ("I built nu"), fricción futura con
Homebrew y gestores de paquetes. Además existe un Lisp histórico llamado **Nu**.

Dos reglas duras que salen de esto:

1. **El binario no puede seguir siendo `nu`** (choque de `PATH`), ni algo
   casi-idéntico a Nushell.
2. **El nombre de proyecto conviene que sea raro**, o la búsqueda te la comen
   otros.

## 2. Criterios y restricciones

**Lo que hacía BUENO a `nu`** (y que buscamos replicar) — es el orden de ranking:

1. **Cortísimo** (2–3 letras ideal).
2. **Varios sentidos a la vez** (ν = letra griega + símbolo de frecuencia +
   símbolo del neutrino + "nuevo"/"desnudo" en romance).
3. **Referencia culta/matemática con gracia** (como π).
4. **Memorable** y que se teclee de un tirón.

**Restricciones duras (filtros eliminatorios):**

- **Sin colisión**: ni lenguaje/herramienta/CLI/runtime, ni empresa/producto/
  marca de software, ni repo de GitHub popular con ese nombre exacto; libre (o
  casi) en npm/PyPI/crates/Homebrew; dominio plausible.
- **Sin malsonancia ni connotación incómoda** en ES/EN/FR/IT. *(Añadido tras
  descartar la veta "desnudo" — `nuda`, `naken` — por asociación incómoda.)*
- No estar en el **nicho saturado de "agentes IA"** (donde ya cayeron varios).

## 3. Metodología

Tres fases, con un **protocolo de verificación** común por candidato:

1. `WebSearch: "<nombre>" software OR cli OR runtime OR programming language OR company OR app`.
2. `gh search repos --match name <nombre> --sort stars` (colisión de repo exacto).
3. `gh api users/<nombre>` (handle libre / ocupado).
4. Registros: `npm` (`registry.npmjs.org`), `PyPI`, `crates.io`, `Homebrew`
   (`formulae.brew.sh`) → 404 = libre, 200 = ocupado.
5. Sondeo de dominios `.dev/.sh/.io/.com`.

**Fase A — verificación manual.** Rondas de WebSearch + `gh` sobre candidatos
generados a mano (letras griegas, raíces, nu-blends, metáforas).

**Fase B — caza por 5 subagentes (Sonnet).** Uno por tipo de idea: nu-blends ·
inventadas · raíces romances · raíces nórdicas/germánicas · metáfora-tesis. Cada
uno generó y verificó su lote.

**Fase C — pipeline masivo (workflow, Sonnet).** 14 generadores por área →
evaluadores que puntúan por los criterios de `nu` → ~30 verificadores. Cifras:
**433 generados → 429 evaluados → 240 verificados → 77 vírgenes confirmados.**

## 4. Cementerio — descartes notables (y el patrón)

El patrón es demoledor: **casi toda palabra corta y evocadora está tomada, varias
en el nicho exacto de agentes IA.** Muestra representativa:

| Nombre | Motivo del descarte |
|---|---|
| `loam` | Algoritmo SLAM/lidar LOAM (robótica), enorme |
| `kern` | Un lenguaje de sistemas + `kernel/cli` (agentes) |
| `kiln` | Kiln-AI (evals/RAG/agents/MCP) + build system + MCP 3D |
| `tau` | CLI `tau` de Taubyte (WASM + git + MCP agentes) |
| `pith` | Un hook para **Claude Code** (mismo nicho) + PyPI |
| `nux` | Sepultado por Nuxt |
| `numen` / `nuon` | CLI de control por voz activa / plataforma BYOC con tap Homebrew |
| `novum` | `novum-lang` (compilador LLVM) |
| `anima` | **Cinco** frameworks de agentes IA (el nombre más saturado del nicho) |
| `atto` | Dos lenguajes de juguete |
| `mote` | `soveran/mote` (motor de plantillas Ruby, 221★) + moteus |
| `nuce` / `nuvel` | Empresa en nuce.team / **Nuvel Inc.** (software) |
| `nuq` | **nuqs** (10,7k★, 2,69M descargas/semana) a una letra |
| `nuit` | **Nuitka** (15k★, compilador Python) + enjambre de empresas "nuit" |
| `nuun` | **Marca registrada Nuun** (hidratación, Nestlé) |
| `nuda` / `naken` | Descartados por **connotación** (= "desnudo") |
| `ictus` / `seno` | Connotación: = derrame cerebral / = pecho (ES) |

Descartes adicionales de los subagentes (colisión real confirmada), a modo de
rastro: `glix`, `dryft`, `worq`, `zorn`, `vosh`, `flisk`, `fenn`, `nucli`,
`nutram`, `telar`, `ordito`, `grano`, `yema`, `lumbre`, `brote`, `yesca`,
`trama`, `gema`, `nodo`, `quilla`, `fragua`, `crisol`, `fulcro`, `cepa`, `ascua`,
`brasa`, `hilo`, `graine`, `cerne`, `radice`, `nucleo` (Helix), `trenza`, `pira`,
`glod`, `gnist`, `smed`, `vev`, `korn`, `ved`, `kil` (Kilo Code), `holm`,
`grund`, `eld`, `drev`, `vrid`.

## 5. Los 77 vírgenes rankeados

Ordenados de mejor a peor por los criterios de §2 (corto ×3 · polisemia ×2 ·
guiño matemático/culto ×2 · memorable · brandable). ✔ = sin caveat; ⚠ = ruido/
riesgo (ver nota).

| # | Nombre | Significado / guiño | Nota |
|---|---|---|---|
| 1 | `hz` | hercio, frecuencia (= símbolo de nu) | ⚠ ruido "hertz" |
| 2 | `jot` | ápice mínimo, del griego iota | ⚠ proyectos menores |
| 3 | `qrk` | "quark" sin vocales, partícula (∥ nu=neutrino) | ⚠ ruido Quark (no exacto) |
| 4 | `jn` | julio/newton + "join" | ⚠ 2 letras, squatting |
| 5 | `tild` | tilde `~` (tecla home) | ⚠ se lee "tilde" |
| 6 | `vav` | letra fenicia→F, palíndromo, "gancho/conexión" | ✔ limpio |
| 7 | `ryd` | constante de Rydberg + "ride" | ⚠ pypi/fintech menor |
| 8 | `vau` | letra vav semítica, "gancho/conexión" | ⚠ mini-lang Vau |
| 9 | `ictus` | latín "golpe/pulsación", el tick | ⚠ = derrame (connotación) |
| 10 | `khor` | *khora*, espacio-matriz (≈core) | ⚠ handle ocupado |
| 11 | `trit` | dígito ternario (guiño a bit) | ⚠ ruido "Triton" |
| 12 | `blit` | bit-block transfer (gráficos) | ⚠ empresa Blit |
| 13 | `macr` | macron ¯ + "macro" | ⚠ verificar handle |
| 14 | `cubo` | cubo / potencia cúbica | ⚠ apps "Cubo" |
| 15 | `nuez` | español "nuez": núcleo en cáscara (conserva nu) | ⚠ eco EN "nut" |
| 16 | `seno` | función seno + "interior/centro" | ⚠ = pecho (connotación) |
| 17 | `epsi` | épsilon ε, infinitesimal | ⚠ acrónimo EPSI |
| 18 | `phot` | unidad de iluminancia, raíz de fotón | ⚠ ≈"photo" |
| 19 | `tot` | gotita mínima + "total" | ⚠ palabra común |
| 20 | `raiz` | raíz de ecuación / root | ⚠ handle ocupado |
| 21 | `cor` | latín "corazón"/núcleo | ⚠ ≈core |
| 22 | `syzy` | syzygy, alineación de 3 cuerpos | ⚠ ≈syzygy tool |
| 23 | `kenon` | griego "el vacío" (atomismo) | ⚠ apellido Kennon |
| 24 | `nudo` | donde se atan hilos + nudo (velocidad) | ⚠ ruido nudoku |
| 25 | `sigl` | sigil, identificador/símbolo | ⚠ ≈sigil |
| 26 | `asper` | aspiración griega + "áspero" | ⚠ ≈Aspera |
| 27 | `fyz` | "fizz"/"physics" | ⚠ empresas menores |
| 28 | `vlk` | volt + Vulcan | ⚠ ≈VLK licencias |
| 29 | `ars` | latín "arte/técnica" | ⚠ acrónimo saturado |
| 30 | `cedi` | cedilla ç + moneda de Ghana | ⚠ handle |
| 31 | `soli` | sol + latín "solus" (único) | ⚠ ≈Sol/Solana |
| 32 | `orbe` | órbita / esfera-mundo | ⚠ ruido "Orb" |
| 33 | `cabo` | extremo, "atar cabos" | ⚠ ≈Cobra |
| 34 | `eje` | eje / axis | ⚠ editor EJE |
| 35 | `nuu` | nu duplicada, vibración | ⚠ handle/pypi |
| 36 | `nuk` | nu+kernel abreviado | ⚠ ≈Nuklear/nuke |
| 37 | `nuit` | francés "noche" (terminal oscura) | ⚠ ≈Nuitka |
| 38 | `nuq` | nu+quantum/query | ⚠ ≈nuqs |
| 39 | `hule` | griego *hyle* "sustrato" + "goma" | ⚠ handle/≈Huly |
| 40 | `nou` | catalán "nuevo"/neerl. "ahora" | ⚠ handle |
| 41 | `sye` | sánscrito *sunya* (cero) | ⚠ empresa SYE |
| 42 | `arje` | griego *arché* "principio/origen" | ⚠ handle |
| 43 | `omeg` | omega ω, frecuencia angular | ⚠ ≈Omega |
| 44 | `cota` | cota / bound | ⚠ handle |
| 45 | `nuy` | nu + ypsilon (dos letras griegas) | ⚠ ≈nu |
| 46 | `nuun` | doble nu / nūn árabe | ⚠ marca Nuun |
| 47 | `nuo` | nu+o (origen/cero) | ⚠ ≈NuoDB |
| 48 | `etho` | griego *ethos* "carácter" | ⚠ ruido Ethos |
| 49 | `arkh` | arché condensado | ⚠ ≈Ark |
| 50 | `tit` | tittle, el punto de la i | ⚠ vulgar (EN) |
| 51 | `ogam` | ogham, alfabeto celta de trazos | ⚠ handle |
| 52 | `pei` | letra semítica origen de pi | ⚠ ≈PEI Software |
| 53 | `trama` | malla/red + trama narrativa | ⚠ marca Trama |
| 54 | `cuna` | origen/cradle (bootstrap) | ⚠ ≈Cua |
| 55 | `morfe` | griego *morphe* "forma" (par de hyle) | ⚠ ≈Morphe/Morpho |
| 56 | `physe` | griego *physis* "naturaleza/crecer" | ✔ limpio |
| 57 | `physi` | *physis* (raíz) | ⚠ ≈PhysiApp |
| 58 | `trad` | nórdico *tråd* (hilo)/thread | ⚠ abreviatura común |
| 59 | `dib` | "dibs" (pedir primero) | ⚠ microempresa |
| 60 | `gset` | G-set (teoría de grupos) + "get set" | ⚠ minoritarios |
| 61 | `decay` | desintegración de partículas | ⚠ repo 374★ |
| 62 | `esse` | sueco *ässja* (fragua) + "essence" | ⚠ handle |
| 63 | `kelo` | evoca "kelvin" | ⚠ ≈Kilo |
| 64 | `whit` | "not a whit" (pizca) + "wit" | ⚠ handle |
| 65 | `tref` | tréma (diéresis) | ⚠ org |
| 66 | `junt` | juncture/ligadura (unión) | ⚠ ≈Junie |
| 67 | `acut` | acento agudo ´ | ⚠ org |
| 68 | `stro` | stroke (trazo mínimo) | ⚠ mucho prefijo-ruido |
| 69 | `sley` | peine del telar que ordena los hilos | ✔ limpio |
| 70 | `brai` | "braid" (trenza), grupo de trenzas | ⚠ ≈Braiins |
| 71 | `nihi` | nihil "nada", kernel vacío | ✔ limpio |
| 72 | `prin` | *principium* (origen) + "print" | ✔ limpio |
| 73 | `nout` | aguas primordiales egipcias (Nun) + "nought" | ✔ limpio |
| 74 | `het` | letra semítica origen de eta | ⚠ handle |
| 75 | `tejo` | tejer código + juego del tejo | ⚠ repo 78★ |
| 76 | `nucl` | raíz de núcleo/nuclear | ⚠ ruido nucl* |
| 77 | `zrk` | onomatopeya chispa/corte | ✔ esencialmente virgen |

**Sin caveat alguno:** `vav`, `physe`, `sley`, `nihi`, `prin`, `nout`, `zrk`.

## 6. Análisis profundo de los 5 finalistas

Verificación dura (404 = libre, 200 = ocupado; dominios: 000 = sin web, señal
débil de libre).

| | GitHub repos exactos | Handle | npm | PyPI | crates | Homebrew | .dev / .sh |
|---|---|---|---|---|---|---|---|
| **naken** | ninguno de peso (`naken_asm` 339★ es otro) | **LIBRE** | **404** | **404** | **404** | **404** | **000 / 000** |
| **noyau** | tiny (`noyaujs/noyau` 3★) | ocupado | 200 | 200 | 404 | **404** | **000 / 000** |
| **nuq** | **`47ng/nuqs` 10 675★** | ocupado | 200 | 404 | 404 | 404 | 000 / 000 |
| **nuit** | **`Nuitka/Nuitka` 14 988★** | ocupado | 200 | 200 | 200 | 404 | 000 / 000 |
| **nuun** | tiny (`nuun-io/kernel` 8★) | **LIBRE** | 404 | 404 | 404 | 404 | 200 / 000 |

- **`naken`** (sueco "desnudo") — **la más limpia de todas**: handle + los cuatro
  registros + `.dev`/`.sh` libres, sin empresa/marca. Único ruido: `naken_asm`
  (ensamblador, 339★). **Descartada por el usuario por connotación** (= desnudo).
- **`noyau`** (francés "el kernel", *le noyau Linux*) — el **mejor significado**;
  Homebrew y `.dev`/`.sh` libres, sin empresa de peso. Peros: npm+PyPI ocupados,
  handle ocupado, y **pronunciación difícil** para anglófonos ("nwa-yó"). Único
  superviviente de los cinco tras el filtro anti-malsonancia.
- **`nuq`** — **descartada**: `nuqs` (gestor de estado de URL para React, 10,7k★,
  **2,69M descargas/semana**, nuqs.dev) está a una letra. Cambiar la colisión de
  Nushell por otra peor.
- **`nuit`** — **descartada**: sepultada bajo **Nuitka** (15k★, compilador Python,
  mismo espacio de dev tooling) + enjambre de empresas "nuit"; ocupada en
  npm+PyPI+crates.
- **`nuun`** — **descartada**: registros y handle libres, **pero es marca
  registrada de Nestlé** (hidratación) y `nuun.dev` está ocupado; se pronuncia
  "noon". (Curiosidad: `kubouch/nuun` es de un mantenedor de Nushell.)

**El patrón clave:** las tres que conservan "nu" (nuq, nuit, nuun) son justo las
que colisionan; las dos rupturas limpias (naken, noyau) son las libres.
**Conservar "nu" te mantiene en un barrio fonético saturado.**

## 7. Estado tras el filtro anti-malsonancia

Al añadir "sin connotación incómoda", cae la veta "desnudo" (`nuda`, `naken`) y
también `ictus` (= derrame) y `seno` (= pecho, ES). La veta limpia y digna para
esta tesis resulta ser **"el núcleo vacío / el origen / el sustrato que tú
llenas"** — que además encaja con "el core no sabe lo que es un agente".

**Shortlist limpia (pasa todos los filtros):**

- Por significado: **`noyau`** (kernel, FR) · **`kenon`** (griego "el vacío") ·
  **`arje`** (griego *arché*, el origen) · **`physe`** (*physis*, lo que crece) ·
  **`hule`** (*hyle*, el sustrato primordial).
- Espíritu `nu` (ultracorto, letra/partícula): **`qrk`** (quark, ∥ nu=neutrino) ·
  **`vav`** (letra fenicia, palíndromo, "gancho").
- Conserva "nu": **`nuez`** (la nuez = núcleo en su cáscara).
- Más veta limpia: `sley`, `nihi`, `prin`, `nout`, `zrk`.

**Recomendación actual:** **`kenon`** — dice la tesis exacta ("el core está
vacío, tú lo llenas"), culto sin ser críptico, limpio en todos los registros y
**sin una sola connotación incómoda**. Alternativas fuertes: `noyau` (mejor
significado, peor pronunciación) y `vav`/`qrk` (más cerca del espíritu de `nu`).

**Decisión: ABIERTA.**

## 8. Próximos pasos

1. **Nueva ronda de generación filtrada** explícitamente por «corto + polisémico
   + guiño matemático/culto + **CERO connotación** en ES/EN/FR/IT», explorando
   vetas aún poco tocadas (notación musical, física, teoría de números, craft).
   *(En marcha.)*
2. **Lock autoritativo** del/los finalista(s): WHOIS real de `.dev`/`.sh` en el
   registrador (el `curl` solo da señal débil), reserva del **org de GitHub** y
   viabilidad del **tap de Homebrew**.
3. Cuando se decida, cerrar **R-04** en la auditoría de promoción y registrar el
   cambio de nombre (binario, repo, dominios, README, strings del CLI).
