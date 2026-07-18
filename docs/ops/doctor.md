---
title: "Espec del esquema `doctor.v1` (salida `--json` de `enu doctor`)"
description: "El contrato de la salida JSON de enu doctor: campos, semántica y política de evolución."
type: "runbook"
status: "vigente"
---
# Espec del esquema `doctor.v1` — salida `--json` de `enu doctor`

La espec del contrato que `enu doctor --json` imprime
([ADR-026](../decisions/adr/adr-026-subcomandos-de-gestion-del-binario.md),
pieza 3). No es API sagrada (`enu.*` no cambia): es superficie CLI del
binario, como los códigos de salida de S45 — pero **es un contrato que CI
ajena consume**, así que se congela aquí y evoluciona con reglas.

## Política de evolución

- Dentro de `v1`, el esquema **solo crece por adición**: un campo nunca
  cambia de tipo ni de significado, y nunca se retira. Añadir un campo o un
  check nuevo no rompe `v1`.
- Un cambio incompatible exige `doctor.v2` con su propia sección aquí; el
  campo `schema` es lo primero que un consumidor debe leer.
- Los `id` de checks son estables: un consumidor puede filtrar por `id` y
  confiar en que no cambia de nombre ni de significado.

## Esquema `doctor.v1`

Un único objeto JSON en stdout:

```json
{
  "schema": "doctor.v1",
  "version": "0.2.0",
  "os": "linux",
  "arch": "amd64",
  "checks": [
    {
      "id": "config.dir",
      "status": "ok",
      "summary": "config.dir() existe y es legible",
      "detail": "~/.config/enu",
      "remedy": null
    }
  ],
  "counts": { "ok": 0, "fail": 0, "skip": 0 },
  "exit_code": 0
}
```

Semántica de los campos:

- `schema` (string): literal `"doctor.v1"`.
- `version` (string): versión del binario (la de `--version`, sin `v`).
- `os` / `arch` (string): plataforma del binario.
- `checks` (array, orden de ejecución estable): un objeto por comprobación.
  - `id` (string): identificador estable, `area.nombre` (ver catálogo).
  - `status` (string): `"ok"` | `"fail"` | `"skip"` (skip = no aplica en este
    entorno, p. ej. checks de TTY en headless; **no** cuenta como fallo).
  - `summary` (string): una línea, humana, sin secretos.
  - `detail` (string|null): dato de apoyo (ruta, nombre de variable, versión).
    **Jamás** contiene el valor de una clave ni contenido de config sensible —
    la variable de una clave se reporta por **nombre y presencia**, nunca por
    valor.
  - `remedy` (string|null): en `fail`, la acción concreta que lo arregla (qué
    fichero/variable tocar); en `ok`/`skip`, `null`.
- `counts` (objeto): totales por estado, redundantes con `checks` (comodidad
  de CI).
- `exit_code` (int): el mismo código con el que sale el proceso (**0** todo
  verde —los `skip` no ensucian—, **1** al menos un `fail`, **2** uso
  inválido; con `2` puede no haber JSON).

## Catálogo de checks `v1`

Los `id` congelados al alta (S50 los implementa; añadir checks después es
adición legítima):

| id | Comprueba | Sin red |
|---|---|---|
| `binary.version` | versión/arquitectura del binario y que `--version` responde | sí |
| `config.dir` | `config.dir()` existe y es legible | sí |
| `config.parse` | los TOML presentes (`enu.toml`, `agent.toml`, `providers.toml`) parsean | sí |
| `plugins.enabled` | los plugins activados existen en el catálogo (embebido o instalado) | sí |
| `plugins.requires` | las dependencias (`requires`) de los activados resuelven | sí |
| `provider.model` | el modelo por defecto resuelve contra `providers.toml` | sí |
| `provider.key` | la variable de `api_key_env` está presente o ausente (por nombre; el valor jamás se lee más allá de la presencia) | sí |
| `sessions.perms` | `data_dir()/sessions/` respeta el `0600` de G57 (muestreo) | sí |
| `tty.caps` | TTY presente y capacidades del terminal (`skip` en headless) | sí |
| `tools.external` | herramientas externas declaradas por las extensiones activas están en `PATH` | sí |
| `provider.reach` | alcanzabilidad del endpoint del provider — **solo con `--net`**; sin el flag, `skip` | no |

Regla de implementación (ADR-026, pieza 3): los checks de producto consultan a
las extensiones o a su fuente única por la API pública; el binario no
re-implementa su semántica ni mantiene tablas propias de conocimiento de
producto.
