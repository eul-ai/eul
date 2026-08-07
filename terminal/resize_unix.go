//go:build darwin || linux

package terminal

import (
	"os"
	"os/signal"
	"syscall"
)

func watchResize() (<-chan os.Signal, func()) {
	resizes := make(chan os.Signal, 1)
	signal.Notify(resizes, syscall.SIGWINCH)
	return resizes, func() { signal.Stop(resizes) }
}
