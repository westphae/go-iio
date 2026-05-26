package sysfs

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// sysfsWriteTimeout bounds writes that can block in the driver when a prior
// kingfisher run left buffering enabled (buffer/enable stuck in kernel).
const sysfsWriteTimeout = 3 * time.Second

func writeFileTimeout(path string, data []byte, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() {
		done <- os.WriteFile(path, data, 0)
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("sysfs: write %s: timed out after %s", path, timeout)
	}
}

func readTrimmed(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n\x00 \t"), nil
}
