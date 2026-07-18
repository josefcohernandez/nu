//go:build darwin

package e2e

// openPTY para darwin: abre el multiplexor `/dev/ptmx`, resuelve el nombre del esclavo
// con el ioctl TIOCPTYGNAME, concede (TIOCPTYGRANT) y desbloquea (TIOCPTYUNLK) el par, y
// abre el esclavo. Es el equivalente a mano de `openpty(3)`/`posix_openpt` sin CGO ni
// una dependencia externa (mismo camino que usa creack/pty por dentro), sobre los ioctl
// que golang.org/x/sys/unix ya expone en darwin.

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func openPTY() (master, slave *os.File, err error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	name, err := ptsname(m.Fd())
	if err != nil {
		_ = m.Close()
		return nil, nil, err
	}
	if err := ioctlNoArg(m.Fd(), unix.TIOCPTYGRANT); err != nil {
		_ = m.Close()
		return nil, nil, err
	}
	if err := ioctlNoArg(m.Fd(), unix.TIOCPTYUNLK); err != nil {
		_ = m.Close()
		return nil, nil, err
	}
	s, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = m.Close()
		return nil, nil, err
	}
	return m, s, nil
}

// ptsname devuelve el nombre del esclavo del par (TIOCPTYGNAME llena un buffer con la
// ruta terminada en NUL).
func ptsname(fd uintptr) (string, error) {
	var buf [128]byte
	if err := ioctlPtr(fd, unix.TIOCPTYGNAME, unsafe.Pointer(&buf[0])); err != nil {
		return "", err
	}
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i]), nil
		}
	}
	return string(buf[:]), nil
}

// ioctlNoArg lanza un ioctl sin argumento (grant/unlock).
func ioctlNoArg(fd uintptr, req uint) error {
	_, _, e := unix.Syscall(unix.SYS_IOCTL, fd, uintptr(req), 0)
	if e != 0 {
		return e
	}
	return nil
}

// ioctlPtr lanza un ioctl cuyo argumento es un puntero (TIOCPTYGNAME sobre el buffer).
func ioctlPtr(fd uintptr, req uint, ptr unsafe.Pointer) error {
	_, _, e := unix.Syscall(unix.SYS_IOCTL, fd, uintptr(req), uintptr(ptr))
	if e != 0 {
		return e
	}
	return nil
}
