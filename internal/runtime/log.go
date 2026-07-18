package runtime

import (
	"os"
	"path/filepath"
	"sync"
	"time"
)

// `enu.log` (api.md §15): las extensiones registran lo que pasa a un fichero en
// `data_dir`, **nunca a la pantalla** —la UI es competencia de las extensiones,
// y un kernel que escupe a stdout/stderr contaminaría la salida de `enu -e` y la
// TUI por igual. Cada línea anota el plugin de origen, para que en un log
// compartido se sepa quién habló.
//
// La superficie es deliberadamente mínima: cuatro niveles (`debug/info/warn/
// error`) con la misma firma `(fmt, ...)`, y `print` redefinido como alias de
// `info`. Sin niveles de filtrado, sin rotación (P20: un log de texto crece
// despacio; se reabre si aparecen logs de varios MB).

const logFileName = "enu.log"

// logLevel es la etiqueta textual que precede a cada línea. No hay umbral de
// filtrado en v1: los cuatro niveles escriben siempre; el nivel es solo una
// anotación para quien lea el fichero.
type logLevel string

const (
	levelDebug logLevel = "DEBUG"
	levelInfo  logLevel = "INFO"
	levelWarn  logLevel = "WARN"
	levelError logLevel = "ERROR"
)

// logger serializa las escrituras al fichero de log. El estado principal es
// single-threaded (ADR-004), pero `enu.log` es **[W]** (disponible en workers,
// §16): varios estados Lua de la misma proceso pueden compartir el fichero, así
// que el `mutex` no es decorativo. El fichero se abre **perezosamente** en la
// primera escritura: un `enu -e` que no loguea nada no crea ni el directorio ni
// el fichero.
type logger struct {
	mu   sync.Mutex
	path string           // <data_dir>/enu.log
	f    *os.File         // nil hasta la primera escritura
	now  func() time.Time // inyectable en tests para timestamps deterministas
}

// newLogger prepara un logger sobre `path` sin tocar el disco todavía.
func newLogger(path string) *logger {
	return &logger{path: path, now: time.Now}
}

// write añade una línea `<timestamp> <LEVEL> [<owner>] <message>` al fichero,
// abriéndolo (y creando su directorio) si es la primera vez. Es **best-effort**:
// si el disco falla, devuelve el error pero el llamante lo ignora —un fallo al
// loguear no debe tumbar el programa ni, menos aún, escupir a la pantalla (que
// es justo lo que `enu.log` promete no hacer).
func (lg *logger) write(level logLevel, owner, msg string) error {
	lg.mu.Lock()
	defer lg.mu.Unlock()

	if lg.f == nil {
		if err := os.MkdirAll(filepath.Dir(lg.path), 0o700); err != nil {
			return err
		}
		// 0600: el log puede contener fragmentos de prompts o rutas privadas;
		// es del usuario y de nadie más (coherente con los permisos de
		// data_dir/plugins en problemas.md G14).
		f, err := os.OpenFile(lg.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		lg.f = f
	}

	ts := lg.now().Format("2006-01-02T15:04:05.000Z07:00")
	line := ts + " " + padLevel(level) + " [" + owner + "] " + msg + "\n"
	_, err := lg.f.WriteString(line)
	return err
}

// close cierra el fichero si llegó a abrirse. Idempotente.
func (lg *logger) close() error {
	lg.mu.Lock()
	defer lg.mu.Unlock()
	if lg.f == nil {
		return nil
	}
	err := lg.f.Close()
	lg.f = nil
	return err
}

// padLevel alinea la etiqueta de nivel a 5 columnas (el ancho de "DEBUG"/
// "ERROR") para que las líneas queden tabuladas y el log sea legible a ojo.
func padLevel(level logLevel) string {
	s := string(level)
	for len(s) < 5 {
		s += " "
	}
	return s
}

// defaultDataDir calcula el `data_dir` por defecto donde vive el log (§14):
// `$XDG_DATA_HOME/enu` o `~/.local/share/enu`. S11 lo expone como
// `enu.config.data_dir()` (loader.go); aquí se usa además como destino del log. Si
// no hay `HOME`, cae a un subdirectorio de los temporales del sistema antes que
// fallar: loguear nunca debe ser la razón de que el runtime no arranque.
func defaultDataDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "enu")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "share", "enu")
	}
	return filepath.Join(os.TempDir(), "enu")
}

// defaultConfigDir calcula el `config.dir` por defecto (§14): `$XDG_CONFIG_HOME/enu`
// o `~/.config/enu`. De ahí cuelga el `init.lua` del usuario (el último del
// arranque canónico) y, en S12, `enu.toml`. Misma red de seguridad que el data_dir
// si no hay `HOME`.
func defaultConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "enu")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "enu")
	}
	return filepath.Join(os.TempDir(), "enu-config")
}
