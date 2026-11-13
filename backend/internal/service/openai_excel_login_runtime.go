package service

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/reauthruntime"
)

// Keep the runtime aligned with the reviewed upstream sources, independently
// of this fork's four-part version. Its GitHub release digest is verified.
const excelLoginRuntimeVersion = "2.21.1"

func (s *OpenAIOAuthService) configureExcelLoginWorker(cfg *config.Config) {
	grant := s.excelGrants
	if grant == nil {
		return
	}
	token := strings.TrimSpace(os.Getenv("OPENAI_REAUTH_WORKER_TOKEN"))
	if len(token) >= 32 {
		grant.loginWorkerToken = token
		return
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return
	}
	grant.loginWorkerToken = hex.EncodeToString(buf)
	root := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if root == "" {
		root = "data"
	}
	host := "127.0.0.1"
	port := 8080
	if cfg != nil {
		if cfg.Server.Port > 0 {
			port = cfg.Server.Port
		}
		if cfg.Server.Host != "" && cfg.Server.Host != "0.0.0.0" && cfg.Server.Host != "::" {
			host = cfg.Server.Host
		}
	}
	grant.loginWorker = reauthruntime.New(filepath.Join(root, "excel-login-worker"), excelLoginRuntimeVersion, "http://"+net.JoinHostPort(host, strconv.Itoa(port)), grant.loginWorkerToken)
}
func (s *OpenAIOAuthService) ExcelLoginWorkerAuthenticated(token string) bool {
	if s == nil || s.excelGrants == nil {
		return false
	}
	expected := s.excelGrants.loginWorkerToken
	return len(expected) >= 32 && token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}
func (s *OpenAIOAuthService) ExcelLoginRuntimeStatus() reauthruntime.Status {
	if s == nil || s.excelGrants == nil {
		return reauthruntime.Status{State: "unavailable", Reason: "not_configured"}
	}
	if s.excelGrants.loginWorker != nil {
		return s.excelGrants.loginWorker.Status()
	}
	// An external worker token does not establish that a worker is online.
	if len(s.excelGrants.loginWorkerToken) >= 32 {
		return reauthruntime.Status{Mode: "external", State: "unknown"}
	}
	return reauthruntime.Status{State: "unavailable", Reason: "not_configured"}
}
func (s *OpenAIOAuthService) ensureExcelLoginWorker() {
	if s.excelGrants.loginWorker != nil {
		s.excelGrants.loginWorker.Ensure()
	}
}
func (s *OpenAIOAuthService) StopExcelLoginWorker() {
	if s != nil && s.excelGrants != nil && s.excelGrants.loginWorker != nil {
		s.excelGrants.loginWorker.Stop()
	}
}
func (s *OpenAIOAuthService) excelLoginEnabled(ctx context.Context) bool {
	if s == nil || s.excelGrants == nil || s.excelGrants.settings == nil {
		return false
	}
	value, err := s.excelGrants.settings.GetValue(ctx, SettingKeyExcelBPSEnabled)
	return err == nil && value == "true"
}
func ValidateExcelLoginIdentity(stored, candidate map[string]any) error {
	info, err := excelOAuthTokenInfo(candidate)
	if err != nil {
		return err
	}
	return validateExcelIdentity(&Account{Credentials: stored}, info)
}
