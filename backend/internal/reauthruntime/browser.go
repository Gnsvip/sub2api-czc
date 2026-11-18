package reauthruntime

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

// The downloaded Excel/BPS runtime stays immutable. This versioned overlay is
// shipped in the application binary, so Codex fixes reach the actual worker.
//
//go:embed browser/browser_worker.py browser/codex_browser.mjs
var browserSource embed.FS

func (m *Manager) prepareBrowserOverlay() (string, error) {
	names := []string{"browser_worker.py", "codex_browser.mjs"}
	sources := make(map[string][]byte, len(names))
	hash := sha256.New()
	for _, name := range names {
		data, err := browserSource.ReadFile("browser/" + name)
		if err != nil {
			return "", err
		}
		sources[name] = data
		_, _ = hash.Write(data)
	}
	dir := filepath.Join(m.root, fmt.Sprintf("codex-browser-%x", hash.Sum(nil)[:12]))
	if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
		return dir, nil
	}
	tmp, err := os.MkdirTemp(m.root, ".browser-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(tmp, name), sources[name], 0600); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, "ready"), hash.Sum(nil), 0600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		if _, otherErr := os.Stat(filepath.Join(dir, "ready")); otherErr != nil {
			return "", err
		}
	}
	return dir, nil
}
