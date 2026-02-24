//go:build windows

package bot

func lockFile(fd uintptr) error {
	return nil // Windows uses mandatory locking via LockFileEx; skip for now
}

func unlockFile(fd uintptr) error {
	return nil
}
