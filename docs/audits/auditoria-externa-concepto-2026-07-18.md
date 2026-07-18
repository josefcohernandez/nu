---
title: "Auditoría externa de concepto, producto y comunicación — 18 de julio de 2026"
type: "auditoria"
date: "2026-07-18"
status: "cerrada"
---
# Auditoría externa de concepto, producto y comunicación — 18 de julio de 2026

Feedback externo independiente (revisión estática de repo, docs, código
visible, CI, releases y web; sin ejecutar el binario contra un modelo real),
recibido el 2026-07-18 y triado el mismo día. Es la **primera validación
externa sustancial** del proyecto. Sus decisiones derivadas viven en
[ADR-025](../decisions/adr/adr-025-reposicionamiento-motor-de-harnesses.md);
este documento conserva el contenido y el triaje.

## Veredicto del auditor (resumen fiel)

«Arquitectura prometedora, no producto validado.» Notas: originalidad
arquitectónica 8/10; ingeniería observable 7,5/10; claridad de tesis 8/10;
utilidad inmediata frente a alternativas 4/10; madurez de producto 4/10;
potencial de nicho 8/10; potencial mainstream 3/10 hoy. Global: 7/10 concepto
técnico, 4/10 producto actual, 8/10 potencial condicionado a caso de uso y
comunidad.

**Lo que valida:** la diferenciación no es cosmética (el core no sabe qué es
un agente; separación agent headless / chat; todo extensión sobre API
pública); «Lua decide, Go ejecuta» como división razonable; el modelo de
concurrencia «pensado, no improvisado»; disciplina de testing/CI «por encima
de lo esperable» (race detector, e2e contra binario real, PTY, providers
simulados); la documentación explica sus compromisos («publicas un modelo
mental, no solo código»).

**Lo que preocupa:** (1) el competidor real es **Pi**, no Claude Code — y el
README lo esquiva; (2) riesgo de ser «Emacs para agentes» antes de tener
usuarios (superficie gigantesca, un contribuidor, validación externa
inexistente); (3) la documentación/proceso interno está «peligrosamente cerca
de superar al producto» y la completitud se ha validado solo contra
extensiones propias (coherencia interna ≠ ergonomía externa); (4) la VM
PUC-Lua/wazero es el punto de mayor mantenimiento especializado; (5) «un
binario estático» no basta como killer feature — debe concretarse en
«distribuir un coding workflow corporativo completo como binario + directorio
Lua, auditable».

**El mercado que sí ve:** equipos de plataforma interna, autores de tooling
de agentes, DevOps/CI agentic (`enu -p`), entornos locales/air-gapped.

## Recomendaciones del auditor y triaje aplicado

| Recomendación | Triaje | Destino |
|---|---|---|
| Posicionar como «self-extensible coding harness with no host runtime»; comparar de frente con Pi | Aceptada | ADR-025, piezas 1-2 |
| Parar la infraestructura nueva; tres demos visibles antes que más core | Aceptada con matiz (ver T3) | ADR-025, pieza 3 (fases) |
| Sistema de distribución de plugins (`enu plugin add/…`, git + lockfile + checksums, manifiesto de capacidades, sin registry) | Aceptada | ADR-025, pieza 3 (Fase 2); reabre P4; registry → P40 |
| `enu init` + `enu doctor` + matriz de smoke tests en sistemas limpios | Aceptada | ADR-025, pieza 3 (Fase 1) → `/planificar-sesion` |
| Protocolo JSONL/RPC estable (`--json -p`, `serve --stdio`, eventos versionados) | Aceptada | ADR-025, pieza 3 (Fase 3); ronda de pseudocódigo previa |
| Plugin `forge` (enu se construye plugins a sí mismo, con staging/diff/aprobación, sin secretos ni red) como demo insignia | Aceptada | ADR-025, pieza 3 (Fase 2); ronda previa |
| Plugin `worktree` (subagentes paralelos aislados) | Aceptada — ya diseñado: es la parte visible de `mesh` (malla.md, G16) | ADR-025, pieza 3 (Fase 3) |
| Plugin `trace` (observabilidad del bus del agente) | Aceptada — consume eventos existentes (G40/G43) | ADR-025, pieza 3 (Fase 3) |
| «No congelar la API todavía» | **Matizada** (T1): disciplina aditiva se mantiene; pre-1.0 admite roturas por ADR; corte de 1.0 = 3 autores externos con extensiones no anticipadas | ADR-025, pieza 4 |
| Frente público en inglés | **Matizada** (T2): público inglés, fuente interna en español | ADR-025, pieza 5; CLAUDE.md |
| README: hero directo, demo, quickstart 3 comandos; eliminar «45 sesiones», «release va por detrás», CTA a la competencia, pseudocódigo-como-validación del camino de entrada | Aceptada | Fase 1 (sesiones de trabajo) |
| Web: legibilidad de la doc (cuerpo 15-16px, contraste, ancho 70-75 col), demo visual tras el hero, priorizar enlaces sobre teclas, congelar themes | Aceptada | Fase 1; congelación de themes en ADR-025 |
| `enu.sh` como dominio canónico; instalador con versión pineada, checksums, `enu update/uninstall` | Aceptada | Fase 1; toca `docs/ops/release.md` |
| Guía «Migrating from Pi to enu»; port automático Pi→Lua vía forge | Guía: Fase 3. Port automático: pospuesto | P42 |
| Repositorio independiente de plugins | Ya tratada: P38 (monorepo) y P40 (registry) | sin cambio |

**Tensiones resueltas por el operador (2026-07-18):** T1 (política de API) y
T2 (idioma) según los matices de la tabla; T3 (prioridad): las fases 1-3 van
primero y la cola durable de tasks (caso «tres tareas por Slack, que se hagan
las tres») se aparca como P41 — su transporte natural (RPC) es Fase 3 y su
doctrina de recursos es la resolución de G60.

**Desacuerdo registrado con el auditor:** el proceso interno (G##/ADR/rondas)
no es un defecto sino la causa probable de que la ingeniería le pareciera
seria; lo que sobra no es el proceso sino su **visibilidad en el camino de
entrada**. La corrección es jerarquía (README/web enlazan, no reproducen), no
amputación — coherente con las propias recomendaciones finales del auditor.
