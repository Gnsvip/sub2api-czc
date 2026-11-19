package service

// Adapted from ranxi2001/sub2api@701612f6: local password/TOTP Codex tasks,
// bounded recovery, original identity validation and credential snapshot guards.
import (
	"context"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/mail"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

const CodexAuthorizationPending = "Codex authorization pending"

const CodexWorkspaceFallbackFrom = "codex_workspace_fallback_from"
const CodexPersonalWorkspaceOnly = "codex_personal_workspace_only"
const CodexReauthorizationCountExtraKey = "openai_codex_reauthorization_count"

// Nil keeps an existing choice and defaults a new configuration to enabled.
// Excel/BPS keeps its separate opt-in input and behavior.
type CodexLoginInput struct {
	Enabled    *bool  `json:"enabled"`
	LoginEmail string `json:"login_email"`
	Password   string `json:"password"`
	TOTPSecret string `json:"totp_secret"`
	ClearTOTP  bool   `json:"clear_totp"`
	Revision   int64  `json:"revision"`
}

func codexPersonalPlan(plan string) bool {
	switch plan {
	case "free", "go", "plus", "pro":
		return true
	default:
		return false
	}
}

func CodexLoginCompletionStage(credentials map[string]any, astra *CodexLoginAstraResult) string {
	stage := "succeeded"
	if excelMapString(credentials, CodexWorkspaceFallbackFrom) != "" {
		if only, _ := credentials[CodexPersonalWorkspaceOnly].(bool); only {
			stage = "succeeded_personal_workspace_only"
		} else {
			stage = "succeeded_personal_workspace"
		}
	}
	if astra != nil && !astra.Healthy() {
		if astra.Verdict == string(openAITiboDegraded) {
			return stage + "_astra_degraded"
		}
		return stage + "_astra_unknown"
	}
	return stage
}

func CodexLoginIsReauthorization(credentials map[string]any, message string) bool {
	if message == CodexAuthorizationPending {
		return false
	}
	for _, key := range []string{"access_token", "refresh_token", "chatgpt_account_id"} {
		if excelMapString(credentials, key) != "" {
			return true
		}
	}
	return false
}

type CodexLoginRepository interface {
	GetCodexLoginConfig(context.Context, int64) (*ExcelLoginConfig, error)
	SaveCodexLoginConfig(context.Context, *ExcelLoginConfig, int64) (bool, error)
	GetCodexLoginTask(context.Context, int64) (*ExcelLoginTask, error)
	GetLoginTaskByID(context.Context, int64) (*ExcelLoginTask, error)
	FindCodexLoginAccount(context.Context, string) (int64, error)
	QueueCodexLogin(context.Context, int64, bool) (*ExcelLoginTask, error)
	ListCodexLoginCandidates(context.Context) ([]int64, error)
	HasActiveCodexLogins(context.Context) (bool, error)
	ClaimCodexLogin(context.Context, string) (*ExcelLoginTask, error)
	FinishCodexLogin(context.Context, int64, string, map[string]any, *CodexLoginAstraResult) (bool, error)
}

// No credential fields may be serialized into progress, error messages or logs.
type CodexLoginEntry struct {
	Email      string `json:"-"`
	Password   string `json:"-"`
	TOTPSecret string `json:"-"`
}

func ParseCodexLoginEntries(raw string) ([]CodexLoginEntry, error) {
	if len(raw) > 256<<10 {
		return nil, infraerrors.BadRequest("CODEX_LOGIN_INPUT_INVALID", "一次最多导入 50 个账号")
	}
	entries := make([]CodexLoginEntry, 0)
	seen := map[string]bool{}
	for i, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		first, last := strings.Index(line, "----"), strings.LastIndex(line, "----")
		invalid := func() error {
			return infraerrors.BadRequest("CODEX_LOGIN_INPUT_INVALID", fmt.Sprintf("第 %d 行格式不正确，请填写 邮箱----密码----TOTP 密钥", i+1))
		}
		if first <= 0 || last <= first {
			return nil, invalid()
		}
		entry := CodexLoginEntry{Email: strings.TrimSpace(line[:first]), Password: line[first+4 : last], TOTPSecret: strings.TrimSpace(line[last+4:])}
		address, err := mail.ParseAddress(entry.Email)
		if err != nil || address.Address != entry.Email || len(entry.Email) > 254 || entry.Password == "" || len(entry.Password) > 4096 {
			return nil, invalid()
		}
		secret, err := normalizeCodexLoginTOTP(entry.TOTPSecret)
		if err != nil {
			return nil, invalid()
		}
		entry.TOTPSecret = secret
		key := strings.ToLower(entry.Email)
		if seen[key] {
			return nil, infraerrors.BadRequest("CODEX_LOGIN_INPUT_INVALID", fmt.Sprintf("第 %d 行邮箱重复", i+1))
		}
		seen[key] = true
		entries = append(entries, entry)
		if len(entries) > 50 {
			return nil, infraerrors.BadRequest("CODEX_LOGIN_INPUT_INVALID", "一次最多导入 50 个账号")
		}
	}
	if len(entries) == 0 {
		return nil, infraerrors.BadRequest("CODEX_LOGIN_INPUT_INVALID", "请填写登录资料")
	}
	return entries, nil
}

func normalizeCodexLoginTOTP(raw string) (string, error) {
	secret := strings.TrimRight(strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(raw), " ", "")), "=")
	if len(secret) > 256 {
		return "", errors.New("invalid TOTP secret")
	}
	if secret == "" {
		return "", nil
	}
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil || len(decoded) < 10 || len(decoded) > 64 {
		return "", errors.New("invalid TOTP secret")
	}
	return secret, nil
}

func (s *OpenAIOAuthService) codexLoginRepo() (CodexLoginRepository, error) {
	if s == nil || s.excelGrants == nil || !s.excelGrants.encryptionKeyConfigured || s.excelGrants.encryptor == nil {
		return nil, infraerrors.ServiceUnavailable("CODEX_LOGIN_UNAVAILABLE", "请先配置持久化凭据加密密钥")
	}
	r, ok := s.excelGrants.repo.(CodexLoginRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("CODEX_LOGIN_UNAVAILABLE", "自动授权队列不可用")
	}
	return r, nil
}

func (s *OpenAIOAuthService) CodexLoginReady() error { _, err := s.codexLoginRepo(); return err }
func (s *OpenAIOAuthService) FindCodexLoginAccount(ctx context.Context, email string) (int64, error) {
	r, err := s.codexLoginRepo()
	if err != nil {
		return 0, err
	}
	return r.FindCodexLoginAccount(ctx, email)
}
func (s *OpenAIOAuthService) codexLoginAccount(ctx context.Context, id int64) (*Account, error) {
	if _, err := s.codexLoginRepo(); err != nil {
		return nil, err
	}
	a, err := s.excelGrants.accounts.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if a == nil || !a.IsOpenAIOAuth() || a.IsExcelOAuth() || a.IsOpenAIPersonalAccessToken() || a.IsOpenAIAgentIdentity() || (a.GetCredential("client_id") != "" && a.GetCredential("client_id") != openai.ClientID) {
		return nil, infraerrors.BadRequest("CODEX_LOGIN_ACCOUNT_INVALID", "仅支持普通 Codex OAuth 账号")
	}
	return a, nil
}
func (s *OpenAIOAuthService) codexLoginProxy(ctx context.Context, a *Account) (*Proxy, error) {
	if a.ProxyID == nil {
		return nil, nil
	}
	if s.proxyRepo == nil {
		return nil, errors.New("Codex authorization proxy is unavailable")
	}
	p, err := s.proxyRepo.GetByID(ctx, *a.ProxyID)
	if err != nil || p == nil || p.Status != StatusActive || p.IsExpired(time.Now()) {
		return nil, errors.New("Codex authorization proxy is unavailable")
	}
	return p, nil
}
func (s *OpenAIOAuthService) CodexLoginStatus(ctx context.Context, id int64) (*ExcelLoginStatus, error) {
	a, err := s.codexLoginAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	r, err := s.codexLoginRepo()
	if err != nil {
		return nil, err
	}
	c, err := r.GetCodexLoginConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	if c == nil {
		c = &ExcelLoginConfig{AccountID: id, Enabled: true, LoginEmail: a.GetCredential("email")}
	}
	c.PasswordConfigured = c.PasswordCiphertext != ""
	c.TOTPConfigured = c.TOTPCiphertext != ""
	t, err := r.GetCodexLoginTask(ctx, id)
	if err != nil {
		return nil, err
	}
	return &ExcelLoginStatus{Config: c, Task: t, Runtime: s.ExcelLoginRuntimeStatus()}, nil
}
func (s *OpenAIOAuthService) SaveCodexLogin(ctx context.Context, id int64, input CodexLoginInput) (*ExcelLoginStatus, error) {
	a, err := s.codexLoginAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	r, err := s.codexLoginRepo()
	if err != nil {
		return nil, err
	}
	c, err := r.GetCodexLoginConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	if c == nil {
		c = &ExcelLoginConfig{AccountID: id, Enabled: true, LoginEmail: a.GetCredential("email")}
	}
	previous := *c
	if previous.Revision != input.Revision {
		return nil, infraerrors.Conflict("CODEX_LOGIN_CONFIG_CHANGED", "设置已变更，请刷新后重试")
	}
	if input.Enabled != nil {
		c.Enabled = *input.Enabled
	}
	if input.LoginEmail != "" {
		c.LoginEmail = strings.TrimSpace(input.LoginEmail)
	}
	address, err := mail.ParseAddress(c.LoginEmail)
	if err != nil || address.Address != c.LoginEmail || len(c.LoginEmail) > 254 {
		return nil, infraerrors.BadRequest("CODEX_LOGIN_INPUT_INVALID", "请填写有效邮箱")
	}
	if email := a.GetCredential("email"); email != "" && !strings.EqualFold(email, c.LoginEmail) {
		return nil, infraerrors.BadRequest("CODEX_LOGIN_IDENTITY_MISMATCH", "登录邮箱必须与原账号一致")
	}
	if len(input.Password) > 4096 {
		return nil, infraerrors.BadRequest("CODEX_LOGIN_INPUT_INVALID", "密码过长")
	}
	if input.Password != "" {
		c.PasswordCiphertext, err = s.excelGrants.encryptor.Encrypt(input.Password)
		if err != nil {
			return nil, errors.New("cannot encrypt login password")
		}
	}
	if input.ClearTOTP {
		c.TOTPCiphertext = ""
	}
	if input.TOTPSecret != "" {
		secret, e := normalizeCodexLoginTOTP(input.TOTPSecret)
		if e != nil {
			return nil, infraerrors.BadRequest("CODEX_LOGIN_INPUT_INVALID", "请填写 TOTP 密钥，不是六位验证码")
		}
		c.TOTPCiphertext, err = s.excelGrants.encryptor.Encrypt(secret)
		if err != nil {
			return nil, errors.New("cannot encrypt TOTP secret")
		}
	}
	if c.PasswordCiphertext == "" {
		return nil, infraerrors.BadRequest("CODEX_LOGIN_INPUT_INVALID", "请先保存登录密码")
	}
	if previous.Revision > 0 && previous.Enabled == c.Enabled && previous.LoginEmail == c.LoginEmail && previous.PasswordCiphertext == c.PasswordCiphertext && previous.TOTPCiphertext == c.TOTPCiphertext {
		return s.CodexLoginStatus(ctx, id)
	}
	changed, err := r.SaveCodexLoginConfig(ctx, c, input.Revision)
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, infraerrors.Conflict("CODEX_LOGIN_CONFIG_CHANGED", "设置已变更，请刷新后重试")
	}
	return s.CodexLoginStatus(ctx, id)
}
func (s *OpenAIOAuthService) QueueCodexLogin(ctx context.Context, id int64) (*ExcelLoginTask, error) {
	a, err := s.codexLoginAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	if a.Status == StatusDisabled {
		return nil, infraerrors.BadRequest("CODEX_LOGIN_DISABLED", "账号已禁用，请先启用账号")
	}
	r, err := s.codexLoginRepo()
	if err != nil {
		return nil, err
	}
	t, err := r.QueueCodexLogin(ctx, id, true)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, infraerrors.Conflict("CODEX_LOGIN_BUSY", "尚未配置登录资料、已有任务进行中，或仍在失败冷却期")
	}
	s.ensureExcelLoginWorker()
	return t, nil
}

// Only persisted authentication failures trigger automatic re-login. Network,
// quota, policy and account-deactivation errors never start a fresh session.
func codexLoginRecoveryNeeded(a *Account) bool {
	if a == nil || a.Status != StatusError {
		return false
	}
	m := a.ErrorMessage
	for _, terminal := range []string{"account_deactivated", "account_deleted", "account_retired", "cyber_policy"} {
		if strings.Contains(strings.ToLower(m), terminal) {
			return false
		}
	}
	if m == CodexAuthorizationPending {
		return true
	}
	if strings.HasPrefix(m, "Token revoked (401):") || strings.HasPrefix(m, "Unauthorized (401):") ||
		strings.HasPrefix(m, "Authentication failed (401):") {
		return true
	}
	if strings.HasPrefix(m, "Token refresh failed (non-retryable):") {
		for _, code := range []string{"refresh_token_reused", "refresh_token_invalidated", "invalid_grant", "invalid_refresh_token"} {
			if strings.Contains(m, code) {
				return true
			}
		}
	}
	return false
}
func CodexLoginCanRecoverState(status, message string) bool {
	return codexLoginRecoveryNeeded(&Account{Status: status, ErrorMessage: message})
}
func (s *OpenAIOAuthService) SyncCodexLogins(ctx context.Context) error {
	r, err := s.codexLoginRepo()
	if err != nil {
		return nil
	}
	active, err := r.HasActiveCodexLogins(ctx)
	if err != nil {
		return err
	}
	ids, err := r.ListCodexLoginCandidates(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		a, e := s.codexLoginAccount(ctx, id)
		if e != nil || !codexLoginRecoveryNeeded(a) {
			continue
		}
		t, e := r.QueueCodexLogin(ctx, id, false)
		if e != nil {
			return e
		}
		active = active || t != nil
	}
	if active {
		s.ensureExcelLoginWorker()
	}
	return nil
}
func (s *OpenAIOAuthService) ClaimCodexLogin(ctx context.Context, worker string) (*ExcelLoginClaim, error) {
	if strings.TrimSpace(worker) == "" || len(worker) > 128 {
		return nil, infraerrors.BadRequest("CODEX_LOGIN_WORKER_INVALID", "Invalid worker ID")
	}
	r, err := s.codexLoginRepo()
	if err != nil {
		return nil, nil
	}
	t, err := r.ClaimCodexLogin(ctx, worker)
	if err != nil || t == nil {
		return nil, err
	}
	fail := func(reason string) (*ExcelLoginClaim, error) {
		_ = s.FailExcelLogin(ctx, t.ID, worker, reason)
		return nil, nil
	}
	a, err := s.codexLoginAccount(ctx, t.AccountID)
	if err != nil {
		return fail("account_changed")
	}
	c, err := r.GetCodexLoginConfig(ctx, t.AccountID)
	if err != nil || c == nil || c.Revision != t.ConfigRevision {
		return fail("config_changed")
	}
	password, err := s.excelGrants.encryptor.Decrypt(c.PasswordCiphertext)
	if err != nil || password == "" {
		return fail("credentials_unavailable")
	}
	secret := ""
	if c.TOTPCiphertext != "" {
		secret, err = s.excelGrants.encryptor.Decrypt(c.TOTPCiphertext)
		if err != nil {
			return fail("credentials_unavailable")
		}
	}
	p, err := s.codexLoginProxy(ctx, a)
	if err != nil {
		return fail("proxy_unavailable")
	}
	proxy := ""
	if p != nil {
		proxy = p.URL()
	}
	return &ExcelLoginClaim{OAuthProfile: "codex", ExpectedWorkspace: a.GetCredential("chatgpt_account_id"), TaskID: t.ID, AccountID: a.ID, LoginEmail: c.LoginEmail, CredentialMode: "password_totp", Engine: "local_worker", Password: password, TOTPSecret: secret, ProxyURL: proxy}, nil
}
func (s *OpenAIOAuthService) IsCodexLoginTask(ctx context.Context, id int64) (bool, error) {
	r, err := s.codexLoginRepo()
	if err != nil {
		return false, err
	}
	t, err := r.GetLoginTaskByID(ctx, id)
	return t != nil && t.OAuthProfile == "codex", err
}

func codexLoginTokenInfo(c map[string]any) (*OpenAITokenInfo, error) {
	invalid := errors.New("invalid Codex OAuth identity")
	if excelMapString(c, "client_id") != openai.ClientID || excelMapString(c, "refresh_token") == "" {
		return nil, invalid
	}
	access := excelMapString(c, "access_token")
	parts := strings.Split(access, ".")
	if len(parts) != 3 {
		return nil, invalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, invalid
	}
	var client struct {
		ClientID string `json:"client_id"`
		AZP      string `json:"azp"`
	}
	if json.Unmarshal(payload, &client) != nil || firstNonEmptyExcel(client.ClientID, client.AZP) != openai.ClientID {
		return nil, invalid
	}
	at, err := openai.DecodeIDToken(access)
	if err != nil || at.Exp <= time.Now().Unix() || at.OpenAIAuth == nil || at.OpenAIAuth.ChatGPTAccountID == "" {
		return nil, invalid
	}
	it, err := openai.DecodeIDToken(excelMapString(c, "id_token"))
	if err != nil || it.Exp <= time.Now().Unix() {
		return nil, invalid
	}
	aud := false
	for _, a := range it.Aud {
		aud = aud || a == openai.ClientID
	}
	if !aud {
		return nil, invalid
	}
	user := firstNonEmptyExcel(at.OpenAIAuth.ChatGPTUserID, at.OpenAIAuth.UserID)
	if it.OpenAIAuth != nil {
		if v := it.OpenAIAuth.ChatGPTAccountID; v != "" && v != at.OpenAIAuth.ChatGPTAccountID {
			return nil, invalid
		}
		if v := firstNonEmptyExcel(it.OpenAIAuth.ChatGPTUserID, it.OpenAIAuth.UserID); v != "" {
			if user != "" && user != v {
				return nil, invalid
			}
			user = v
		}
	}
	if user == "" || it.Email == "" || (at.Email != "" && !strings.EqualFold(at.Email, it.Email)) {
		return nil, invalid
	}
	userInfo := it.GetUserInfo()
	if excelMapString(c, CodexWorkspaceFallbackFrom) != "" && (!codexPersonalPlan(at.OpenAIAuth.ChatGPTPlanType) || !codexPersonalPlan(userInfo.PlanType)) {
		return nil, invalid
	}
	return &OpenAITokenInfo{AccessToken: access, RefreshToken: excelMapString(c, "refresh_token"), IDToken: excelMapString(c, "id_token"), ClientID: openai.ClientID, ExpiresAt: at.Exp, Email: it.Email, ChatGPTAccountID: at.OpenAIAuth.ChatGPTAccountID, ChatGPTUserID: user, PlanType: userInfo.PlanType, OrganizationID: userInfo.OrganizationID, SubscriptionExpiresAt: userInfo.SubscriptionExpiresAt}, nil
}
func ValidateCodexLoginIdentity(stored, candidate map[string]any) error {
	info, err := codexLoginTokenInfo(candidate)
	if err != nil {
		return err
	}
	return validateCodexLoginIdentity(stored, candidate, info)
}

func validateCodexLoginIdentity(stored, candidate map[string]any, info *OpenAITokenInfo) error {
	from := excelMapString(candidate, CodexWorkspaceFallbackFrom)
	if from != "" {
		// Only the same stored person/email may fall back to a verified personal
		// workspace. A missing binding, another team or an unsolicited change fails.
		if from != excelMapString(stored, "chatgpt_account_id") || from == info.ChatGPTAccountID ||
			excelMapString(stored, "email") == "" || excelMapString(stored, "chatgpt_user_id") == "" || !codexPersonalPlan(info.PlanType) {
			return errors.New("invalid Codex personal workspace fallback")
		}
		stored = maps.Clone(stored)
		stored["chatgpt_account_id"] = info.ChatGPTAccountID
	}
	return validateExcelIdentity(&Account{Credentials: stored}, info)
}
func (s *OpenAIOAuthService) CompleteCodexLogin(ctx context.Context, id int64, worker string, c map[string]any) (*ExcelLoginTask, error) {
	r, err := s.codexLoginRepo()
	if err != nil {
		return nil, err
	}
	t, err := r.GetLoginTaskByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if t == nil || t.OAuthProfile != "codex" || t.Status != "running" || t.WorkerID != worker {
		return nil, infraerrors.Conflict("CODEX_LOGIN_TASK_CHANGED", "授权任务已变更")
	}
	a, err := s.codexLoginAccount(ctx, t.AccountID)
	if err != nil {
		return nil, err
	}
	info, err := codexLoginTokenInfo(c)
	if err == nil {
		err = validateCodexLoginIdentity(a.Credentials, c, info)
	}
	if err != nil {
		_ = s.FailExcelLogin(ctx, id, worker, "identity_mismatch")
		return nil, infraerrors.BadRequest("CODEX_LOGIN_IDENTITY_MISMATCH", "授权身份与原账号不一致")
	}
	cfg, err := r.GetCodexLoginConfig(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	if cfg == nil || cfg.Revision != t.ConfigRevision {
		return nil, infraerrors.Conflict("CODEX_LOGIN_CONFIG_CHANGED", "登录设置已变更，未覆盖新设置")
	}
	if !strings.EqualFold(cfg.LoginEmail, info.Email) {
		_ = s.FailExcelLogin(ctx, id, worker, "identity_mismatch")
		return nil, infraerrors.BadRequest("CODEX_LOGIN_IDENTITY_MISMATCH", "授权邮箱与登录资料不一致")
	}
	safe := s.BuildAccountCredentials(info)
	if excelMapString(c, CodexWorkspaceFallbackFrom) != "" {
		// A new workspace must not inherit the old team's billing metadata.
		safe["organization_id"] = info.OrganizationID
		safe["subscription_expires_at"] = info.SubscriptionExpiresAt
		safe["subscription_is_delinquent"] = nil
		safe["chatgpt_account_is_fedramp"] = false
	}
	if s.codexLoginVerify == nil {
		return nil, infraerrors.ServiceUnavailable("CODEX_LOGIN_VERIFIER_UNAVAILABLE", "授权验证服务未就绪")
	}
	probe := *a
	probe.Credentials = MergeCredentials(a.Credentials, maps.Clone(safe))
	// The verification must use the same configured exit, never silently direct.
	probe.Proxy, err = s.codexLoginProxy(ctx, a)
	if err != nil {
		_ = s.FailExcelLogin(ctx, id, worker, "proxy_unavailable")
		return nil, infraerrors.ServiceUnavailable("CODEX_LOGIN_PROXY_UNAVAILABLE", "账号代理不可用")
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	err = s.codexLoginVerify(verifyCtx, &probe)
	cancel()
	if err != nil {
		_ = s.FailExcelLogin(ctx, id, worker, "verification_failed")
		return nil, infraerrors.BadRequest("CODEX_LOGIN_VERIFICATION_FAILED", "授权后的连通验证未通过，请稍后重试")
	}
	var astra *CodexLoginAstraResult
	if CodexLoginIsReauthorization(a.Credentials, a.ErrorMessage) {
		// A failed quality probe must not discard valid new tokens or cause a
		// second browser login. Only a fresh healthy result may resume routing.
		result := CodexLoginAstraResult{Verdict: string(openAITiboUnknown)}
		if s.codexLoginAstraProbe != nil {
			probeCtx, probeCancel := context.WithTimeout(ctx, openAITiboProbeTimeout)
			result = s.codexLoginAstraProbe(probeCtx, &probe)
			probeCancel()
		}
		astra = &result
	}
	if from := excelMapString(c, CodexWorkspaceFallbackFrom); from != "" {
		safe[CodexWorkspaceFallbackFrom] = from
		safe[CodexPersonalWorkspaceOnly], _ = c[CodexPersonalWorkspaceOnly].(bool)
	}
	changed, err := r.FinishCodexLogin(ctx, id, worker, safe, astra)
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, infraerrors.Conflict("CODEX_LOGIN_TASK_CHANGED", "账号或登录资料已变更，未覆盖新设置")
	}
	s.AuthCredentialsPersisted(ctx, a)
	if s.codexLoginCredentialsChanged != nil {
		s.codexLoginCredentialsChanged(ctx, a)
	}
	return &ExcelLoginTask{ID: id, AccountID: a.ID, OAuthProfile: "codex", Status: "succeeded", Stage: CodexLoginCompletionStage(safe, astra)}, nil
}
