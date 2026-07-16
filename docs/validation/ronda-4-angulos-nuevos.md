---
title: "Ronda 4: ángulos nuevos (verificación de completitud)"
type: "ronda"
id: "ronda-4"
zone: "ángulos nuevos (verificación de completitud)"
status: "cerrada"
scenarios: [18, 19, 20, 21, 22, 23]
---
# Ronda 4: ángulos nuevos (verificación de completitud)

Pregunta explícita: ¿estaba todo? Respuesta: no. Esta ronda ataca el bus
bajo reentrada, las fronteras de datos binarios, los providers
corporativos y de suscripción, el modelo de confianza del contenido del
repo, y el interior de los workers. Hallazgos G10-G16, sin resolver, a
[problemas.md](problemas.md).

## Escenario 18: el bus de eventos bajo reentrada

```lua
enu.events.on("agent:message", function(p)
  enu.events.emit("mi-plugin:resumen", digest(p))   -- emit DENTRO de un emit
end)
enu.events.on("agent:message", function(p)
  sub:cancel()                                     -- ¿y si cancela una sub
  otra = enu.events.on("agent:message", g)          --  o suscribe NUEVOS
end)                                               --  durante el despacho?
-- ¿El emit anidado despacha en profundidad (recursión) o se encola?
-- ¿Un handler recién suscrito ve el evento EN CURSO? ¿Y uno cancelado
-- a mitad? Todo indefinido — y es el tipo de indefinición que produce
-- bugs según el orden de carga de plugins.                          [G10]
```

## Escenario 19: bytes que no son texto

```lua
-- La tool bash hace cat de un PNG por error:
local r = enu.proc.run({ "cat", "logo.png" }, {})
return r.stdout   -- bytes arbitrarios → tool_result → tres fronteras JSON:
-- 1) enu.json.encode hacia el provider: JSON exige UTF-8 válido. ¿Lanza?
--    ¿Reemplaza? ¿Silencio?
-- 2) la entrada `message` del transcript JSONL: igual.
-- 3) un Worker:send con ese resultado: "JSON-able"... ¿lo es?
-- Sin regla, cada frontera improvisa y el bug aparece lejos del origen.
--                                                                   [G11]
```

## Escenario 20: el proxy corporativo que pusimos en la filosofía

```lua
-- providers.toml prometía "proxy corporativo" como caso estrella:
[providers.corp]
adapter  = "openai-compat"
base_url = "https://llm.interna.corp"   -- CA corporativa autofirmada
-- enu.http no tiene opciones TLS: ni ca_file, ni insecure, ni proxy
-- explícito (¿se respeta HTTPS_PROXY del entorno? sin especificar).
-- El caso anunciado no se puede configurar.                         [G12]
```

## Escenario 21: provider por suscripción (OAuth)

```lua
-- Un adaptador para un plan de suscripción (no API key): OAuth device flow
-- sí es escribible (http.request en bucle de polling + abrir URL con
-- enu.proc). Pero el flujo con callback localhost NO: no existe primitiva
-- de servidor/listener HTTP. ¿Y dónde guarda el adaptador el refresh
-- token? (¿plugins/<nombre>/? ¿en claro?) Sin convención.           [G13]
```

## Escenario 22: el repo malicioso (modelo de confianza)

```lua
-- enu se abre en un repo clonado de internet. El repo trae:
--   .enu/skills/inocente/SKILL.md   → se inyecta su índice en el system
--                                     prompt (agente §6-§7) SIN preguntar
--   .enu/agent.toml                 → ¡puede traer allow = ["bash:*"]!
--                                     (precedencia: proyecto > global)
-- Resultado: clonar un repo y abrir enu ya es ejecutar la voluntad del
-- repo. Mismo problema con descripciones de tools de servidores MCP de
-- terceros (texto no confiable inyectado al modelo). No hay modelo de
-- confianza: ni trust-on-first-use, ni qué config del repo se honra sin
-- preguntar.                                                        [G14]
```

## Escenario 23: dentro de un worker, ¿qué hay exactamente?

```lua
-- worker con task [W]: ¿el worker tiene su PROPIO scheduler/event loop?
enu.task.spawn(...)   -- ¿múltiples tasks dentro de un worker? ¿timers?
enu.task.race(...)    -- (el escenario 4 ya lo asumió para multiplexar
                     --  stream y cancelación... sin que estuviera escrito)
-- ¿Aplica watchdog dentro del worker? ¿Con qué presupuesto?        [G15]

-- Y dos subagentes paralelos editando el MISMO fichero vía proxy de
-- tools: las tools se intercalan en el principal pero nada coordina
-- escrituras al mismo path — last-write-wins silencioso.            [G16]
```

Menores anotados al pasar: rotación del fichero de `enu.log`
(→ [P20](pospuesto.md)); propiedad de los `Timer` (¿mueren con la task?
→ convención `cleanup`); restricciones de versión en `requires` (se
pliega a [P4](pospuesto.md) cuando se reabra).

---
