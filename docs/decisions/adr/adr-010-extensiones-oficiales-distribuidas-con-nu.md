---
title: "Extensiones oficiales: distribuidas con nu, no activas por defecto"
type: "adr"
id: "ADR-010"
status: "aceptada"
date: "2026-06"
---
# ADR-010 · Extensiones oficiales: distribuidas con nu, no activas por defecto

**Estado:** Aceptada · 2026-06 (modifica una consecuencia de ADR-003 y el
principio 5 de la filosofía) · **Refinada por [ADR-015](#adr-015--conjunto-oficial-de-producto-y-onramp-no-interactivo)** (qué es "el conjunto oficial" y cómo se activa sin TTY; no la reemplaza: "inactivas por defecto" sigue siendo de este ADR)

**Contexto.** ADR-003 decidió embeber las extensiones oficiales
(`go:embed`) "preservando la experiencia batteries-included", lo que
implicaba activarlas por defecto. Al resolver G6 (paquetes de caps como
tablas de la extensión del agente) se reabrió la pregunta y se decidió un
modelo más austero.

**Decisión.** Las extensiones oficiales (agente, chat, providers, MCP,
toolkit, paquetes `agent.caps.*`) **no se activan por defecto**: se
distribuyen con nu, pero las activa quien las quiere. Activación explícita
y trivial (config o primer arranque, una tecla). Distribución: siguen
embebidas en el binario — inactivas — para no romper la promesa "un
binario, offline" (activar no requiere red).

**Razonamiento.** Coherencia radical con "el core no sabe lo que es un
agente": tampoco lo presupone. nu instalado es un runtime desnudo; el
harness es una elección del usuario, no un hecho consumado. Mismo modelo
mental que Neovim (el editor no trae plugins activados) — el público
objetivo lo espera así.

**Consecuencias.** El primer arranque debe ofrecer la activación del
conjunto oficial (sin eso, la primera experiencia sería una pantalla
vacía); el "agente funcionando en el primer minuto" pasa de automático a
"a una tecla de distancia". La filosofía §5 se reescribe. `nu.toml` pasa
de `plugins.disabled` a gobernar la activación (`plugins.enabled` o
equivalente — detalle del loader).

---
