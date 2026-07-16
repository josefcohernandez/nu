# Cortar una release estable de `enu`

Runbook **operativo** del operador: los *pasos* para publicar una versión
estable. El *porqué* (CI, cross-compile, versionado por constantes) vive en
[ADR-013](adr.md#adr-013--integración-continua-y-publicación-de-releases), que
deliberadamente "captura las decisiones, no los *steps* del YAML" — este
documento es esos steps. Es DevOps del operador: **no** toca la API sagrada
([api.md](api.md)) ni los contratos de extensión. No se publica en la web (Capa 2,
ver [docs/README.md](README.md)).

Recordatorio del modelo de ramas ([CLAUDE.md](../CLAUDE.md) §«Convenciones de
Git»): `develop` es integración y rama por defecto; `main` queda para **estables**
y solo recibe merges desde `develop`. Las versiones *no estables* salen de
`develop` sin tag.

## Precondiciones

- Los worktrees de las tareas en vuelo están cerrados y su trabajo, integrado en
  `develop`.
- CI de `develop` en verde.
- Número `vX.Y.Z` decidido (semver): **patch** para correcciones; **minor** para
  adiciones a la API — que además suben `APILevel` ([api.md](api.md) §17).

## Pasos

### 1. Bump de versión — fuente de verdad + espejos

La versión vive en las constantes de `internal/runtime/enu.go`
(`VersionMajor/Minor/Patch`), expuestas como `enu.version` (estrategia A,
ADR-013). Cambiar el número exige tocar, **en el mismo commit**:

- `internal/runtime/enu.go` — `VersionMajor/Minor/Patch`. `APILevel` solo sube si
  hubo adiciones a la API; en un patch se queda igual.
- `internal/runtime/bare_screen_test.go` — el test fija la cadena
  `"enu X.Y.Z · API N"`; si no lo actualizas, el rojo del test bloquea el build.
- `web/src/lib/const.ts` — `VERSION` (badge de la web) y el comentario del REPL.
- `web/src/scripts/keyboard.ts` — el comentario que cita la versión (cosmético,
  por coherencia).

El gate de `release.yml` (job `verificar-version`) **ejecuta el runtime** y aborta
si no casa con el tag. Reprodúcelo en local antes de nada:

```
go build ./... && go run . -e 'return string.format("%d.%d.%d", enu.version.major, enu.version.minor, enu.version.patch)'
```

### 2. De-riesgo de los gates de la web

`check:contraste` y `check:limpieza` **solo corren en `docs.yml`** (CI solo hace
`check-drift`), así que nunca se validan sobre el contenido actual hasta el deploy.
Córrelos en local para que el deploy post-merge no falle a medias:

```
cd web && npm ci && npm run check:drift && npm run check:contraste \
  && npm run check:limpieza:fuente && npm run build && npm run check:limpieza
```

### 3. Commit del bump en `develop` y push

```
git add internal/runtime/enu.go internal/runtime/bare_screen_test.go \
        web/src/lib/const.ts web/src/scripts/keyboard.ts
git commit -m "Versión X.Y.Z: bump de VersionPatch y badge de la web"
git push origin develop
```

### 4. Merge `develop → main` y push (redespliega la web)

`main` solo recibe merges desde `develop`. El push a `main` que toca `web/**`
**dispara `docs.yml`** → redeploy de la web con el badge nuevo.

```
git switch main && git pull --ff-only
git merge --no-ff develop -m "Corta estable vX.Y.Z: integra develop"
git push origin main
```

`main` está protegida (exige PR): el push directo lo hace el owner por *bypass*;
alternativa limpia, una PR `develop → main`.

### 5. Tag `vX.Y.Z` en `main` y push → dispara Release

El tag debe apuntar a un commit donde `enu.go` ya tenga la versión nueva (el merge
del paso 4 lo garantiza).

```
git tag -a vX.Y.Z -m "enu vX.Y.Z"
git push origin vX.Y.Z
```

Un tag con sufijo (`-rc1`, `-beta`…) se publica como **pre-release** (no "Latest");
uno limpio `X.Y.Z` es release normal. `release.yml` cross-compila las 4 plataformas
objetivo, genera `checksums.txt` y crea la GitHub Release con notas autogeneradas.

### 6. Reintegrar `main → develop` — cierra el grafo

Los `--no-ff` del paso 4 (y el propio commit de tag) dejan **merge commits en
`main` que `develop` no tiene**. Como `develop` es la rama por defecto, GitHub ve
`main` "por delante" y sugiere una PR (aviso *«main had recent pushes»*). Reintegra
para alinear el grafo y silenciar el aviso:

```
git switch develop && git merge --ff-only main && git push origin develop
```

Si no fuese fast-forward, un `git merge main` normal. Tras esto `main` y `develop`
apuntan al mismo commit y el banner desaparece. **No** abras la PR que sugiere
GitHub: sería `main → develop` (al revés) y arrastraría solo esos merge commits.

## Verificación

- **Release**: `gh release view vX.Y.Z` → sin draft, sin prerelease (salvo sufijo),
  **5 assets** (`checksums.txt` + `enu-vX.Y.Z-{linux,darwin}-{amd64,arm64}.tar.gz`);
  `gh release list` la marca `Latest`.
- **Web**: `https://dbareagimeno.github.io/enu/` responde `200` y el badge muestra
  `vX.Y.Z` (0 enlaces `/nu/` residuales).
- **Instalador**: `install.sh` resuelve la última estable dinámicamente y baja
  `enu-vX.Y.Z-<os>-<arch>.tar.gz` — mismo nombre que produce `release.yml`.

## Reposo

Checkout principal en `develop` actualizado; worktrees de tareas eliminados
([CLAUDE.md](../CLAUDE.md) §«Convenciones de Git»).
