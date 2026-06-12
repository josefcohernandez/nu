# Discusiones pospuestas

Registro de todo lo que se ha decidido **no decidir todavía**. Cada entrada
tiene un *disparador*: la señal que indica que toca reabrirla. Nada de esta
lista está rechazado; está esperando su momento. Cuando una entrada se
reabra y se decida, sale de aquí y entra en el ADR.

| # | Tema | Dónde se pospuso | Por qué | Disparador para reabrir |
|---|------|------------------|---------|--------------------------|
| P1 | TCP/sockets crudos (`nu.net.tcp`) | [api.md](api.md) §8 | HTTP+WS cubren los casos conocidos; menos superficie sagrada | Una extensión real que necesite un protocolo no HTTP (p. ej. LSP por TCP, bases de datos) |
| P2 | Actores por plugin (`isolated = true`) | ADR-008 | Mata la composabilidad como modo por defecto; dos modos de ejecución duplican la semántica de hooks | Ecosistema con plugins de terceros no confiables populares, o incidentes de estabilidad que watchdog+pcall no contengan |
| P3 | Plugins WASM (segunda capa) | ADR-002 | DX de autoría muy inferior a Lua; el sandboxing duro ya lo dan los workers con `caps` | Demanda real de plugins en otros lenguajes con aislamiento fuerte |
| P4 | Package manager de plugins | [filosofia.md](filosofia.md) / [arquitectura.md](arquitectura.md) | `git clone` en `plugins/` basta para arrancar (modelo vim-pathogen) | Dolor real de versionado/dependencias con un ecosistema de decenas de plugins |
| P5 | Embeddings y endpoints no-chat | [providers.md](providers.md) §5 | El harness no los necesita para funcionar; contrato aparte cuando exista consumidor | Una extensión de memoria/búsqueda semántica que los pida |
| P6 | Imágenes/archivos generados por el modelo | [providers.md](providers.md) §5 | Harness de código; render de imágenes en terminal es un melón propio | Modelos multimodales de salida relevantes para flujos de código + soporte estable en terminales objetivo (`nu.ui.caps().images`) |
| P7 | Cifrado en reposo / redacción de secretos en transcripts | [sesiones.md](sesiones.md) §8 | `0600` y disco del usuario; la fidelidad del transcript es una feature | Demanda de entornos regulados o un incidente real de fuga |
| P8 | Índice global de sesiones | [sesiones.md](sesiones.md) §7 | Listar directorios + leer línea `meta` es suficiente; un índice sería caché reconstruible | Listados perceptiblemente lentos (miles de sesiones por proyecto) |
| P9 | Sincronización de sesiones entre máquinas | [sesiones.md](sesiones.md) §8 | Construible encima: el formato JSONL es la API | Demanda; no requiere cambios del formato |
| P10 | Política de retención/GC de sesiones | [sesiones.md](sesiones.md) §8 | Es política configurable de la extensión del agente, no del formato | Diseño fino de la config de la extensión del agente |
| P11 | Workers anidados | [api.md](api.md) §13 | Simplicidad del árbol de supervisión (el principal posee todos los workers) | Un subagente-en-worker que necesite su propio paralelismo interno |
| P12 | Ejecución paralela de tool calls de un mismo turno | [agente.md](agente.md) §4 | Secuencial es más seguro (tools que editan ficheros) y más fácil de razonar; es lo que hacen los harnesses de referencia | Evidencia de turnos dominados por tools lentas e independientes (solo lectura) |
| P13 | Animación/repintado de alta frecuencia (>~30 fps) | [modelo-ejecucion.md](modelo-ejecucion.md) §limitaciones | Una TUI pinta por cambios; el coalescing de ~30 ms es deliberado | Probablemente nunca; si una extensión legítima lo necesita, discutir un canal de pintado directo |
| P14 | Splits / vista multi-sesión en `chat` | [chat.md](chat.md) §1 | Una columna y una sesión visible mantienen la v1 simple; los subagentes ya dan concurrencia sin UI dividida | Demanda real de supervisar varias sesiones/subagentes en vivo |
| P15 | Búsqueda dentro del transcript | [chat.md](chat.md) §10 | El transcript es JSONL legible; `grep` externo cubre el apuro | Sesiones largas donde scrollear duela de verdad |
| P16 | Modo vim en el editor de input | [chat.md](chat.md) §10 | El editor v1 es deliberadamente simple; un modo vim es un proyecto en sí | Demanda; implementable como plugin sobre la pila de input si el toolkit expone lo suficiente |
| P17 | Scoping de `caps` por rutas (`fs.read:/repo`) | [problemas.md](problemas.md) G6 | Contención de rutas correcta (symlinks, `..`, case-insensitivity) es un proyecto de seguridad en sí; hacerlo *casi* bien es peor que no tenerlo | Demanda real de contención por directorio + diseño de seguridad dedicado; la sintaxis ya le reserva sitio |
| P18 | Windows nativo (sin WSL2) | [problemas.md](problemas.md) G9 | v1 = Linux/macOS nativos + WSL2 en Windows: el contrato POSIX se cumple íntegro sin especificación condicional | Demanda real de usuarios Windows sin WSL2; implica shell portable, semántica de señales dual y pruebas de terminal nativas |
| P19 | Listener HTTP mínimo (`listen_once`) para callbacks OAuth | [problemas.md](problemas.md) G13 | Device flow / pegado de código cubren los providers conocidos sin abrir puertos | Un provider real que no ofrezca device flow ni pegado de código |
