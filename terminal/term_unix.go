//go:build darwin || linux

package terminal

import (
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

type terminalState struct {
	termios syscall.Termios
}

func watchResize() (<-chan os.Signal, func()) {
	resizes := make(chan os.Signal, 1)
	signal.Notify(resizes, syscall.SIGWINCH)
	return resizes, func() { signal.Stop(resizes) }
}

func isTerminal(fd int) bool {
	_, err := getTermios(fd)
	return err == nil
}

func makeRaw(fd int) (*terminalState, error) {
	termios, err := getTermios(fd)
	if err != nil {
		return nil, err
	}
	oldState := &terminalState{termios: *termios}

	termios.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	termios.Oflag &^= syscall.OPOST
	termios.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	termios.Cflag &^= syscall.CSIZE | syscall.PARENB
	termios.Cflag |= syscall.CS8
	termios.Cc[syscall.VMIN] = 1
	termios.Cc[syscall.VTIME] = 0
	if err := setTermios(fd, termios); err != nil {
		return nil, err
	}

	return oldState, nil
}

func restoreTerminal(fd int, state *terminalState) error {
	if state == nil {
		return nil
	}
	return setTermios(fd, &state.termios)
}

func terminalSize(fd int) (int, int, error) {
	var size windowSize
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), ioctlGetWindowSize, uintptr(unsafe.Pointer(&size)))
	if errno != 0 {
		return 0, 0, errno
	}
	return int(size.columns), int(size.rows), nil
}

func getTermios(fd int) (*syscall.Termios, error) {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), ioctlGetTermios, uintptr(unsafe.Pointer(&termios)))
	if errno != 0 {
		return nil, errno
	}
	return &termios, nil
}

func setTermios(fd int, termios *syscall.Termios) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), ioctlSetTermios, uintptr(unsafe.Pointer(termios)))
	if errno != 0 {
		return errno
	}
	return nil
}

type windowSize struct {
	rows    uint16
	columns uint16
	x       uint16
	y       uint16
}
