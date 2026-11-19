package reauthruntime

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

// The pinned Excel/BPS runtime stays immutable. Only Codex local-worker login
// is replaced by this content-addressed overlay embedded in the application.
//
//go:embed protocol/*.py protocol/reauth/*.py protocol/reauth/LICENSE protocol/reauth/NOTICE.md
var protocolSource embed.FS

var protocolFiles = []string{
	"protocol_worker.py", "codex_protocol.py", "reauth/__init__.py",
	"reauth/chatgpt_client.py", "reauth/http_client.py", "reauth/oauth_client.py",
	"reauth/sentinel_token.py", "reauth/totp.py", "reauth/utils.py",
	"reauth/LICENSE", "reauth/NOTICE.md",
}

func (m *Manager) prepareProtocolOverlay() (string, error) {
	sources := make(map[string][]byte, len(protocolFiles))
	hash := sha256.New()
	for _, name := range protocolFiles {
		data, err := protocolSource.ReadFile("protocol/" + name)
		if err != nil {
			return "", err
		}
		sources[name] = data
		_, _ = hash.Write([]byte(name + "\x00"))
		_, _ = hash.Write(data)
	}
	dir := filepath.Join(m.root, fmt.Sprintf("codex-protocol-%x", hash.Sum(nil)[:12]))
	if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
		return dir, nil
	}
	tmp, err := os.MkdirTemp(m.root, ".protocol-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	for _, name := range protocolFiles {
		path := filepath.Join(tmp, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, sources[name], 0600); err != nil {
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
