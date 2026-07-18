---
title: "Granularidad de `caps`"
type: "hallazgo"
id: "G6"
status: "resuelto"
origin: "ronda 3-4 de pseudocódigo (zonas sin torturar)"
resolution: "Caps por función y por módulo con deny-by-default para funciones futuras; el vocabulario curado vive en la extensión del agente."
affected: ["api.md §13"]
adr: "ADR-010"
---
# G6 · Granularidad de `caps` — `api.md` §13 — **RESUELTO**

**Resolución** (aplicada en [api.md](../contracts/api.md) §13, [agente.md](../contracts/agente.md)
§9, guía §3; nueva ADR-010): mecanismo por función en el core (dos
granularidades: `"fs"` módulo, `"fs.read"` función; deny-by-default para
funciones futuras), vocabulario como tablas inspeccionables de la
extensión del agente (`agent.caps.FS_RO`). Los paquetes curados en el core
se descartaron (esconden juicios y redistribuyen poder retroactivamente al
crecer la API); el scoping por rutas va a [P17](../postponed/pospuesto.md). Derivada:
ADR-010 — las extensiones oficiales se distribuyen embebidas pero
**inactivas por defecto**, activación explícita de una tecla.

**Problema.** `caps` concede módulos enteros: `"fs"` incluye `write` y
`remove`. El subagente auditor de solo lectura — el caso estrella del
sandboxing — no se puede expresar.

**Impacto.** Una de las features diferenciales (permisos duros) se queda
corta en su mejor caso de uso.

**Opciones.** (a) Caps con sufijo de modo: `"fs:ro"` (lista corta y
curada de variantes por módulo, sin inventar un lenguaje de policies);
(b) caps por función (`"fs.read"`, `"fs.stat"`): expresivo pero
N×funciones de superficie a congelar; (c) scoping por ruta además del
modo (`fs:ro:/repo`): el más potente y el más caro de especificar bien;
(d) dejar módulo-entero en v1 y anotar en pospuestos.
