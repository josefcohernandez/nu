---
name: sesion
description: Ejecuta la sesión de implementación que marca el puntero ▶ de docs/implementacion.md con el ciclo BDD→TDD→juicio→DoD, y cierra moviendo puntero y bitácora en el mismo commit. Úsala cuando la tarea sea construir (no diseñar) y exista una sesión abierta en el plan; si la feature no está en el plan, antes va /planificar-sesion.
---

# Sesión de implementación (BDD → TDD → juicio → DoD)

Una sesión = una feature. El estado vive en el repo, no en tu memoria. Este
protocolo envuelve el de `docs/implementacion.md` §"Protocolo de cada sesión"
con las disciplinas BDD y TDD y el juicio clean-room.

## Pasos

1. **Sitúate.** Lee el puntero ▶ y la última fila de la bitácora de
   `docs/implementacion.md`. Implementa **solo** la sesión que marca el
   puntero; verifica que sus dependencias ("Depende de") están cerradas. Si el
   puntero está en `—`, no hay nada que implementar: ofrece
   `/planificar-sesion`. Crea el worktree y la rama de la tarea con
   `EnterWorktree` y renombra la rama a `claude/<tema>` — la convención
   completa vive en CLAUDE.md §"Convenciones de Git".

2. **Escenarios BDD (antes de una línea de código).** Lanza el agente
   `escenarista-bdd` en modo sesión con la S## y sus §N de espec. Revisa los
   escenarios devueltos: son tu lista de casos. Si el escenarista reporta un
   candidato a hallazgo (la espec no da para escribir un escenario), trátalo
   ya: `/hallazgo`. Los escenarios que blindan un G## lo nombran en comentario.

3. **TDD: rojo → verde → refactor.**
   - **Rojo**: escribe los escenarios como subtests table-driven
     (`t.Run("dado_X_cuando_Y_entonces_Z", ...)`, `testing` estándar, sin
     testify) y compruébalos fallar por la razón correcta.
   - **Verde**: el mínimo que cumple la espec — la firma y semántica exactas
     del §N, marcadores ⏸/[W] incluidos, **ni una función de más**.
   - **Refactor** con los tests en verde.
   - **Si la API no basta para implementar la espec: PÁRATE.** Es un hallazgo
     G##: `/hallazgo` primero (documentos), código después. Nunca al revés.

4. **Mutación (solo sesiones 🔒).** Si la sesión está en el inventario 🔒,
   ejecuta `/mutacion` acotada a los ficheros tocados. Cada mutante LIVED: o
   un test nuevo que lo mata, o queda anotado como equivalente en la bitácora.

5. **Juicio clean-room.** Ejecuta `/juicio` con el diff de la sesión — su
   política de coste decide el panel (sesión 🔒 o que toca API/contratos →
   panel completo; wrapper fino → solo juez-espec; glue → nada). **Siempre
   antes de escribir la bitácora** (la bitácora contiene tu racionalización y
   contaminaría a los jueces). Arregla los hallazgos REALES antes de seguir.

6. **Definition of Done** (las cinco, sin excepción):
   1. `CGO_ENABLED=0 go build ./...` verde.
   2. Tests al nivel que pide su lógica: snippet Lua que ejercita la firma
      desde el lado del autor de extensiones + unitarios 🔒 si aplica.
      `go test -race -shuffle=on` sobre los paquetes tocados (excepto
      `internal/vmwasm`, que tiene su régimen propio — ver `.github/workflows/ci.yml`).
   3. La espec se respeta (lo confirmó el juicio).
   4. No regresiona: la suite completa sigue verde.
   5. Deja rastro: `gofmt`, `go vet`, lint limpios.

7. **Cierre — todo en el MISMO commit:** el código, el puntero ▶ avanzado a
   la sesión siguiente (o `—`), la fila nueva de la bitácora (fecha, sesión,
   commit, notas: hallazgos, desviaciones, lo que debe saber la siguiente) y,
   si cerraste fase, el tablero marcado. Un commit que toca código sin mover
   el puntero es una sesión a medias. **Si la sesión cierra fase**: ejecuta
   antes su checkpoint 🔎 CP-N; si falla, el puntero no se mueve y la bitácora
   anota qué falló. Mensaje en español: `S##: <qué entrega>`, citando el G##
   si lo hubo. No abras PR salvo petición explícita. Si hay PR y se
   aprueba (merge a `develop`), elimina el worktree y la rama de trabajo —
   local y remota — en cuanto el merge esté hecho: `ExitWorktree(remove)` si
   la sesión sigue dentro, o `git worktree remove` + borrado de rama si no.
