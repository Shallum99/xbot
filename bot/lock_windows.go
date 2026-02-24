//go:build windows

package bot

import (
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

var (
	modkernel32      = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = modkernel32.NewProc("LockFileEx")
	procUnlockFileEx = modkernel32.NewProc("UnlockFileEx")
)

const (
	lockfileExclusiveLock   = 0x00000002
	lockfileFailImmediately = 0x00000001
)

// lockTimeout is the maximum time to wait for a file lock (L6).
const lockTimeout = 5 * time.Second

// lockFile acquires an exclusive lock on the file using Windows LockFileEx.
// M7: Implements proper Windows file locking instead of previous no-op stub.
// L6: Uses non-blocking mode with retry and timeout.
func lockFile(fd uintptr) error {
	deadline := time.Now().Add(lockTimeout)
	for {
		ol := new(syscall.Overlapped)
		r1, _, err := procLockFileEx.Call(
			fd,
			lockfileExclusiveLock|lockfileFailImmediately,
			0,
			1, 0,
			uintptr(unsafe.Pointer(ol)),
		)
		// Issue #13: Prevent GC from collecting Overlapped before syscall completes
		runtime.KeepAlive(ol)
		if r1 != 0 {
			return nil // Success
		}
		// ERROR_LOCK_VIOLATION = 33
		if errno, ok := err.(syscall.Errno); ok && errno != 33 {
			return fmt.Errorf("LockFileEx failed: %v", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout after %s waiting for state file lock", lockTimeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// unlockFile releases the exclusive lock on the file.
func unlockFile(fd uintptr) error {
	ol := new(syscall.Overlapped)
	r1, _, err := procUnlockFileEx.Call(
		fd,
		0,
		1, 0,
		uintptr(unsafe.Pointer(ol)),
	)
	runtime.KeepAlive(ol)
	if r1 == 0 {
		return fmt.Errorf("UnlockFileEx failed: %v", err)
	}
	return nil
}
