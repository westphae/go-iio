package sysfs

import (
	"os"
	"strings"
)

func readTrimmed(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n\x00 \t"), nil
}
