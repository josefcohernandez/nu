#!/bin/sh
# Instalador de nu (ADR-015, G33): el camino "curl | sh y a trabajar" que promete
# filosofia.md §2. Descarga el binario estático de la última release ESTABLE, verifica
# su checksum sha256 y lo coloca en el PATH. Sin dependencias raras: POSIX sh + curl (o
# wget) + tar + sha256sum (o shasum). Sin red más allá de GitHub; no compila nada.
#
# No instala las extensiones oficiales: nu queda como runtime desnudo (ADR-010). Para
# activarlas, tras instalar: `nu --default-config` (sin TTY) o la pantalla de arranque
# con TTY. El instalador lo recuerda al terminar.
#
# Variables de entorno:
#   NU_INSTALL_DIR   Directorio de instalación (default: ~/.local/bin, o /usr/local/bin
#                    si tienes permiso de escritura ahí y ~/.local/bin no existe).
#   NU_VERSION       Versión a instalar, p. ej. "v0.1.0" (default: la última estable).
#
# Uso:
#   curl -fsSL https://raw.githubusercontent.com/dbareagimeno/nu/main/install.sh | sh
#   curl -fsSL .../install.sh | NU_INSTALL_DIR=/usr/local/bin sh

set -eu

REPO="dbareagimeno/nu"
RELEASES_API="https://api.github.com/repos/${REPO}/releases"

# --- utilidades ------------------------------------------------------------------

# err imprime a stderr y aborta. info imprime a stderr (no contamina un eventual pipe).
err()  { printf 'install: error: %s\n' "$1" >&2; exit 1; }
info() { printf 'install: %s\n' "$1" >&2; }

# have comprueba si un comando existe.
have() { command -v "$1" >/dev/null 2>&1; }

# fetch descarga $1 a stdout, con curl o wget (lo que haya). Sigue redirecciones.
fetch() {
	if have curl; then
		curl -fsSL "$1"
	elif have wget; then
		wget -qO- "$1"
	else
		err "necesito curl o wget para descargar"
	fi
}

# fetch_to descarga $1 al fichero $2.
fetch_to() {
	if have curl; then
		curl -fsSL -o "$2" "$1"
	elif have wget; then
		wget -qO "$2" "$1"
	else
		err "necesito curl o wget para descargar"
	fi
}

# --- detección de plataforma -----------------------------------------------------

detect_os() {
	os="$(uname -s)"
	case "$os" in
		Linux)  echo linux ;;
		Darwin) echo darwin ;;
		*) err "sistema no soportado: $os (nu v1 es Linux y macOS; en Windows usa WSL2, P18)" ;;
	esac
}

detect_arch() {
	arch="$(uname -m)"
	case "$arch" in
		x86_64|amd64)      echo amd64 ;;
		aarch64|arm64)     echo arm64 ;;
		*) err "arquitectura no soportada: $arch (nu v1 es amd64 y arm64)" ;;
	esac
}

# --- resolución de versión -------------------------------------------------------

# latest_stable_tag saca el tag de la última release NO prerelease vía la API de GitHub.
# Las prereleases (tags con sufijo -rc/-beta) se marcan `prerelease: true` en el release;
# se filtran sin depender de `jq` (parseo mínimo del JSON con grep/sed, suficiente para
# este campo). Si la API falla o no hay estable, se aborta con un mensaje accionable.
latest_stable_tag() {
	# Trae la lista de releases (la primera estable es la más reciente, ya ordenada).
	json="$(fetch "${RELEASES_API}?per_page=20")" || err "no pude consultar las releases de ${REPO}"
	# Recorre el JSON recordando el último `tag_name` visto; al primer `prerelease:false`
	# imprime ese tag (la API devuelve las releases de más reciente a más antigua, así que
	# la primera estable es la última estable). Parseo mínimo —sin `jq`—: extrae el valor
	# entrecomillado de `tag_name` con un match acotado, robusto a que los campos vengan en
	# líneas separadas (como hace la API real de GitHub).
	echo "$json" | awk '
		/"tag_name"/ {
			if (match($0, /"tag_name"[ \t]*:[ \t]*"[^"]*"/)) {
				s = substr($0, RSTART, RLENGTH)
				sub(/^"tag_name"[ \t]*:[ \t]*"/, "", s)
				sub(/"$/, "", s)
				tag = s
			}
		}
		/"prerelease"/ { if ($0 ~ /false/ && tag != "") { print tag; exit } }
	'
}

# --- instalación -----------------------------------------------------------------

choose_install_dir() {
	if [ -n "${NU_INSTALL_DIR:-}" ]; then
		echo "$NU_INSTALL_DIR"
		return
	fi
	# Preferencia: ~/.local/bin (no requiere sudo). Si no existe pero /usr/local/bin es
	# escribible, úsalo; si no, ~/.local/bin (que crearemos).
	if [ -d "${HOME}/.local/bin" ]; then
		echo "${HOME}/.local/bin"
	elif [ -w /usr/local/bin ]; then
		echo "/usr/local/bin"
	else
		echo "${HOME}/.local/bin"
	fi
}

verify_checksum() {
	# $1 = fichero .tar.gz, $2 = checksums.txt (formato `sha256  nombre`).
	file="$1"; sums="$2"
	name="$(basename "$file")"
	expected="$(awk -v n="$name" '$2 == n || $2 == "*"n { print $1 }' "$sums" | head -n1)"
	[ -n "$expected" ] || err "no encontré el checksum de ${name} en checksums.txt"

	if have sha256sum; then
		actual="$(sha256sum "$file" | awk '{print $1}')"
	elif have shasum; then
		actual="$(shasum -a 256 "$file" | awk '{print $1}')"
	else
		err "necesito sha256sum o shasum para verificar la integridad"
	fi

	[ "$actual" = "$expected" ] || err "checksum NO coincide para ${name} (esperado ${expected}, obtenido ${actual})"
	info "checksum verificado: ${name}"
}

main() {
	OS="$(detect_os)"
	ARCH="$(detect_arch)"

	VERSION="${NU_VERSION:-}"
	if [ -z "$VERSION" ]; then
		info "resolviendo la última release estable…"
		VERSION="$(latest_stable_tag)"
		[ -n "$VERSION" ] || err "no encontré ninguna release estable de ${REPO} (¿solo hay prereleases? fija NU_VERSION=vX.Y.Z)"
	fi
	# Normaliza: el nombre del artefacto usa la versión sin la 'v' inicial.
	VER_NOV="${VERSION#v}"

	NAME="nu-v${VER_NOV}-${OS}-${ARCH}"
	BASE="https://github.com/${REPO}/releases/download/${VERSION}"
	TARBALL_URL="${BASE}/${NAME}.tar.gz"
	SUMS_URL="${BASE}/checksums.txt"

	info "instalando nu ${VERSION} (${OS}/${ARCH})"

	# Directorio temporal autolimpiado.
	tmp="$(mktemp -d)"
	# shellcheck disable=SC2064
	trap "rm -rf '$tmp'" EXIT INT TERM

	info "descargando ${NAME}.tar.gz…"
	fetch_to "$TARBALL_URL" "${tmp}/${NAME}.tar.gz" || err "no pude descargar ${TARBALL_URL}"
	fetch_to "$SUMS_URL"    "${tmp}/checksums.txt"  || err "no pude descargar ${SUMS_URL}"

	verify_checksum "${tmp}/${NAME}.tar.gz" "${tmp}/checksums.txt"

	info "descomprimiendo…"
	tar -C "$tmp" -xzf "${tmp}/${NAME}.tar.gz" || err "no pude descomprimir el tar.gz"
	[ -f "${tmp}/nu" ] || err "el tar.gz no contiene el binario 'nu'"
	chmod +x "${tmp}/nu"

	DIR="$(choose_install_dir)"
	mkdir -p "$DIR" || err "no pude crear el directorio de instalación ${DIR}"

	# Instala con mv; si el destino no es escribible y hay sudo, reintenta con sudo.
	if mv "${tmp}/nu" "${DIR}/nu" 2>/dev/null; then
		:
	elif have sudo; then
		info "necesito permisos para escribir en ${DIR}; usando sudo…"
		sudo mv "${tmp}/nu" "${DIR}/nu" || err "no pude instalar en ${DIR} ni con sudo"
	else
		err "no puedo escribir en ${DIR} y no hay sudo; fija NU_INSTALL_DIR a un directorio escribible"
	fi

	info "instalado: ${DIR}/nu"

	# Aviso de PATH si el directorio elegido no está en él.
	case ":${PATH}:" in
		*":${DIR}:"*) : ;;
		*) info "ojo: ${DIR} no está en tu PATH; añádelo, p. ej.: export PATH=\"${DIR}:\$PATH\"" ;;
	esac

	# Mensaje final: comprobar + activar (a stdout, es el resultado útil del comando).
	printf '\n'
	printf 'nu %s instalado en %s/nu\n' "$VERSION" "$DIR"
	printf 'comprueba:  nu -e '"'"'return nu.version'"'"'\n'
	printf 'activa el agente y demás extensiones oficiales:  nu --default-config\n'
}

main "$@"
