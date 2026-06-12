# nu

> Un runtime de Lua orientado a terminal cuya killer app es un coding
> harness. Un binario Go, kernel mínimo, y todo lo demás — incluido el
> propio agente — extensiones Lua.

Estado: **fase de diseño**. Aún no hay código; estos documentos *son* el
proyecto. La API se valida escribiendo pseudocódigo contra ella antes de
congelarla.

## Documentación

Orden de lectura sugerido:

1. [Filosofía](docs/filosofia.md) — principios y lo que nu no es
2. [Arquitectura](docs/arquitectura.md) — la forma del sistema (vista estática)
3. [Modelo de ejecución](docs/modelo-ejecucion.md) — concurrencia, comunicación y limitaciones (vista dinámica)
4. [API del core](docs/api.md) — la superficie sagrada v1
5. [ADR](docs/adr.md) — registro de decisiones y su razonamiento

Contratos de las extensiones oficiales:

- [Providers](docs/providers.md) — registro TOML y adaptadores de LLM
- [Agente](docs/agente.md) — el motor headless: turno, tools, permisos, subagentes
- [Sesiones](docs/sesiones.md) — persistencia JSONL append-only
- [Chat](docs/chat.md) — la UI oficial

Para autores de plugins:

- [Guía de plugins](docs/guia-plugins.md) — sabiduría práctica y checklist

Proceso y registro de trabajo:

- [Pseudocódigo de validación](docs/pseudocodigo.md) — las rondas que torturaron la API
- [Problemas abiertos](docs/problemas.md) — grietas que la v1 necesita cerradas
- [Pospuesto](docs/pospuesto.md) — lo que decidimos no decidir todavía, con su disparador
