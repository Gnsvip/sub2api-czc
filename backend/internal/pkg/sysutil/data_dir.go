package sysutil

import "os"

// GetDataDir returns the data directory for storing config and lock files.
// Priority: DATA_DIR env > /app/data (if exists and writable) > current directory
func GetDataDir() string {
	// Check DATA_DIR environment variable first
	if dir := os.Getenv("DATA_DIR"); dir != "" {
		return dir
	}

	// Check if /app/data exists and is writable (Docker environment)
	dockerDataDir := "/app/data"
	if info, err := os.Stat(dockerDataDir); err == nil && info.IsDir() {
		// Try to check if writable by creating a temp file
		testFile := dockerDataDir + "/.write_test"
		if f, err := os.Create(testFile); err == nil {
			_ = f.Close()
			_ = os.Remove(testFile)
			return dockerDataDir
		}
	}

	// Default to current directory
	return "."
}
