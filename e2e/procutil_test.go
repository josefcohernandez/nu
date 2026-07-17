package e2e

// Helpers GENÉRICOS de sondeo del SO compartidos por varios tests del paquete
// (mcp, sessions, …): liveness de un PID por la señal 0 y esperas por deadline de
// un PID muerto o de un fichero que aparece. Viven en el arnés —no en el fichero
// de un plugin— porque son maquinaria de proceso/disco sin nada específico de un
// plugin; tenerlos aquí evita que un test dependa del `_test.go` de otro (antes
// `sessions` colgaba de `pidAlive` definido en `mcp_test.go`). Unix-only, como el
// resto de la suite (el PTY ya fija Unix).

import (
	"os"
	"syscall"
	"time"
)

// pidAlive comprueba, sin instrumentar `enu`, si un PID está vivo: la señal 0 de
// POSIX no envía nada pero valida la existencia del proceso (ESRCH si no existe).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// waitPidDead espera hasta `d` a que un PID deje de existir, sondeando. Absorbe la
// breve ventana entre que `enu` retorna y el SO cosecha al hijo huérfano.
func waitPidDead(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return !pidAlive(pid)
}

// waitFile espera hasta `d` a que aparezca un fichero (lo escribe un subproceso
// asíncrono, p. ej. el pidfile de un servidor MCP al arrancar).
func waitFile(path string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, err := os.Stat(path)
	return err == nil
}
