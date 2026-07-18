---
title: "El primer arranque de ADR-010 no tiene dueño"
type: "hallazgo"
id: "G21"
status: "resuelto"
origin: "revisión de coherencia de la documentación completa"
resolution: "El runtime desnudo pinta una pantalla fija pre-Lua de activación (versión, rutas, extensiones, acciones), sumando la extensión oficial repl."
affected: ["ADR-010", "api.md §14"]
---
# G21 · El primer arranque de ADR-010 no tiene dueño — ADR-010 / `api.md` §14 — **RESUELTO**

**Resolución** (aplicada en [api.md](../contracts/api.md) §14,
[filosofia.md](../core/filosofia.md) §2 y [arquitectura.md](../core/arquitectura.md)):
opción (a), reencuadrada con la formulación general del principio — **el
kernel solo conoce sus propias capacidades** —, bajo la cual esto no es
una excepción: las extensiones embebidas y su activación son capacidad
del loader, así que la pregunta es del kernel. El runtime desnudo (TTY +
ningún plugin activo) pinta una **pantalla fija de runtime**: versión y
API, rutas, extensiones embebidas y acciones (activar el conjunto
oficial, activar sueltas, salir) — render fijo, pre-Lua, sin lógica de
producto; es la cara permanente de enu sin plugins, no un diálogo de
primera vez. El apetito de "algo usable sin el harness" lo cubre una
extensión oficial más: **`repl`** (REPL de Lua sobre la API pública),
activable sola desde esa pantalla. Descartados: la extensión bootstrap
siempre-activa (un plugin privilegiado sin precedente, y exigiría añadir
activación de plugins en runtime a la API sagrada solo para esa
pantalla) e imprimir-y-salir (contradice la "una tecla" de ADR-010 y la
filosofía §5).

**Problema.** Con las extensiones oficiales inactivas por defecto y un
core que no pinta ni sabe de agentes (`enu.log` "nunca a la pantalla"),
¿qué código muestra el ofrecimiento de activación "de una tecla" del
primer arranque? La consecuencia central de ADR-010 no tiene mecanismo.

**Impacto.** La primera experiencia del usuario — exactamente lo que
ADR-010 dice proteger.

**Opciones.** (a) Excepción mínima y declarada en el loader: si no hay
plugins activos y hay TTY, el core pinta un prompt fijo de activación
(la única UI del core, deliberadamente trivial); (b) una extensión
oficial `bootstrap` siempre activa que hace solo esto (¿contradice el
"ninguna se activa sola" de ADR-010?); (c) sin UI: el binario imprime
instrucciones (`enu --enable-official`) y sale — austero pero hostil.
