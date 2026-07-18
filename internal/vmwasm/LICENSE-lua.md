# Licencia del intérprete embebido (enu.wasm)

`enu.wasm` contiene el código de **PUC-Lua 5.4.7** (https://www.lua.org)
compilado a WebAssembly, junto al shim del kernel (`shim/`).

Lua se distribuye bajo la **licencia MIT**:

> Copyright © 1994–2024 Lua.org, PUC-Rio.
>
> Permission is hereby granted, free of charge, to any person obtaining a copy
> of this software and associated documentation files (the "Software"), to deal
> in the Software without restriction, including without limitation the rights
> to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
> copies of the Software, and to permit persons to whom the Software is
> furnished to do so, subject to the following conditions:
>
> The above copyright notice and this permission notice shall be included in all
> copies or substantial portions of the Software.

La licencia MIT es compatible con la Apache-2.0 del proyecto (ADR-013). Las
fuentes de Lua NO se versionan en este repo (se clonan pineadas en `build.sh`);
lo que se comitea es el artefacto `enu.wasm`, reproducible desde esas fuentes y
el shim. El shim (`shim/`, copyright de Diego Barea) es Apache-2.0 como el resto
del proyecto.
