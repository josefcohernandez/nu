---
title: "Renombrado total del proyecto y de la API: `nu` → `enu`"
type: "adr"
id: "ADR-022"
status: "aceptada"
date: "2026-07-16"
---
# ADR-022 · Renombrado total del proyecto y de la API: `nu` → `enu`

**Estado:** Aceptada · 2026-07-16 (**reemplaza** la decisión 1 de
[ADR-009](#adr-009--convenciones-de-la-api-namespace-global-async-por-corrutinas-errores-estructurados);
no toca sus decisiones 2 y 3)

**Contexto.** El nombre `nu` colisiona en el `PATH` con el binario de
**Nushell** y, en menor medida, con el Lisp histórico `Nu`
([docs/audits/analisis-nombres-2026-07-15.md](../docs/audits/analisis-nombres-2026-07-15.md),
que resuelve R-04 de la auditoría de promoción). El estudio de renombrado
recorrió el cementerio de descartes, un ranking de 77 nombres vírgenes y el
análisis profundo de cinco finalistas; el propietario decidió el 16 de julio
de 2026 el nombre **`enu`** (wordmark **`e/nu`**, backronym *Extensible
Native Userland* — detalle en el §10 de ese análisis). El proyecto sigue en
fase de diseño (pre-1.0, sin usuarios que migrar), así que no hay
instalaciones que preservar ni razón para pagar el coste permanente de un
shim de compatibilidad.

**Decisión.** Renombrado total, de una vez, en bloque:

1. **El namespace global de la API pasa de `nu` a `enu`**: todo módulo y
   función de la superficie sagrada cambia de `nu.<modulo>` a
   `enu.<modulo>` (`enu.fs`, `enu.task`, `enu.ui`, `enu.http`, `enu.events`,
   `enu.version` — incluida `enu.version.api` —, `enu.plugin`, `enu.worker`,
   `enu.search`, `enu.config`, `enu.ws`, `enu.log`, `enu.re`, `enu.sys`,
   `enu.text`, `enu.proc`, `enu.json`, `enu.yaml`, `enu.eval`, `enu.has`,
   etc.). Esto **reemplaza la decisión 1 de ADR-009**; sus decisiones 2
   (async por corrutinas) y 3 (errores estructurados) no cambian.
2. **El binario, el nombre de producto y el module path pasan a `enu`**: el
   binario ejecutable se llama `enu` (ya sin colisión de `PATH`), y el
   `module` de Go se renombra en consecuencia.
3. **Las rutas en disco siguen al namespace**: ficheros de configuración
   `nu.toml` → `enu.toml`, contexto de proyecto `nu.md` → `enu.md`,
   directorios de proyecto `.nu/` → `.enu/`, referencias git de `mesh`
   `refs/nu/` → `refs/enu/`, y los directorios estándar de usuario
   `~/.config/nu` → `~/.config/enu`, `~/.local/share/nu` →
   `~/.local/share/enu` (o equivalentes por plataforma).
4. **Sin shim de compatibilidad.** No se acuña un alias `nu.*` que reenvíe a
   `enu.*`, ni un symlink `nu` → `enu` del binario, ni lectura de fallback
   de `nu.toml`/`.nu/`. Antes de v1 no hay compromiso de compatibilidad que
   proteger, y un shim solo aplazaría el coste (y la superficie sagrada) sin
   necesidad.

**Consecuencias.**

- Esto **rompe deliberadamente** la regla "la API del core crece solo por
  adición" (idea central 4 de [CLAUDE.md](../CLAUDE.md)) — pero de forma
  consciente y de una sola vez, **antes** de congelar v1 (el punto en el que
  esa regla empieza a regir en serio). No es un precedente para romper
  firmas después de congelar.
- Todo el pseudocódigo de [pseudocodigo.md](pseudocodigo.md) y los contratos
  (`api.md`, `agente.md`, `providers.md`, `sesiones.md`, `chat.md`,
  `guia-plugins.md`, `malla.md`) migran en bloque al namespace `enu.*` y a
  las rutas en disco `enu`/`.enu`; no queda un periodo mixto documentado.
- Los ADR previos que citan `nu.*` como namespace vigente (ADR-009 y
  cualquier otro que lo dé por hecho en su prosa) **no se reescriben**: son
  el registro histórico de lo que se decidió *entonces*: ADR-009 queda
  marcado como reemplazado en su decisión 1 por este ADR, sin tocar su
  cuerpo. La lectura correcta de cualquier ADR anterior que mencione
  `nu.algo` es "sustitúyase por `enu.algo`" desde el 16-jul-2026 en
  adelante.
- `docs/audits/**` conserva `nu` en su prosa donde discute la disyuntiva
  `nu` vs. `enu` (es, precisamente, el análisis que motiva esta decisión):
  esa carpeta es informe fechado y cerrado, no contrato vivo, y no se toca.
- Fuera de `docs/`, el renombrado del binario, el module path de Go y las
  rutas reales en disco es trabajo de una sesión de implementación
  ([docs/implementacion.md](implementacion.md)), no de este ADR: aquí se
  registra la decisión y se actualizan los contratos; el código la sigue
  después, protocolo habitual "el contrato lidera, el código sigue".
