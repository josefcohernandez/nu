#!/bin/sh
# Build reproducible de nu.wasm (migracion-vm.md M02): PUC-Lua OFICIAL (5.4.7,
# sin un solo parche) + el shim del kernel (shim/nu_shim.c), compilados a
# WebAssembly con el trampolín de desenrollado (shim/nu_unwind.h) en vez de
# setjmp/longjmp.
#
# Requisitos (Ubuntu): clang>=18 lld wasi-libc libclang-rt-18-dev-wasm32
#   apt install clang lld wasi-libc libclang-rt-18-dev-wasm32
#
# El blob nu.wasm SE COMITEA (DM1): CI y contribuidores no necesitan la
# toolchain. Un job de CI (ci.yml) reconstruye y compara el hash para que blob
# y fuentes no deriven. Las fuentes de Lua NO se versionan (MIT de terceros,
# CLAUDE.md): se clonan aquí de forma pineada.
set -e
cd "$(dirname "$0")"

CC="${CC:-clang}"
LUA_VERSION=5.4.7
LUA_DIR="lua-${LUA_VERSION}"

[ -d "$LUA_DIR" ] || git clone --depth 1 --branch "v${LUA_VERSION}" \
  https://github.com/lua/lua "$LUA_DIR"

# Fuentes de Lua menos los entrypoints (lua.c/luac.c) y las libs que el baseline
# NO abre (io/os/loadlib/init/debug): api.md §1.2 / sandbox.
LUA_SRC=$(ls "$LUA_DIR"/*.c \
  | grep -v -E "lua\.c|luac\.c|onelua|liolib|loslib|loadlib|linit|ldblib" \
  | tr '\n' ' ')

"$CC" --target=wasm32-wasi --sysroot=/usr \
  -I/usr/include/wasm32-wasi -L/usr/lib/wasm32-wasi \
  -O2 -mexec-model=reactor \
  -D_WASI_EMULATED_SIGNAL -D_WASI_EMULATED_PROCESS_CLOCKS \
  -include shim/nu_unwind.h -Ishim -I"$LUA_DIR" \
  $LUA_SRC shim/nu_shim.c -o nu.wasm \
  -lwasi-emulated-signal -lwasi-emulated-process-clocks \
  -Wl,--export=__stack_pointer -Wl,--export=malloc

echo "nu.wasm: $(wc -c < nu.wasm) bytes"
echo "sha256:  $(sha256sum nu.wasm | cut -d' ' -f1)"
