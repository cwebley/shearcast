//go:build !darwin && !linux

package state

import (
	"fmt"
	"os"
	"runtime"
)

func acquireLock(path string) (*os.File, error) {
	return nil, fmt.Errorf("state locking is not supported on %s; use Linux or macOS", runtime.GOOS)
}
