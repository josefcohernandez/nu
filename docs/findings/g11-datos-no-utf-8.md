---
title: "Datos no-UTF-8 en las fronteras JSON"
type: "hallazgo"
id: "G11"
status: "resuelto"
origin: "ronda 3-4 de pseudocódigo (zonas sin torturar)"
resolution: "enu.json.encode lanza EINVAL ante UTF-8 inválido y las tools oficiales sanean el output binario de forma visible."
affected: ["api.md §12 / transversal"]
---
# G11 · Datos no-UTF-8 en las fronteras JSON — `api.md` §12 / transversal — **RESUELTO**

**Resolución** (aplicada en [api.md](api.md) §12 y guía §5): el codec es
estricto (`encode` lanza `EINVAL` ante UTF-8 inválido) y las tools sanean
en el origen, visiblemente (`[output binario: NKB omitidos]`). Base64
automático descartado (blob inesperado para el LLM, ambigüedad para el
lector); `U+FFFD` silencioso en el codec descartado (esconde corrupción en
todas las fronteras — sanear es decisión con contexto).

**Problema.** Un tool result con bytes binarios (cat de un PNG) cruza
tres fronteras que asumen JSON/UTF-8 (request al provider, transcript
JSONL, mensajes de worker) sin regla definida: ¿lanzar, reemplazar,
base64? El bug aparecería lejos del origen.

**Impacto.** Robustez básica de la tool `bash` — pasará el primer día.

**Opciones.** (a) `enu.json.encode` lanza `EINVAL` ante UTF-8 inválido y
las tools sanean (reemplazo lossy + nota "output binario truncado") —
regla en la guía y en la tool oficial; (b) base64 automático con marca;
(c) reemplazo silencioso con U+FFFD en el codec (cómodo, pero esconde
corrupción).
