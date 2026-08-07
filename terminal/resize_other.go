//go:build !darwin && !linux

package terminal

import "os"

func watchResize() (<-chan os.Signal, func()) {
	return nil, func() {}
}
