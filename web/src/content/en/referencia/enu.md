---
title: enu — root
description: Runtime version, API level, and capability detection with enu.has.
---

The root namespace exposes the runtime version and capability detection. It's
the first thing any plugin that wants to be portable touches.

## `enu.version` [W]

```
enu.version -> { major, minor, patch, api: integer }
```

Runtime version and **API level** of the core. `api` is the number that grows
with every addition to the sacred surface; use it to require a minimum, but
prefer [`enu.has`](#enuhas-w) to detect concrete capabilities.

```sh
enu -e 'return enu.json.encode(enu.version)'
```

```
{"api":2,"major":0,"minor":1,"patch":0}
```

```lua
-- Require a minimum API level.
assert(enu.version.api >= 2, "this plugin needs api >= 2")
```

## `enu.has` [W]

```
enu.has(cap: string) -> boolean
```

Capability detection for portable extensions. Returns whether a capability is
available in this runtime/environment. It covers both fine-grained traits
(`"ui.images"`, `"net.tcp"`) and **whole modules**: in headless mode `enu.ui`
doesn't exist, and `enu.has("ui")` is the correct way to know that — never
probe and catch the error.

```sh
enu -e 'return enu.has("ui")'
```

```
false
```

(In `enu -e` there's no TTY, so `enu.ui` doesn't exist and `enu.has("ui")` is
`false`.)

```lua
-- Degrade gracefully depending on the environment.
if enu.has("ui") then
  -- paint a region
else
  -- headless mode: text only, to stdout/log
end
```

:::tip[Capabilities, not versions]
`enu.has` is the recommended detection mechanism over comparing
`enu.version.api`. A capability can be absent because of the *environment*
(headless, terminal without image support), not just the API level.
:::
