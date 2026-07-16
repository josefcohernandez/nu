---
name: release
description: Corta una release estable de enu — bump de versión, merge develop→main, tag vX.Y.Z (dispara release.yml) y reintegración main→develop. Envuelve el runbook docs/release.md y se para pidiendo OK antes de cada paso irreversible (push a main, push del tag). Úsala solo cuando el operador decida publicar una estable; nunca la inicies tú.
---

# Cortar una release estable (/release)

Mecaniza el runbook [docs/release.md](../../../docs/ops/release.md) —que sigue siendo
el **protocolo canónico**; esta skill lo *conduce*, no lo duplica—. Una release es
acción **del operador** y **outward-facing** (crea una GitHub Release pública y
redespliega la web; ADR-013 la enmarca como "el operador crea el tag a mano"). Por
eso la skill **se para pidiendo OK explícito antes de cada paso irreversible** y
**nunca se inicia sola**: solo cuando el operador pide publicar una estable.

## Antes de empezar

- Acuerda con el operador el número `vX.Y.Z` (semver: **patch** = correcciones;
  **minor** = adiciones a la API, que además suben `APILevel`).
- Verifica precondiciones (docs/release.md §Precondiciones): los worktrees de las
  tareas están cerrados y su trabajo integrado en `develop`; CI de `develop` en
  verde (`gh run list --branch develop --limit 5`). Si un worktree sigue vivo con
  trabajo sin integrar, **PÁRATE y avisa**: quedaría fuera de la estable.

## Pasos

Cada bloque sigue su §N de docs/release.md; aquí se marcan las **⛔ puertas**.

1. **Bump de versión** (§1). Toca los 4 sitios en el mismo commit:
   `internal/runtime/enu.go` (`VersionMajor/Minor/Patch`; `APILevel` **solo** si
   hubo adiciones a la API), el test `internal/runtime/bare_screen_test.go`,
   `web/src/lib/const.ts` y el comentario de `web/src/scripts/keyboard.ts`.
   **Verifica el gate en local antes de seguir:**
   ```
   go build ./... && go run . -e 'return string.format("%d.%d.%d", enu.version.major, enu.version.minor, enu.version.patch)'
   ```
   Debe imprimir exactamente `X.Y.Z`. Si no casa, no sigas: el job
   `verificar-version` de `release.yml` abortaría.

2. **De-riesgo de los gates de la web** (§2). Corre en local los gates que **solo**
   viven en `docs.yml` (CI solo hace `check-drift`):
   ```
   cd web && npm ci && npm run check:drift && npm run check:contraste \
     && npm run check:limpieza:fuente && npm run build && npm run check:limpieza
   ```
   Todo verde o no continúes.

3. **Commit + push en `develop`** (§3). Commit `Versión X.Y.Z: …` y
   `git push origin develop`.

4. **⛔ PUERTA — merge a `main`** (§4). Resume al operador **qué se va a publicar**
   (versión, commits que entran, que dispara el redeploy de la web) y **pide OK
   explícito**. Solo entonces:
   ```
   git switch main && git pull --ff-only
   git merge --no-ff develop -m "Corta estable vX.Y.Z: integra develop"
   git push origin main
   ```
   El push directo a `main` (protegida) requiere bypass de owner; si el operador
   prefiere, una PR `develop → main`.

5. **⛔ PUERTA — tag y release** (§5). Crea el tag anotado y **comprueba que apunta
   a un commit con `enu.go` = X.Y.Z** (`git show vX.Y.Z:internal/runtime/enu.go`).
   **Pide OK explícito** antes de empujar el tag: dispara la GitHub Release pública.
   ```
   git tag -a vX.Y.Z -m "enu vX.Y.Z" && git push origin vX.Y.Z
   ```
   Un tag con sufijo (`-rc1`, `-beta`) sale como **pre-release**; uno limpio, normal.

6. **Reintegra `main → develop`** (§6). Alinea el grafo para que GitHub no sugiera
   PR (aviso *«main had recent pushes»*):
   ```
   git switch develop && git merge --ff-only main && git push origin develop
   ```

7. **Verifica** (§Verificación). Vigila los runs `Release` y `Docs` hasta verde
   (`gh run watch <id> --exit-status`). Confirma: `gh release view vX.Y.Z` sin
   draft, sin prerelease (salvo sufijo), **5 assets** y `Latest`; la web responde
   `200` con badge `vX.Y.Z`; `install.sh` coherente con el nombre del artefacto.

8. **Reposo.** Checkout principal en `develop` actualizado; worktrees de tareas
   eliminados.

## Qué NO hace esta skill

- **No decide cuándo cortar** — lo decide el operador.
- **No empuja `main` ni el tag sin OK explícito** (pasos 4 y 5).
- **No toca la API ni los contratos.** Si el bump es *minor* por una adición, esa
  adición ya pasó antes por `/planificar-sesion` → `/sesion` → `/sync-web`; aquí
  solo se publica lo que ya está congelado.
