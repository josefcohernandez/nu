---
title: "Licencia: Apache 2.0"
type: "adr"
id: "ADR-014"
status: "aceptada"
date: "2026-06"
---
# ADR-014 · Licencia: Apache 2.0

**Estado:** Aceptada · 2026-06

**Contexto.** El kernel ya es código real y se va a distribuir (ADR-013), pero el
repo no tenía licencia: sin ella, legalmente nadie puede usar ni redistribuir
`nu`. El autor quiere dos cosas a la vez, en apariencia en tensión: (1) que sea
**open source de verdad**, para aportar a la comunidad y maximizar adopción, y
(2) conservar la opción de **comercializarlo o venderlo** en el futuro si el
proyecto despega (el patrón de productos como pi/pdf.ai, donde el dueño pudo
vender). La clave —y la razón de que no haya contradicción— es que el poder de
vender/relicenciar **no nace de la licencia, sino de la titularidad del
copyright**: quien posee el 100% del código puede siempre, además de publicarlo
con una licencia abierta (que es no exclusiva), ofrecer una licencia propietaria
o ceder el proyecto entero. El riesgo a esa titularidad no es la licencia
elegida, sino **aceptar código de terceros sin cesión de derechos**.

Sobre la autoría: el único autor de `nu` es **Diego Barea**. La identidad
`Candela1011 <candelabr72@gmail.com>` que aparece en el historial de git no es un
segundo autor: es el `git config` que quedó en el ordenador prestado; no hay
co-titularidad. Se fijó la identidad del repo a nombre del autor para que el
rastro de autoría sea coherente.

**Decisión.** **Apache License 2.0**, copyright de Diego Barea. Se añaden a la
raíz: `LICENSE` (texto íntegro de Apache 2.0), `NOTICE` (atribución que la
licencia recomienda) y `CONTRIBUTING.md`. Las aportaciones externas se gestionan
**caso por caso, sin CLA formal por ahora**, pero `CONTRIBUTING.md` **reserva
expresamente** el derecho del mantenedor a pedir cesión de derechos o un acuerdo
de contribución antes de fusionar código de terceros. Así la titularidad se
mantiene unificada y la opción de comercializar sigue viva, sin imponer todavía
la fricción de un CLA.

**Razonamiento.**
- **Por qué permisiva y no copyleft (AGPL/GPL).** El objetivo es adopción amplia
  y "dar a la comunidad". Una AGPL volvería `nu` copyleft viral (quien lo corra
  modificado como servicio debe publicar sus cambios), lo que **reduce** la
  adopción y se usa cuando se quiere *forzar* compradores comerciales de forma
  continua —no es el caso—. Para la meta "vendible algún día" basta con la
  titularidad; una permisiva no se la quita.
- **Por qué Apache 2.0 y no MIT.** Ambas son permisivas y ambas preservan el
  derecho a vender. Apache 2.0 añade una **concesión explícita de patentes**
  (protege al autor y a los usuarios si esto se vuelve un negocio) y una cláusula
  de contribución (§5) que encaja con un futuro CLA. El coste es un `LICENSE` más
  largo y un `NOTICE`; merece la pena para un producto con ambición comercial.
- **Por qué sin CLA todavía.** Hoy el autor posee el 100% y puede vender sin
  pedir permiso a nadie; un CLA solo hace falta cuando entra código ajeno. Montar
  el CLA ahora sería fricción prematura. La cláusula de `CONTRIBUTING.md` evita
  el riesgo real (que alguien asuma que su PR entra con su copyright intacto)
  manteniéndolo barato.

**Consecuencias.**
- `nu` es libre para usar, estudiar, modificar y distribuir (incluso
  comercialmente) bajo Apache 2.0; la CI y el release ya pueden publicar con una
  licencia válida.
- El autor conserva la titularidad y, por tanto, la capacidad de ofrecer una
  versión propietaria o vender el proyecto. **Disparador de reapertura:** si el
  volumen de contribuciones externas crece, formalizar un CLA (texto + bot tipo
  CLA-assistant) para no tener que negociar cesiones una a una; el marco ya está
  anunciado en `CONTRIBUTING.md`.
- Si en el futuro se crea una entidad/empresa para comercializar `nu`, se
  actualiza el nombre del copyright; no requiere cambiar de licencia.
- No se añaden cabeceras de licencia por fichero `.go` (el `LICENSE` en la raíz
  basta para Apache 2.0 en un módulo de un solo titular); si algún día se acepta
  código de terceros, se revisará por si conviene marcar autoría por fichero.

---
