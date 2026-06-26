# Ejemplos de `nu`

## `nu/` — config de ejemplo con una TUI de demostración

`examples/nu/` es un `config.dir()` de ejemplo (un `nu.toml` + un plugin de disco) que
arranca una **TUI funcional escrita en Lua** sobre la API pública del core (`nu.ui`,
`nu.events`, `nu.task`). Demuestra el driver de TTY: una extensión Lua que pinta y
responde en un terminal de verdad —regiones, blocks estilizados, teclado, reloj en vivo
y `ui:resize`— sin una sola línea de Go de producto.

### Cómo correrlo

Desde la raíz del repositorio:

```sh
XDG_CONFIG_HOME=examples go run .
# o, con el binario ya instalado (install.sh):
XDG_CONFIG_HOME=examples nu
```

`XDG_CONFIG_HOME=examples` hace que `config.dir()` resuelva a `examples/nu`, donde vive
el `nu.toml` que activa el plugin `tui-demo` (`examples/nu/plugins/tui-demo`).

### Qué muestra

- Un marco a pantalla completa con título, pares etiqueta/valor y un pie de ayuda.
- **↑/↓** o **j/k**: mueven un contador.
- **escribir**: rellena un campo de texto (con el cursor real colocado al final).
- **Backspace**: borra; **Enter**: confirma.
- Un **reloj** que repinta cada segundo (`nu.task.every`): la UI reacciona a fuentes
  asíncronas, no solo al teclado.
- **resize**: redibuja al nuevo tamaño del terminal (`ui:resize`).
- **q** o **Ctrl+C**: salen (emiten `core:shutdown`, que el driver convierte en un
  apagado ordenado que restaura el terminal).

El código está comentado en
[`nu/plugins/tui-demo/init.lua`](nu/plugins/tui-demo/init.lua); usa a propósito la API de
bajo nivel del core (no el toolkit) para que se vea qué primitivas bastan para una UI.
