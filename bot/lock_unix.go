//go:build !windows

package bot

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

// lockTimeout is the maximum time to wait for a file lock (L6).
const lockTimeout = 5 * time.Second

// lockFile acquires an exclusive advisory lock on the file.
// L6: Uses non-blocking mode with retry and timeout to prevent hanging.
func lockFile(fd uintptr) error {
	deadline := time.Now().Add(lockTimeout)
	for {
		err := syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("flock failed: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout after %s waiting for state file lock", lockTimeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func unlockFile(fd uintptr) error {
	return syscall.Flock(int(fd), syscall.LOCK_UN)
}
