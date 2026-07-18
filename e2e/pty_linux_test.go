//go:build linux

package e2e

// openPTY para linux: abre `/dev/ptmx`, desbloquea el esclavo (TIOCSPTLCK = 0), obtiene
// su número (TIOCGPTN) para componer `/dev/pts/N`, y lo abre. Equivalente a mano de
// `openpty(3)` sin CGO ni dependencia externa, sobre los helpers de golang.org/x/sys/unix.

import (
	"os"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

func openPTY() (master, slave *os.File, err error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	// Desbloquea el esclavo (unlockpt): TIOCSPTLCK con valor 0.
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		_ = m.Close()
		return nil, nil, err
	}
	// Número del esclavo (ptsname): TIOCGPTN.
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		_ = m.Close()
		return nil, nil, err
	}
	s, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = m.Close()
		return nil, nil, err
	}
	return m, s, nil
}
