-- Extensión embebida de ejemplo (S12). NO es una extensión oficial real (esas
-- llegan en la Fase 8: agente, chat, providers...): es el STUB mínimo que existe
-- para poder probar el *gating* de activación por `nu.toml` (ADR-010: las
-- embebidas están INACTIVAS por defecto; solo se cargan si `plugins.enabled` las
-- nombra). Su init.lua deja una huella observable para que un test confirme que,
-- cuando se activa, se materializa y corre como cualquier otro plugin.
--
-- Cuando lleguen las extensiones oficiales reales, este directorio embebido se
-- amplía con las suyas (cada una su `plugin.toml` + `init.lua`); el mecanismo de
-- embebido y gating no cambia.
nu.log.info("extensión embebida 'example' activada")
_example_embedded_cargada = true
