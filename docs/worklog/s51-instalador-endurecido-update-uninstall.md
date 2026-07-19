---
title: "Instalador endurecido + `enu update`/`enu uninstall` (Fase 9, ADR-026 piezas 4-5)"
type: "sesion"
id: "S51"
phase: 9
status: "cerrada"
---
# S51 — Instalador endurecido + `enu update`/`enu uninstall` (Fase 9 — Producto)

**Qué es.** Los dos últimos subcomandos de gestión ([ADR-026](../decisions/adr/adr-026-subcomandos-de-gestion-del-binario.md)
piezas 4-5) y el endurecimiento del instalador, con la espec operativa en
[release.md](../ops/release.md) §Instalador. Depende de S48 (la matriz de smoke) y
S49 (el dispatcher). Superficie CLI (`package main`), no API sagrada.

**Qué se entregó.**
- `update.go` (nuevo): `runUpdate` (núcleo testeable), `verifyChecksum` (la
  verificación de checksum en **Go compartido**, la que consume `enu update`),
  `extractBinaryFromTarGz`, `probeWritable`, `atomicReplace`, `currentVersionTag`,
  `sameVersion`, y el `releaseFetcher` inyectable (`httpReleaseFetcher` en
  producción; el parseo `parseLatestStableTag`/`jsonStringValue` en paralelo al
  awk de `install.sh`).
- `uninstall.go` (nuevo): `runUninstall` (núcleo testeable) + `isWithin` (guardia
  de que `data_dir()` no cae dentro de `config.dir()`).
- `init.go`: el `case "update"`/`case "uninstall"` del dispatch pasa de
  «reservado» a `runUpdateMain`/`runUninstallMain`.
- `install.sh`: **quita el `sudo`** (alineación código↔espec, ver decisión 1),
  reemplazo **atómico** escribir-al-lado + `rename`, y la guarda
  `ENU_INSTALL_NO_MAIN=1` (para el humo de S48).
- `.github/workflows/smoke-instalacion.yml`: job `checksum` nuevo — el humo del
  camino shell de la verificación (corrupto rechazado + íntegro aceptado), en
  aislamiento con la guarda, sin necesidad de una release real.
- `main_update_test.go` / `main_uninstall_test.go` (nuevos): el 🔒 table-driven
  de `verifyChecksum` y los flujos de `update`/`uninstall`.

**Decisiones operativas (bajo umbral de G##).**

1. **`install.sh` NO eleva privilegios (quita el `sudo`): deriva código↔espec
   preexistente, no un hallazgo.** El escenarista BDD la marcó como candidato a
   hallazgo, pero `release.md` §Instalador ya dice, negro sobre blanco, «el
   instalador **nunca eleva privilegios** ni escribe fuera del destino» y la fila
   S51 del plan dice «**sin sudo**»: la espec estaba **congelada**; el `sudo mv`
   de `install.sh` era código que se había desviado de ella (se escribió antes de
   que la espec se fijara). Corregirlo es **aplicar** la espec, no decidir nada
   nuevo, así que no abre un `G##` (los `G##` son huecos que la espec *no*
   cubre). Ahora, destino no escribible → aborta con remedio (fija
   `ENU_INSTALL_DIR` a un directorio tuyo), coherente con `enu update`.
2. **`releaseFetcher` inyectable: costura de testeabilidad, no API.** Sin ella,
   la garantía 🔒 «checksum corrupto no toca el binario» solo se podría probar
   con una release real y red. El fake devuelve tarball + `checksums.txt`
   canónicos; producción usa `httpReleaseFetcher`. Mismo patrón que los streams
   inyectables de `enu init`/`enu doctor`.
3. **«Escribir-al-lado» = MISMO directorio que el destino, nunca `os.TempDir()`.**
   `atomicReplace` crea el sidecar con `os.CreateTemp(filepath.Dir(dest), …)`, no
   en el temporal del sistema: así el `rename` **jamás** cruza sistema de
   ficheros (no hay `EXDEV`) y es la sustitución atómica canónica de un binario
   **en uso** en Linux (el proceso vivo conserva su inodo; el nuevo arranque ve
   el nuevo — de paso esquiva el `ETXTBSY`, que solo aparece al escribir/truncar
   *in situ*, cosa que no hacemos). Los «bordes ETXTBSY/cross-device» del
   inventario 🔒 quedan cubiertos **por construcción**, no por un caso de test
   (un `rename` same-dir no puede darlos).
4. **La escribibilidad se prueba ANTES de descargar.** `probeWritable` (crea y
   borra un temporal en el dir del destino) corre antes del `Download`, así un
   destino gestionado por un tercero aborta con remedio sin gastar red. El test
   `TestUpdateDestinoNoEscribible…` afirma además que **no se descargó nada**. El
   probe usa un fallo real de creación (destino cuyo «directorio» es un fichero →
   `ENOTDIR`) para ejercer el camino **incluso como root** (los bits de permiso
   no bastarían: root los ignora).
5. **Verificación de checksum en DOS implementaciones, misma disciplina.** El
   plan pide «Go compartido» = una impl Go (`verifyChecksum`) que usa `enu
   update`, con test table-driven; `install.sh` conserva su propia impl shell
   (`verify_checksum`) porque el camino shell no puede llamar al binario Go
   (causalidad: se instala *antes* de que el binario exista). «Compartido» es la
   disciplina (checksum obligatorio, fail-closed), no un único fichero de código.
   El humo de S48 cubre el camino shell; el table-driven, el Go.
6. **`enu uninstall` borra el binario sin confirmar; `--purge` sí confirma.** La
   espec pide confirmación **explícita** solo para el purge de `config.dir()`
   (destructivo de configuración del usuario); borrar el binario es el propósito
   mismo del comando. `data_dir()` es intocable por construcción: se hace
   `os.RemoveAll(configDir)` EXACTO (nunca su padre), y una guardia `isWithin`
   rehúsa el purge si `data_dir()` anida dentro de `config.dir()` (config
   atípica). Un EOF/`n` en la confirmación conserva la config (no es un «sí»).

**Alcance del pointer.** S51 **no** cierra la Fase 9: quedan S52 (limpieza
nu→enu en las traducciones inglesas, ortogonal) y **CP-12** (humo del funnel
completo + **mutación 🔒 BATCHEADA** de S49/S50/S51, decisión del operador). El
puntero avanza a S52.

**DoD.** `CGO_ENABLED=0 go build ./...` verde; `gofmt`/`go vet` limpios; `go test
-race -shuffle=on ./` verde (sin regresiones: se quitaron del routing test de S49
los casos `update`/`uninstall` «reservado» —ahora implementados y con efectos
reales: `uninstall` borraría `os.Executable()`—; su enrutado lo cubren los
`TestUpdate*`/`TestUninstall*`). El humo shell del checksum verificado en local.

**Juicio clean-room (sesión 🔒, panel de dos jueces).**
- **juez-espec: CONFORME.** Diez refutaciones intentadas (orden verificar→escribir,
  rastreo de `sudo`, no-op de misma versión, `--purge` vs `data_dir()`,
  confirmación explícita, atomicidad/EXDEV/ETXTBSY, caso corrupto en el smoke,
  «Go compartido» vs verificación shell, cierre del conjunto de verbos, código de
  salida del borde no-escribible): **ninguna prospera**.
- **juez-tests: SUITE INSUFICIENTE → corregida a suficiente.** Cazó cuatro huecos,
  todos de test (el código ya cumplía): **(T1, crítico)** el fixture de
  `uninstallDirs` ponía config y datos en ramas hermanas de `base`, así que un
  mutante `RemoveAll(filepath.Dir(configDir))` —el borrado-de-más— sobrevivía;
  **arreglado** poniendo un centinela `vecino.txt` DENTRO del padre de
  `config.dir()` y afirmando que sobrevive al purge (más el padre). **(T2)** la
  invariante «sidecar en el mismo dir» y la limpieza en fallo no se ejercían:
  añadido `TestAtomicReplace` (éxito sin residuo; fallo de `rename` sobre un
  directorio no vacío deja el destino intacto y sin sidecar). **(T3)** `checksums.txt`
  ausente (caso propio de la espec, distinto de «corrupto») no se probaba a nivel
  de `runUpdate`: añadido `TestUpdateChecksumsAusenteNoTocaElBinario`. **(T4)** el
  test de destino no escribible no comprobaba el remedio: ahora captura stderr y
  exige que mencione «privilegios» y `ENU_INSTALL_DIR`.

**Mutación 🔒 diferida a CP-12** (batch de la Fase 9): la pasada de `/mutacion`
sobre `verifyChecksum`/`atomicReplace`/`runUpdate`/`runUninstall` se corre junto
con la de S49 y S50 al cierre de la fase.
