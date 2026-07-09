---
name: planificar-sesion
description: Da de alta una feature nueva como sesión S## en docs/implementacion.md, pasando primero por la puerta SDD (¿la espec ya la cubre?) y por el juez de filosofía. Úsala cuando el usuario quiera una capacidad nueva y el puntero ▶ esté en '—' o la feature no exista en el plan. No implementa nada: deja el plan listo para /sesion.
---

# Planificar una sesión nueva (la puerta SDD)

El plan de construcción manda (`docs/implementacion.md`) y la API es sagrada:
una feature entra al plan **solo** si su espec ya existe. Esta skill separa a
fuego los dos modos del proyecto — diseño (no se escribe código) y
construcción (el plan manda) — para que "añadir una sesión" no se convierta en
la vía de hecho para ampliar la API.

## Pasos

1. **Puerta SDD.** Localiza la espec de la feature: el §N de `docs/api.md` o
   del contrato de extensión (`agente.md`, `providers.md`, `sesiones.md`,
   `chat.md`) que la define con firma y semántica completas.
   - **Si no existe, o existe pero es ambigua o incompleta: PÁRATE.** No hay
     sesión que planificar; hay diseño que hacer. Dile al usuario qué falta y
     ofrece el camino: `/hallazgo` (si es una grieta concreta) o `/ronda` (si
     hay que explorar la zona con pseudocódigo primero). Solo cuando la espec
     esté congelada se vuelve aquí.
   - Si la feature es de una extensión y su espec vive en el contrato de la
     extensión, comprueba además que la API del core que necesita ya existe
     (corolario de completitud).

2. **Juicio de filosofía.** Lanza el agente `juez-filosofia` con el texto de
   la propuesta (qué se construye, contra qué §N, si añade algo a api.md).
   Una OBJECIÓN bloqueante vuelve al usuario con la cita; no se planifica en
   contra del veredicto sin decisión explícita del usuario.

3. **Redactar la S##.** Siguiente número libre (la numeración es secuencial y
   append-only). Usa el formato de las tablas del plan — columnas: Sesión,
   Feature, Depende de (sesiones que deben existir antes; el grafo es
   estricto), Espec (§N que la define), Criterio de hecho (la prueba concreta
   que la cierra). Si abre una fase nueva, define también su checkpoint de
   integración 🔎 CP-N (la prueba de humo de extremo a extremo).

4. **Evaluar 🔒.** Contrasta la lógica de la sesión con los tres criterios de
   la política de tests (§"Política de tests"): ¿la lógica es nuestra?, ¿el
   fallo es silencioso o de borde?, ¿implementa un G##? Si cumple cualquiera,
   añade la fila al inventario 🔒 con **el caso exacto que el test debe
   blindar**. El inventario crece, nunca se relaja.

5. **Actualizar el plan.** Añade la sesión a su tabla (o crea la fase),
   apunta el puntero ▶ a ella si no hay otra sesión en curso, y deja el
   tablero consistente.

6. **Commit de diseño** en español: `Plan: alta de S## (<feature>)`, citando
   el §N de espec y el G##/ADR que la motivó si lo hay. Esta skill NO
   implementa: el código llega después con `/sesion`.

## Guardarraíles

- Nunca des de alta una sesión cuya espec piense "ya la escribiré
  implementando": eso invierte SDD y el punto 3 del protocolo lo prohíbe (el
  código nunca corrige la espec por la vía de hecho).
- Si la propuesta exige una adición a `api.md`, esa adición es un cambio de
  diseño previo: pasa por `/hallazgo` (con bump de `nu.version.api` y ADR si
  procede) antes del alta.
- No juntes features: una sesión = una capacidad observable y probada que cabe
  en una ventana de contexto.
