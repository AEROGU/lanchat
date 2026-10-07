//go:build !windows

package transfer

import (
	"os"
	"path/filepath"
)

func freeSpace(string) int64 { return -1 }

func markFromNetwork(string) error { return nil }

func defaultDownloadDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Downloads"), nil
}
