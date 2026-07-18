---
title: "El binario estrena subcomandos de gestión: `enu init`, `enu doctor`, `enu update`, `enu uninstall` (refina ADR-015/ADR-017; su disparador ya sonó)"
type: "adr"
id: "ADR-026"
status: "aceptada"
date: "2026-07-18"
---
# ADR-026 · El binario estrena subcomandos de gestión: `init`, `doctor`, `update`, `uninstall`

**Estado:** Aceptada · 2026-07-18 (**refina** [ADR-015](adr-015-conjunto-oficial-de-producto.md)
y [ADR-017](adr-017-el-onramp-deja-config.md); ejecuta el disparador de
reapertura que ambos dejaron armado; motivada por
[ADR-025](adr-025-reposicionamiento-motor-de-harnesses.md) pieza 3, Fase 1, y
la [auditoría externa 2026-07-18](../../audits/auditoria-externa-concepto-2026-07-18.md))

**Contexto.** ADR-015 rechazó expresamente estrenar subcomandos «por una sola
necesidad» y dejó el disparador: «una tercera o cuarta acción de configuración
del binario». ADR-017 dejó el suyo gemelo: «reconsiderar un flujo de
configuración guiado (ligado al disparador `nu config` de ADR-015)». La Fase 1
de ADR-025 trae de golpe **cuatro** acciones de gestión — un flujo de
configuración guiado (`init`), un diagnóstico (`doctor`), la actualización y la
desinstalación del binario — y la Fase 2 traerá la familia `enu plugin …`
(P4→ADR-025). El disparador ha sonado con margen: seguir por la vía de flags
(`--default-config`, `--doctor`, `--update`…) convertiría la CLI en un cajón de
sastre sin gramática. A la vez, la frontera de S45/ADR-015 sigue vigente: la
superficie CLI vive en `main.go`, orquesta extensiones por la API pública y
**nada de esto toca la API sagrada** (`enu.version.api` intacto).

**Decisión.** Cinco piezas, ninguna en `enu.*`:

1. **Se abre la superficie de subcomandos, restringida a GESTIÓN.** El binario
   acepta `enu <subcomando>` para acciones de gestión del propio binario y su
   configuración: `init`, `doctor`, `update`, `uninstall` hoy; la familia
   `plugin` queda **reservada** para la Fase 2 (P4→ADR-025). Regla de frontera:
   un subcomando gestiona el binario/config; la **funcionalidad de producto**
   (agente, chat, evaluación) sigue en flags (`-e`, `-p`, `--continue`) y
   extensiones — no habrá `enu run`/`enu chat`. Los flags existentes no
   cambian; `--default-config` **se conserva** tal cual (ADR-015): es el camino
   no interactivo/scriptable y CI/Docker depende de él.

2. **`enu init` — el flujo de configuración guiado** (ejecuta el disparador de
   ADR-017). *Estrechado en v1 por [G61](../../findings/g61-el-wizard-de-init-ofrece-providers-sin-plantilla.md):
   el asistente ofrece **solo `anthropic`** (el único provider con plantilla,
   ADR-017); `openai-compat`/`gemini`/`ollama` se difieren como P44 hasta que se
   especifiquen sus plantillas. El resto del flujo es idéntico.* Con TTY
   interactivo: asistente breve — provider
   (`anthropic` | `openai-compat` | `gemini` | `ollama`) → clave (siempre por
   variable de entorno: detecta si la convencional está exportada y la
   referencia como `api_key_env`; **jamás escribe la clave en un fichero**,
   providers.md §1) → modelo por defecto (propone el del provider elegido; para
   `anthropic`, la plantilla de ADR-017) → activar el conjunto oficial de
   producto (sí/no; el conjunto es el `officialProductSet` de ADR-015, única
   fuente de verdad). Escribe los mismos tres ficheros que ADR-017
   (`enu.toml`, `agent.toml`, `providers.toml`) con las mismas primitivas y la
   misma semántica **por fichero**: atómico, escribe **los que faltan** y
   **respeta y lista** los que ya existen — nunca sobrescribe ninguno, en
   ningún modo; `config.dir()` se crea si no existe (como ya hace ADR-017).
   Cierra con el mensaje honesto de ADR-017 (qué se creó, qué se respetó, qué
   variable exportar, cómo arrancar); con la config ya completa es un **no-op
   honesto** (lo dice y sale con 0). Códigos de salida: **0** éxito o no-op,
   **1** error de escritura, **2** uso inválido. **Sin TTY** (o con `--yes`):
   sin preguntas — equivale **exactamente** a `--default-config` persistente
   (plantillas Anthropic de ADR-017, misma semántica por fichero: la
   equivalencia es posible precisamente porque ambos modos escriben-lo-que-
   falta) y lo dice. `init` no usa red (ADR-010: activar sale del binario
   embebido).

3. **`enu doctor` — diagnóstico de solo lectura.** Ejecuta una batería de
   comprobaciones **sin efectos y sin red por defecto**: versión/arquitectura
   del binario; existencia y parseo de `config.dir()` y sus TOML; plugins
   activados y resolución de sus dependencias (`requires`); modelo por defecto
   resoluble contra `providers.toml`; variable de la clave **presente o
   ausente, sin imprimir jamás su valor** (ni en `--json`); permisos de
   `sessions/` (el `0600` de G57); TTY y capacidades del terminal;
   herramientas externas que las tools usan. Salida humana legible y `--json`
   con **esquema versionado** (`doctor.v1`), cuya espec vive en
   [docs/ops/doctor.md](../../ops/doctor.md) (fichero propio, redactado con
   este ADR; dentro de `v1` el esquema solo crece por adición y un campo
   nunca cambia de significado). Regla anti-duplicación (la lección del
   `officialProductSet` de ADR-015): los checks **de producto** (modelo
   resoluble, dependencias de plugins, herramientas de tools) consultan a las
   extensiones o a su fuente única por la API pública — nunca re-implementan
   esa semántica en Go; la lista de herramientas externas la declara cada
   extensión, no una tabla cableada en el binario. Códigos de salida
   coherentes con la convención de S45: **0** todo verde, **1** al menos una
   comprobación falló, **2** uso inválido. Cada fallo emite un remedio
   accionable (qué fichero/variable tocar). Una comprobación de
   alcanzabilidad del provider (red) existe solo como opt-in explícito
   (`--net`), apagada por defecto.

4. **`enu update` y `enu uninstall` — el ciclo de vida del binario.**
   `update`: consulta la última release estable, descarga el artefacto de la
   plataforma, **verifica su checksum contra `checksums.txt` de la release**
   (obligatorio: sin verificación no hay reemplazo), y reemplaza el binario en
   uso de forma atómica (escribir al lado + rename), respetando dónde está
   instalado; `ENU_VERSION` pineado también vale aquí (`enu update` a una
   versión concreta). Si el destino **no es escribible sin privilegios**
   (instalación vía gestor de paquetes ajeno, o `/usr/local/bin` con sudo
   previo), `update` **aborta con remedio** («tu enu lo gestiona X; actualiza
   por ahí») — nunca eleva privilegios, la misma regla que el instalador.
   `uninstall`: elimina el binario e imprime qué **no** borra; `--purge`
   borra además **exclusivamente `config.dir()`** (`~/.config/enu`), pidiendo
   confirmación explícita. `data_dir()` (`~/.local/share/enu`:
   sesiones/transcripts, plugins instalados, log) no se toca **nunca** desde
   `uninstall`, ni con `--purge`: son datos del usuario, y su borrado es una
   decisión que se toma con `rm`, no con un flag. Ambos son los únicos
   subcomandos con red (update) y efectos fuera de `config.dir()` — por eso
   viven en subcomandos explícitos y no en flags que se activan por accidente.

5. **El instalador se endurece a juego** (la espec operativa completa vive en
   [docs/ops/release.md](../../ops/release.md) §«Instalador»): verificación de
   checksum **obligatoria**, `ENU_VERSION` para pinear versión,
   `ENU_INSTALL_DIR` (default `~/.local/bin`, **sin sudo**), aviso claro si el
   destino no está en `PATH`, y releases inmutables como precondición del
   pineado. `install.sh` y `enu update` comparten la misma disciplina
   (artefacto + checksum de la release firmada por CI, ADR-013).

**Razonamiento.**
- **Por qué ahora sí subcomandos.** ADR-015 fijó el umbral («tercera o cuarta
  acción») y se ha cruzado con cuatro acciones nuevas más una familia entera
  (`plugin`) ya decidida. La alternativa — cinco flags más — pierde la
  gramática verbo/objeto justo cuando el binario pasa a ser producto
  (ADR-025): `enu doctor --json` se lee; `--doctor-json` no.
- **Por qué la regla de frontera gestión/producto.** Es la misma frontera de
  ADR-003/S45 proyectada a la CLI: el binario delgado orquesta; el producto
  vive en extensiones. Sin la regla, los subcomandos serían la puerta de
  atrás para privilegiar al agente oficial (`enu chat`) que ADR-015 evitó.
- **Por qué `init` no reemplaza a `--default-config`.** Son públicos
  distintos: `init` es el humano en su terminal (Fase 1 de ADR-025: primera
  experiencia); `--default-config` es el script/Dockerfile (ADR-015, modos
  persistente y efímero). Compartir las plantillas y primitivas de ADR-017
  garantiza que no diverjan: `init` sin TTY **es** `--default-config`.
- **Por qué `doctor` sin red por defecto.** La promesa de valor es «funciona
  en una Debian limpia y air-gapped» (ADR-025: mercados corporativo y
  aislado); un diagnóstico que llama fuera por defecto mentiría exactamente
  en el entorno donde más se necesita. Y sin efectos: un doctor que arregla
  es un doctor que rompe (la reparación es de `init` o del usuario, guiado
  por el remedio impreso).
- **Por qué el checksum es obligatorio y no opt-in.** `curl | sh` ya pide un
  acto de fe; que además baje sin verificar sería negligencia. La release ya
  genera `checksums.txt` (ADR-013/release.yml): usarlo cuesta cero
  infraestructura nueva.
- **Por qué no toca la API sagrada.** Todo vive en `main.go` + `install.sh` +
  Lua de extensiones vía API pública (mismo argumento de ADR-015 §«vive en el
  binario»): el core sigue sin saber qué es un agente ni qué es un provider.

**Consecuencias.**
- ADR-015 y ADR-017 quedan **refinados, no reemplazados**: el conjunto
  oficial, los dos modos del onramp y la degradación con gracia del chat
  siguen siendo suyos; este ADR añade la capa guiada y el ciclo de vida.
- La sección «Instalador» de `docs/ops/release.md` pasa a ser espec operativa
  (escrita junto a este ADR); `release.yml` no cambia (ya produce checksums).
- Las sesiones de construcción entran al plan por `/planificar-sesion` en la
  Fase 9 (S49 `init`, S50 `doctor`, S51 instalador+`update`/`uninstall`), con
  el contrato de sesión **general** (son código Go/shell con tests, no
  trabajo editorial: el DoD propio de S46-S48 no les aplica).
- La familia `plugin` (Fase 2) hereda esta gramática sin nuevo debate de
  superficie: el debate quedó cerrado aquí.
- **Disparador de reapertura:** si alguien propone un subcomando de
  *producto* (`enu chat`, `enu run`, `enu agent …`), este ADR lo veta; esa
  discusión exigiría un ADR nuevo que argumente contra la regla de frontera
  de la pieza 1.
