# Modelo de ejecución: comunicación y orquestación

Vista dinámica del sistema: cómo fluyen eventos, IO y pintado entre el estado
Lua principal, las goroutines de Go y los workers. La vista estática está en
[arquitectura.md](arquitectura.md); las firmas, en [api.md](api.md).

## Topología

```mermaid
flowchart TB
    subgraph TERMINAL[Terminal]
        IN["Input: teclas / mouse / resize"]
        OUT[Pantalla]
    end

    subgraph KERNEL["Kernel Go (un solo proceso)"]
        Q["Cola de eventos<br/>(input + completions de IO + mensajes de workers + timers)"]

        subgraph MAIN["Estado Lua principal — UNA goroutine, turnos secuenciales"]
            H["Handlers síncronos<br/>(input, eventos, timers)<br/>no pueden suspender"]
            T["Tasks (corrutinas)<br/>IO secuencial con await implícito"]
        end

        WD["Watchdog<br/>presupuesto por slice (~100 ms)<br/>pcall en cada frontera de hook"]

        subgraph PRIM["Primitivas Go — goroutines, paralelas por dentro"]
            IOP["fs / proc / http / ws"]
            CPU["text / search<br/>(markdown, diff, grep, fuzzy)"]
        end

        COMP["Compositor<br/>diff de celdas + damage tracking<br/>coalescing ≤ ~30 ms"]

        subgraph WORKERS["Workers (opt-in) — un estado Lua por goroutine"]
            W1["worker 1"]
            WN["worker N"]
        end
    end

    EXT["Procesos externos<br/>(MCP, herramientas, shells)"]

    IN --> Q
    Q -->|"un turno cada vez"| MAIN
    H -->|"task.spawn"| T
    T -->|"llamada suspendiente ⏸"| IOP
    T -->|"llamada suspendiente ⏸"| CPU
    IOP -->|completion| Q
    CPU -->|completion| Q
    H & T -->|"ui.* (solo aquí)"| COMP
    COMP --> OUT
    WD -.->|supervisa y aborta| MAIN
    T <-->|"mensajes (COPIAS JSON-ables)"| W1
    T <-->|"mensajes (COPIAS JSON-ables)"| WN
    W1 & WN -->|"primitivas [W]"| IOP
    IOP <-->|"stdio / JSON-RPC"| EXT
```

Lectura clave: **todo lo que toca el estado Lua principal pasa por la cola y
se procesa por turnos, de uno en uno**. El paralelismo real vive en las
primitivas Go y en los workers; sus resultados vuelven a la cola como un
evento más. La pantalla tiene un único escritor (el estado principal) y un
único pintor (el compositor).

## Secuencia: el caso caliente (streaming de tokens del LLM)

```mermaid
sequenceDiagram
    participant P as Plugin (task Lua)
    participant L as Loop / cola
    participant N as Goroutine net (Go)
    participant TX as Primitiva text (Go)
    participant C as Compositor (Go)
    participant T as Terminal

    P->>N: nu.http.stream(...) ⏸ (la task se suspende)
    Note over L: el loop sigue atendiendo input y otros eventos
    N-->>L: cabeceras recibidas
    L->>P: reanuda la task con el Stream
    loop por cada evento SSE
        N-->>L: chunk (encolado)
        L->>P: reanuda el iterador Stream:events()
        P->>TX: nu.text.markdown(texto parcial)
        TX-->>P: Block
        P->>C: region:blit(Block)
        Note over C: marca damage; NO pinta aún
    end
    C-->>T: repinta lo dañado (coalescido, ≤ ~30 ms)
```

El plugin escribe código secuencial; la concurrencia (red, render, pintado,
input simultáneo) es invisible para él. Lua solo ejecuta los pasos baratos:
recibir el chunk ya parseado, pedir el Block, colocarlo.

## Limitaciones del modelo (aceptadas y conocidas)

1. **Un turno cada vez en el estado principal.** La latencia de input está
   acotada por el peor slice de Lua en cola. El watchdog corta un slice que
   exceda su presupuesto, pero no protege de la *muerte por mil cortes*
   (muchos handlers lentos pero bajo presupuesto) — ADR-008.
2. **La cancelación es cooperativa.** `Task:cancel()` solo surte efecto en el
   siguiente punto de suspensión. Un bucle de CPU puro en Lua no es
   cancelable: solo el watchdog lo aborta. El aborto no es capturable con
   `pcall`; los recursos se liberan con `nu.task.cleanup` (api.md §1.3).
3. **La frontera de workers solo cruza datos, nunca referencias.** Mensajes =
   valores JSON-ables copiados. No cruzan: closures, userdata ni **Blocks**.
   Consecuencia práctica: un worker no puede pre-renderizar UI; manda datos
   digeridos y el estado principal pide los Blocks y los coloca.
4. **Workers sin `nu.ui` ni `nu.events`.** Su único canal con el mundo es la
   mensajería con el padre. Diseño deliberado (un solo escritor de UI), pero
   significa que un worker no puede reaccionar a eventos del bus ni emitirlos
   directamente. La API del worker puede recortarse aún más al crearlo
   (`opts.caps`), hasta dejar solo los módulos concedidos.
5. **Memoria compartida dentro del estado principal.** Un memory leak de un
   plugin infla el proceso entero; no hay presupuesto de memoria por plugin
   en v1 (los actores aislados quedaron como evolución futura, ADR-008).
6. **Repintado coalescido a ~30 ms.** Suficiente para streaming y UI fluida;
   inadecuado para animaciones de alta frecuencia. Decisión consciente: una
   TUI pinta por cambios, no por frames.
7. **Backpressure por ritmo de consumo.** Los streams (SSE, stdout de
   procesos) se bufferizan en Go mientras Lua consume a su ritmo; el buffer
   tiene límite y al excederlo el stream falla (`EIO`). Un consumidor Lua
   lento no puede dejar el buffer creciendo sin cota.
8. **El rendimiento de Lua es el techo del camino caliente.** Todo lo que
   escale con el tamaño de la pantalla o del repo debe ser primitiva Go
   ("Lua decide, Go ejecuta"). Si una extensión necesita CPU en Lua, su
   herramienta es un worker — nunca el estado principal.
