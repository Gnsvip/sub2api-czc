package service

// Excel-only integration of ranxi2001/sub2api's password/TOTP worker contract.
// Account snapshots, queue ownership and grant writes remain transaction guarded.
import (
	"context"
	"encoding/base32"
	"encoding/json"
	"errors"
	"net/mail"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/reauthruntime"
)

type ExcelLoginConfig struct {
	AccountID          int64  `json:"account_id"`
	Enabled            bool   `json:"enabled"`
	LoginEmail         string `json:"login_email"`
	PasswordConfigured bool   `json:"password_configured"`
	TOTPConfigured     bool   `json:"totp_configured"`
	Revision           int64  `json:"revision"`
	PasswordCiphertext string `json:"-"`
	TOTPCiphertext     string `json:"-"`
}
type ExcelLoginInput struct {
	Enabled    bool   `json:"enabled"`
	LoginEmail string `json:"login_email"`
	Password   string `json:"password"`
	TOTPSecret string `json:"totp_secret"`
	ClearTOTP  bool   `json:"clear_totp"`
	Revision   int64  `json:"revision"`
}
type ExcelLoginTask struct {
	ID             int64     `json:"id"`
	AccountID      int64     `json:"account_id"`
	Status         string    `json:"status"`
	Stage          string    `json:"stage"`
	ErrorCode      string    `json:"error_code,omitempty"`
	WorkerID       string    `json:"-"`
	ConfigRevision int64     `json:"-"`
	UpdatedAt      time.Time `json:"updated_at"`
}
type ExcelLoginRepository interface {
	GetExcelLoginConfig(context.Context, int64) (*ExcelLoginConfig, error)
	SaveExcelLoginConfig(context.Context, *ExcelLoginConfig, int64) (bool, error)
	GetExcelLoginTask(context.Context, int64) (*ExcelLoginTask, error)
	QueueExcelLogin(context.Context, int64, bool) (*ExcelLoginTask, error)
	ListMissingExcelLogins(context.Context) ([]int64, error)
	HasActiveExcelLogins(context.Context) (bool, error)
	PrepareMissingExcelRoutes(context.Context) error
	ClaimExcelLogin(context.Context, string) (*ExcelLoginTask, error)
	ProgressExcelLogin(context.Context, int64, string, string) (bool, error)
	FailExcelLogin(context.Context, int64, string, string) (bool, error)
	FinishExcelLogin(context.Context, int64, string, map[string]any, string, ExcelBPSCredentialState) (bool, error)
}
type ExcelLoginStatus struct {
	Config  *ExcelLoginConfig    `json:"config"`
	Task    *ExcelLoginTask      `json:"task"`
	Runtime reauthruntime.Status `json:"runtime"`
}

// Secret fields exist only on the authenticated internal worker response.
type ExcelLoginClaim struct {
	OAuthProfile      string `json:"oauth_profile"`
	ExpectedWorkspace string `json:"expected_workspace"`
	TaskID            int64  `json:"task_id"`
	AccountID         int64  `json:"account_id"`
	LoginEmail        string `json:"login_email"`
	CredentialMode    string `json:"credential_mode"`
	Engine            string `json:"engine"`
	Password          string `json:"password"`
	TOTPSecret        string `json:"totp_secret,omitempty"`
	ProxyURL          string `json:"proxy_url,omitempty"`
}

func (s *OpenAIOAuthService) excelLoginRepo() (ExcelLoginRepository, error) {
	if s == nil || s.excelGrants == nil || !s.excelGrants.encryptionKeyConfigured {
		return nil, infraerrors.ServiceUnavailable("EXCEL_LOGIN_UNAVAILABLE", "Configure a persistent encryption key before automatic Excel authorization")
	}
	r, ok := s.excelGrants.repo.(ExcelLoginRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("EXCEL_LOGIN_UNAVAILABLE", "Excel authorization queue is unavailable")
	}
	return r, nil
}
func (s *OpenAIOAuthService) ExcelLoginStatus(ctx context.Context, id int64) (*ExcelLoginStatus, error) {
	if _, err := s.excelAuthorizationAccount(ctx, id); err != nil {
		return nil, err
	}
	r, err := s.excelLoginRepo()
	if err != nil {
		return nil, err
	}
	cfg, err := r.GetExcelLoginConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = &ExcelLoginConfig{AccountID: id}
	}
	cfg.PasswordConfigured = cfg.PasswordCiphertext != ""
	cfg.TOTPConfigured = cfg.TOTPCiphertext != ""
	task, err := r.GetExcelLoginTask(ctx, id)
	if err != nil {
		return nil, err
	}
	return &ExcelLoginStatus{Config: cfg, Task: task, Runtime: s.ExcelLoginRuntimeStatus()}, nil
}
func (s *OpenAIOAuthService) SaveExcelLogin(ctx context.Context, id int64, input ExcelLoginInput) (*ExcelLoginStatus, error) {
	account, err := s.excelAuthorizationAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	r, err := s.excelLoginRepo()
	if err != nil {
		return nil, err
	}
	old, err := r.GetExcelLoginConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	cfg := &ExcelLoginConfig{AccountID: id, Enabled: input.Enabled, LoginEmail: strings.TrimSpace(input.LoginEmail)}
	if old != nil {
		*cfg = *old
		cfg.Enabled = input.Enabled
		if input.LoginEmail != "" {
			cfg.LoginEmail = strings.TrimSpace(input.LoginEmail)
		}
	}
	if cfg.LoginEmail == "" {
		cfg.LoginEmail = account.GetCredential("email")
	}
	address, e := mail.ParseAddress(cfg.LoginEmail)
	if e != nil || address.Address != cfg.LoginEmail || len(cfg.LoginEmail) > 254 {
		return nil, infraerrors.BadRequest("EXCEL_LOGIN_EMAIL_INVALID", "Enter a valid login email")
	}
	if email := account.GetCredential("email"); email != "" && !strings.EqualFold(email, cfg.LoginEmail) {
		return nil, infraerrors.BadRequest("EXCEL_LOGIN_IDENTITY_MISMATCH", "Use this account's login email")
	}
	if len(input.Password) > 4096 || len(input.TOTPSecret) > 256 {
		return nil, infraerrors.BadRequest("EXCEL_LOGIN_SECRET_INVALID", "Login secret is too long")
	}
	if input.Password != "" {
		cfg.PasswordCiphertext, err = s.excelGrants.encryptor.Encrypt(input.Password)
		if err != nil {
			return nil, errors.New("cannot encrypt login password")
		}
	}
	if input.ClearTOTP {
		cfg.TOTPCiphertext = ""
	}
	if input.TOTPSecret != "" {
		secret := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(input.TOTPSecret), " ", ""))
		decoded, e := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.TrimRight(secret, "="))
		if e != nil || len(decoded) < 10 || len(decoded) > 64 {
			return nil, infraerrors.BadRequest("EXCEL_LOGIN_TOTP_INVALID", "Enter the Base32 TOTP secret, not a one-time code")
		}
		cfg.TOTPCiphertext, err = s.excelGrants.encryptor.Encrypt(secret)
		if err != nil {
			return nil, errors.New("cannot encrypt TOTP secret")
		}
	}
	if cfg.Enabled && cfg.PasswordCiphertext == "" {
		return nil, infraerrors.BadRequest("EXCEL_LOGIN_PASSWORD_REQUIRED", "Save a login password before enabling automatic authorization")
	}
	changed, err := r.SaveExcelLoginConfig(ctx, cfg, input.Revision)
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, infraerrors.Conflict("EXCEL_LOGIN_CONFIG_CHANGED", "Login settings changed; reload before saving")
	}
	return s.ExcelLoginStatus(ctx, id)
}
func (s *OpenAIOAuthService) QueueExcelLogin(ctx context.Context, id int64) (*ExcelLoginTask, error) {
	if _, err := s.excelAuthorizationAccount(ctx, id); err != nil {
		return nil, err
	}
	if !s.excelLoginEnabled(ctx) {
		return nil, infraerrors.BadRequest("EXCEL_BPS_DISABLED", "Enable the BPS system switch first")
	}
	r, err := s.excelLoginRepo()
	if err != nil {
		return nil, err
	}
	task, err := r.QueueExcelLogin(ctx, id, true)
	if err == nil && task != nil {
		s.ensureExcelLoginWorker()
	}
	if err == nil && task == nil {
		return nil, infraerrors.BadRequest("EXCEL_LOGIN_NOT_CONFIGURED", "Save and enable automatic authorization first, or wait for the previous attempt cooldown")
	}
	return task, err
}
func (s *OpenAIOAuthService) SyncMissingExcelLogins(ctx context.Context) error {
	if !s.excelLoginEnabled(ctx) {
		return nil
	}
	r, err := s.excelLoginRepo()
	if err != nil {
		return nil
	}
	if err = r.PrepareMissingExcelRoutes(ctx); err != nil {
		return err
	}
	active, err := r.HasActiveExcelLogins(ctx)
	if err != nil {
		return err
	}
	ids, err := r.ListMissingExcelLogins(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err = r.QueueExcelLogin(ctx, id, false); err != nil {
			return err
		}
	}
	if active || len(ids) > 0 {
		s.ensureExcelLoginWorker()
	}
	return nil
}
func (s *OpenAIOAuthService) ClaimExcelLogin(ctx context.Context, worker string) (*ExcelLoginClaim, error) {
	if !s.excelLoginEnabled(ctx) {
		return nil, nil
	}
	worker = strings.TrimSpace(worker)
	if worker == "" || len(worker) > 128 {
		return nil, infraerrors.BadRequest("EXCEL_LOGIN_WORKER_INVALID", "Invalid worker ID")
	}
	r, err := s.excelLoginRepo()
	if err != nil {
		return nil, err
	}
	task, err := r.ClaimExcelLogin(ctx, worker)
	if err != nil || task == nil {
		return nil, err
	}
	fail := func(code string) (*ExcelLoginClaim, error) {
		_, e := r.FailExcelLogin(ctx, task.ID, worker, code)
		return nil, e
	}
	account, err := s.excelAuthorizationAccount(ctx, task.AccountID)
	if err != nil {
		return fail("account_changed")
	}
	cfg, err := r.GetExcelLoginConfig(ctx, task.AccountID)
	if err != nil || cfg == nil || !cfg.Enabled || cfg.Revision != task.ConfigRevision {
		return fail("config_changed")
	}
	password, err := s.excelGrants.encryptor.Decrypt(cfg.PasswordCiphertext)
	if err != nil || password == "" {
		return fail("credentials_unavailable")
	}
	secret := ""
	if cfg.TOTPCiphertext != "" {
		secret, err = s.excelGrants.encryptor.Decrypt(cfg.TOTPCiphertext)
		if err != nil {
			return fail("credentials_unavailable")
		}
	}
	proxy, err := s.excelGrants.excelProxyURL(ctx, account.ProxyID)
	if err != nil {
		return fail("proxy_unavailable")
	}
	return &ExcelLoginClaim{OAuthProfile: "excel", ExpectedWorkspace: account.GetCredential("chatgpt_account_id"), TaskID: task.ID, AccountID: account.ID, LoginEmail: cfg.LoginEmail, CredentialMode: "password_totp", Engine: "local_worker", Password: password, TOTPSecret: secret, ProxyURL: proxy}, nil
}
func (s *OpenAIOAuthService) CompleteExcelLogin(ctx context.Context, id int64, worker string, credentials map[string]any) (*ExcelLoginTask, error) {
	r, err := s.excelLoginRepo()
	if err != nil {
		return nil, err
	}
	info, err := excelOAuthTokenInfo(credentials)
	if err != nil {
		_, _ = r.FailExcelLogin(ctx, id, worker, "identity_mismatch")
		return nil, infraerrors.BadRequest("EXCEL_LOGIN_CREDENTIALS_INVALID", "Invalid Excel authorization identity or OAuth client")
	}
	// Only verified token fields enter encrypted storage. Worker metadata is ignored.
	safe := s.BuildAccountCredentials(info)
	raw, err := json.Marshal(safe)
	if err != nil {
		return nil, err
	}
	cipher, err := s.excelGrants.encryptor.Encrypt(string(raw))
	if err != nil {
		return nil, errors.New("cannot encrypt Excel grant")
	}
	changed, err := r.FinishExcelLogin(ctx, id, worker, safe, cipher, ExcelBPSGrantState(info.ExpiresAt))
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, infraerrors.Conflict("EXCEL_LOGIN_TASK_CHANGED", "Account, authorization, task or login settings changed; stale result discarded")
	}
	return &ExcelLoginTask{ID: id, Status: "succeeded", Stage: "succeeded"}, nil
}
func (s *OpenAIOAuthService) ProgressExcelLogin(ctx context.Context, id int64, worker, stage string) error {
	switch stage {
	case "starting", "protocol_connecting", "email_submitted", "password_submitted", "mfa_submitted", "waiting_callback", "exchanging_token", "applying_credentials":
	default:
		return infraerrors.BadRequest("EXCEL_LOGIN_STAGE_INVALID", "Invalid login stage")
	}
	r, err := s.excelLoginRepo()
	if err != nil {
		return err
	}
	ok, err := r.ProgressExcelLogin(ctx, id, worker, stage)
	if err == nil && !ok {
		return infraerrors.Conflict("EXCEL_LOGIN_TASK_CHANGED", "Task is no longer active")
	}
	return err
}
func (s *OpenAIOAuthService) FailExcelLogin(ctx context.Context, id int64, worker, reason string) error {
	switch reason {
	case "timeout", "password_rejected", "totp_rejected", "proxy_unavailable", "identity_mismatch", "security_check", "additional_verification":
	default:
		reason = "worker_failed"
	}
	r, err := s.excelLoginRepo()
	if err != nil {
		return err
	}
	ok, err := r.FailExcelLogin(ctx, id, worker, reason)
	if err == nil && !ok {
		return infraerrors.Conflict("EXCEL_LOGIN_TASK_CHANGED", "Task is no longer active")
	}
	return err
}
