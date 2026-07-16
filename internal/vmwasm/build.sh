#!/bin/sh
# Build hermético y reproducible de enu.wasm (migracion-vm.md M02 + fix
# post-M17): PUC-Lua OFICIAL (5.4.7, sin un solo parche) + el shim del kernel
# (shim/enu_shim.c), compilados a WebAssembly con el trampolín de desenrollado
# (shim/enu_unwind.h) en vez de setjmp/longjmp.
#
# Toolchain HERMÉTICA: el script descarga el wasi-sdk oficial (versión y
# sha256 pineados abajo) y compila con SU clang y SU sysroot — nada de la
# distro entra en el binario. Esto es lo que hace el blob reproducible
# byte-a-byte entre máquinas y runners de CI: con la toolchain de apt, el
# archivo de la distro cambiaba wasi-libc/clang bajo los pies y el rebuild
# dejaba de coincidir con el blob comiteado (visto dos veces en la PR de la
# migración; diagnóstico en la bitácora post-M17 de migracion-vm.md).
#
# El blob enu.wasm SE COMITEA (DM1): CI y contribuidores no necesitan la
# toolchain. Un job de CI (ci.yml, vmblob) reconstruye y compara el hash para
# que blob y fuentes no deriven. Las fuentes de Lua NO se versionan (MIT de
# terceros, CLAUDE.md): se clonan aquí pineadas por tag Y por commit.
set -e
cd "$(dirname "$0")"

WASI_SDK_TAG=wasi-sdk-33
WASI_SDK_VERSION=33.0
LUA_VERSION=5.4.7
LUA_COMMIT=1ab3208a1fceb12fca8f24ba57d6e13c5bff15e3

# sha256 de los tarballs oficiales de la release wasi-sdk-33 (una entrada por
# plataforma soportada). Si se sube de versión, se repinean TODOS y se
# regenera el blob en el mismo cambio.
case "$(uname -s)-$(uname -m)" in
  Linux-x86_64)
    WASI_SDK_PLAT=x86_64-linux
    WASI_SDK_SHA256=0ba8b5bfaeb2adf3f29bab5841d76cf5318ab8e1642ea195f88baba1abd47bce ;;
  Linux-aarch64|Linux-arm64)
    WASI_SDK_PLAT=arm64-linux
    WASI_SDK_SHA256=4f98ee738c7abb45c81a94d1461fc53cc569d1cd01498951c8184d841a027844 ;;
  Darwin-arm64)
    WASI_SDK_PLAT=arm64-macos
    WASI_SDK_SHA256=85c997a2665ead91673b5bb88b7d0df3fc8900df3bfa244f720d478187bbdc78 ;;
  *)
    echo "build.sh: plataforma sin pin de wasi-sdk: $(uname -s)-$(uname -m)" >&2
    echo "  añade su tarball y sha256 al case de arriba (release ${WASI_SDK_TAG})" >&2
    exit 1 ;;
esac

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

SDK_DIR="wasi-sdk-${WASI_SDK_VERSION}-${WASI_SDK_PLAT}"
if [ ! -x "${SDK_DIR}/bin/clang" ]; then
  tarball="${SDK_DIR}.tar.gz"
  curl -fsSL -o "$tarball" \
    "https://github.com/WebAssembly/wasi-sdk/releases/download/${WASI_SDK_TAG}/${tarball}"
  got=$(sha256 "$tarball")
  if [ "$got" != "$WASI_SDK_SHA256" ]; then
    echo "build.sh: sha256 de ${tarball} NO coincide con el pin" >&2
    echo "  esperado: ${WASI_SDK_SHA256}" >&2
    echo "  obtenido: ${got}" >&2
    exit 1
  fi
  tar xzf "$tarball"
  rm "$tarball"
fi

# clang del wasi-sdk: target y sysroot propios (bin/clang.cfg), sin flags de
# la distro. wasip1 ES WASI preview1, los mismos imports que consume wazero.
# --strip-debug en el linker: las libs PRECOMPILADAS del sysroot traen
# secciones .debug_* con rutas del build de CADA tarball del SDK (macOS vs
# linux difieren SOLO ahí; el código es byte-idéntico) — se strippean para que
# el blob sea idéntico desde cualquier host, y de paso adelgaza ~325 KB.
CC="${SDK_DIR}/bin/clang"

LUA_DIR="lua-${LUA_VERSION}"
[ -d "$LUA_DIR" ] || git clone --depth 1 --branch "v${LUA_VERSION}" \
  https://github.com/lua/lua "$LUA_DIR"
got=$(git -C "$LUA_DIR" rev-parse HEAD)
if [ "$got" != "$LUA_COMMIT" ]; then
  echo "build.sh: el checkout de Lua ${LUA_VERSION} no es el commit pineado" >&2
  echo "  esperado: ${LUA_COMMIT}" >&2
  echo "  obtenido: ${got}" >&2
  exit 1
fi

# Fuentes de Lua menos los entrypoints (lua.c/luac.c) y las libs que el baseline
# NO abre (io/os/loadlib/init/debug): api.md §1.2 / sandbox.
LUA_SRC=$(ls "$LUA_DIR"/*.c \
  | grep -v -E "lua\.c|luac\.c|onelua|liolib|loslib|loadlib|linit|ldblib" \
  | tr '\n' ' ')

"$CC" --target=wasm32-wasip1 \
  -O2 -mexec-model=reactor \
  -D_WASI_EMULATED_SIGNAL -D_WASI_EMULATED_PROCESS_CLOCKS \
  -include shim/enu_unwind.h -Ishim -I"$LUA_DIR" \
  $LUA_SRC shim/enu_shim.c -o enu.wasm \
  -lwasi-emulated-signal -lwasi-emulated-process-clocks \
  -Wl,--export=__stack_pointer -Wl,--export=malloc \
  -Wl,--strip-debug

echo "enu.wasm: $(wc -c < enu.wasm) bytes"
echo "sha256:  $(sha256 enu.wasm)"
