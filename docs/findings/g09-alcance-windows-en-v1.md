---
title: "Alcance Windows en v1"
type: "hallazgo"
id: "G9"
status: "resuelto"
origin: "ronda 3-4 de pseudocódigo (zonas sin torturar)"
resolution: "v1 soporta Linux/macOS nativos; Windows se usa dentro de WSL2, y Windows nativo queda pospuesto como P18."
affected: ["transversal"]
---
# G9 · Alcance Windows en v1 — transversal — **RESUELTO**

**Resolución**: v1 soporta Linux y macOS nativos; en Windows, **enu se usa
dentro de WSL2** (documentado como requisito, no como apología). Ventaja
decisiva: dentro de WSL2 el contrato POSIX se cumple íntegro — cero
especificación condicional, cero shell portable, cero semántica dual de
señales. Windows nativo queda en pospuestos ([P18](../postponed/pospuesto.md)) con su
disparador. La promesa "cross-compile a todas las plataformas" se matiza en
la arquitectura: el binario *compila* para Windows, el soporte v1 es WSL2.

**Problema.** La tool `bash` asume `sh`, `Proc:kill` habla señales POSIX,
y el input de terminal difiere (IME, teclas). Go cross-compila a Windows,
pero "compila" no es "funciona bien". Sin decisión de alcance, cada
contrato asume POSIX en silencio.

**Impacto.** Decisión de producto más que técnica; condiciona promesas de
la distribución ("un binario para todas las plataformas").

**Opciones.** (a) v1 = Linux/macOS de primera + Windows best-effort
documentado (la tool bash exige WSL o git-bash); (b) Windows de primera
desde v1 (coste alto: shell portable, semántica kill, pruebas de
terminal); (c) v1 sin Windows, explícitamente.
