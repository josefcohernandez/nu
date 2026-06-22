-- Extensión oficial `sessions` (S38). Implementa el contrato de persistencia de
-- [sesiones.md](../../../../docs/sesiones.md): el transcript **JSONL append-only**
-- y el **lockfile de un escritor por sesión** (§6, G5).
--
-- ADR-003: el core NO sabe lo que es una sesión; toda esta lógica es Lua puro
-- sobre la API pública congelada ([api.md](../../../../docs/api.md)), sin
-- privilegio de kernel. Sus únicas primitivas (sesiones.md §1.4): `nu.fs`
-- (read/append/write{exclusive}/list/stat/remove/mkdir), `nu.json`
-- (encode/decode), `nu.proc.alive`, `nu.sys` (pid/hostname/now_ms — la última
-- añadida por G17/G32) y `nu.config.data_dir`. El namespace de eventos de esta
-- extensión es `sessions:` (el del propio plugin, por convención §4; el core
-- solo reserva `core:`/`ui:`).
--
-- El `init.lua` solo CABLEA: deja el módulo público accesible por `require`. La
-- API de consumo (`open`, `Session:append`, `Session:replay`, `Session:close`,
-- `list`) la expone el módulo `sessions`, requerible por el agente (S39) y
-- cualquier extensión/herramienta con `require("sessions")` —el formato JSONL es
-- API pública (§7): pickers, exportadores y estadísticas leen sin pasar por aquí.

require("sessions")
