---
title: "Baseline completo y reproducible de lint antes de congelar v1"
type: "adr"
id: "ADR-021"
status: "aceptada"
date: "2026-07"
---
# ADR-021 · Baseline completo y reproducible de lint antes de congelar v1

**Estado:** Aceptada · 2026-07 (**refina** [ADR-013](adr-013-integracion-continua-y-publicacion.md), punto 5; no cambia el resto de su política de CI ni la superficie de [api.md](../../contracts/api.md))

**Contexto.** ADR-013 introdujo un conjunto pequeño de linters con
`only-new-issues: true` para que la deuda del código ya escrito no bloqueara la
construcción. Era una concesión de migración, no el estado deseado para congelar
v1. La limpieza posterior a M17 corrigió los 26 hallazgos que dejó la retirada
de gopher-lua y el análisis completo de `golangci-lint` v2.12.2 queda en **cero
hallazgos**. Mantener el filtro por líneas modificadas ya no protege una
transición: permite que un diagnóstico introducido por cambios indirectos o por
una actualización de la herramienta permanezca invisible mientras no coincida
con el diff.

**Decisión.** El baseline de lint cubre **todo el repositorio** en cada Pull
Request y push a `main`; `only-new-issues` queda desactivado. El workflow fija
el binario en `golangci-lint` v2.12.2, la versión con la que se estableció el
baseline limpio. Actualizarla es un cambio deliberado: la nueva versión debe
ejecutarse sobre el árbol completo y entrar solo cuando vuelva a producir cero
hallazgos. "Baseline limpio" se refiere al conjunto explícito de ADR-013
(`govet`, `staticcheck`, `errcheck`, `ineffassign`, `unused`); ampliar ese
conjunto sigue requiriendo una justificación independiente. No se añaden
exclusiones ni directivas `nolint` para alcanzar el cero.

**Consecuencias.**

- Cualquier hallazgo de los cinco linters bloquea la CI aunque esté fuera de las
  líneas tocadas por el PR; la deuda nueva no puede acumularse silenciosamente.
- Fijar el binario evita que una release del linter cambie el gate sin revisión.
  A cambio, las actualizaciones pasan a ser mantenimiento explícito y deben
  demostrar de nuevo el baseline completo.
- La política endurece el gate de calidad previo a v1, pero no modifica ninguna
  firma, semántica o versión de la API sagrada.

---
