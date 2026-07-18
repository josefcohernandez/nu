---
title: API conventions
description: How to read the reference — signature notation, ⏸ and [W] markers, structured errors, units and common types.
---

The reference documents the **core API v1**: the "sacred surface". Everything that
lives under the `enu` global and only grows by addition. What is **not** here
(widget toolkit, agent, chat, MCP, providers) is an extension and is versioned
separately.

## Signature notation

Signatures use the notation `enu.mod.fn(arg: type, opts?: table) -> type`:

- `arg: type` — required argument and its type.
- `opts?: table` — the `?` marks it as optional.
- `-> type` — the return value.

## Markers

| Marker | Meaning |
|---|---|
| **⏸** | **Suspends**: the function can only be called **inside a task**; it yields control until it completes and returns the result directly (implicit await). Calling it outside a task throws `EINVAL`. |
| **[W]** | Available inside **workers**. Without the mark, the function is main-state only. |

Remember: the `enu -e` chunk runs on the main state, **not** inside a task,
so to test ⏸ functions you wrap them in `enu.task.spawn(function() ...
end)`. See [Your first script](/enu/en/docs/primer-script/).

## The `enu` namespace

The whole API lives under the `enu` global, with submodules. `require` is reserved
for plugin modules and pure Lua libraries. Identifiers are in
**English** and `snake_case`.

### Lua environment baseline

Lua 5.1 (gopher-lua). Available: `string`, `table`, `math`, `coroutine`,
`pairs`/`ipairs`/`pcall`/`error`/… **Disabled**: `io`, `os.execute`,
`os.exit`, `os.remove`, `os.rename`, `os.getenv`, `dofile`/`loadfile` outside the
loader. And `print` is **redirected to `enu.log.info`** (it goes to the log, not the
screen). Reason: all IO must go through the core's async primitives; the stdlib's
blocking IO would freeze the event loop.

## Errors

Core functions **throw** (via `error()`) structured tables, instead of
returning `(value, err)`:

```lua
{ code = "ENOENT", message = "...", detail = nil }  -- detail is optional
```

They're caught with `pcall`. Always branch on `code` (stable, part of the
contract), never on `message`.

```lua
local ok, err = pcall(function() return enu.fs.read(ruta) end)
if not ok then
  if err.code == "ENOENT" then
    -- the file doesn't exist: create a default one
  else
    error(err)  -- re-throw what you don't know how to handle
  end
end
```

### Reserved v1 codes

`ENOENT`, `EEXIST`, `EACCES`, `EIO`, `EHTTP`, `ENET`, `ETIMEOUT`, `ECANCELED`,
`EBUDGET`, `EINVAL`, `ECLOSED`.

Two are special: **`ECANCELED`** (cancellation) and **`EBUDGET`** (watchdog)
name **uncatchable** aborts —they unwind the stack without going through `pcall`— and
only serve to *observe* them, e.g. in the result of `Task:await`.

Extensions coin their own codes with the same shape, outside this
list (e.g. `EPROVIDER`, `EAGENT`).

## Units and common types

- **Times in milliseconds.** Every function with IO accepts `opts.timeout_ms`
  (throws `ETIMEOUT`).
- **Paths as UTF-8 strings.**
- The core's **handles** (Task, Region, Proc, Worker…) are opaque userdata with
  methods (called with `:`, e.g. `task:await()`).

## Stability

Freezing v1 = freezing signatures and semantics: they only change **by addition**, and
every addition bumps `enu.version.api`. Code written against one level remains
valid on the following ones. Detect capabilities with
[`enu.has()`](/enu/en/api/enu/), never by comparing versions.
