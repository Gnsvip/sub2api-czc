package admin

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func codexLoginAdmin(c *gin.Context) bool {
	c.Header("Cache-Control", "no-store")
	if adminaccess.RestrictedFromContext(c.Request.Context()) != nil {
		response.Error(c, http.StatusForbidden, "自动授权需要完整管理员权限")
		return false
	}
	return true
}
func (h *OpenAIOAuthHandler) CodexLoginStatus(c *gin.Context) {
	if !codexLoginAdmin(c) {
		return
	}
	id, ok := excelAuthorizationAccountID(c)
	if !ok {
		return
	}
	state, err := h.openaiOAuthService.CodexLoginStatus(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}
func (h *OpenAIOAuthHandler) SaveCodexLogin(c *gin.Context) {
	if !codexLoginAdmin(c) {
		return
	}
	id, ok := excelAuthorizationAccountID(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var input service.CodexLoginInput
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "登录资料格式不正确")
		return
	}
	state, err := h.openaiOAuthService.SaveCodexLogin(c.Request.Context(), id, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}
func (h *OpenAIOAuthHandler) QueueCodexLogin(c *gin.Context) {
	if !codexLoginAdmin(c) {
		return
	}
	id, ok := excelAuthorizationAccountID(c)
	if !ok {
		return
	}
	task, err := h.openaiOAuthService.QueueCodexLogin(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, task)
}

type codexLoginCreateRequest struct {
	Content                 string         `json:"content"`
	AutoRecover             *bool          `json:"auto_recover"`
	Name                    string         `json:"name"`
	Notes                   *string        `json:"notes"`
	GroupIDs                []int64        `json:"group_ids"`
	ProxyIDs                []int64        `json:"proxy_ids"`
	ProxyID                 *int64         `json:"proxy_id"`
	Concurrency             *int           `json:"concurrency"`
	Priority                int            `json:"priority"`
	RateMultiplier          *float64       `json:"rate_multiplier"`
	LoadFactor              *int           `json:"load_factor"`
	ExpiresAt               *int64         `json:"expires_at"`
	AutoPauseOnExpired      *bool          `json:"auto_pause_on_expired"`
	Extra                   map[string]any `json:"extra"`
	CredentialExtras        map[string]any `json:"credential_extras"`
	SkipDefaultGroupBind    bool           `json:"skip_default_group_bind"`
	ConfirmMixedChannelRisk bool           `json:"confirm_mixed_channel_risk"`
}
type codexLoginCreateResult struct {
	Line      int                     `json:"line"`
	Email     string                  `json:"email"`
	AccountID int64                   `json:"account_id,omitempty"`
	Task      *service.ExcelLoginTask `json:"task,omitempty"`
	ErrorCode string                  `json:"error_code,omitempty"`
}

func (h *OpenAIOAuthHandler) CreateCodexLoginAccounts(c *gin.Context) {
	if !codexLoginAdmin(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 384<<10)
	var req codexLoginCreateRequest
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "登录资料格式不正确或超过大小限制")
		return
	}
	entries, err := service.ParseCodexLoginEntries(req.Content)
	req.Content = ""
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if req.Concurrency != nil && *req.Concurrency < 0 || req.Priority < 0 || req.RateMultiplier != nil && *req.RateMultiplier < 0 {
		response.BadRequest(c, "并发、优先级和倍率不能为负数")
		return
	}
	if err = h.openaiOAuthService.CodexLoginReady(); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	results := make([]codexLoginCreateResult, 0, len(entries))
	for i, entry := range entries {
		if c.Request.Context().Err() != nil {
			return
		}
		result := codexLoginCreateResult{Line: i + 1, Email: entry.Email}
		id, e := h.openaiOAuthService.FindCodexLoginAccount(c.Request.Context(), entry.Email)
		if e != nil {
			result.ErrorCode = "account_lookup_failed"
			results = append(results, result)
			continue
		}
		if id > 0 {
			result.AccountID = id
			result.ErrorCode = "account_exists"
			results = append(results, result)
			continue
		}
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = entry.Email
		} else if len(entries) > 1 {
			name = fmt.Sprintf("%s-%d", name, i+1)
		}
		// Finish this bounded setup even when the browser closes after account
		// creation. Failed setup stays visibly pending and cannot serve requests.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 15*time.Second)
		credentials := mergeCodexImportMap(sanitizeCodexImportCredentialExtras(req.CredentialExtras), map[string]any{"email": entry.Email, "client_id": openai.ClientID})
		account, e := h.adminService.CreateAccount(ctx, &service.CreateAccountInput{
			Name: name, Notes: req.Notes, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: credentials, Extra: maps.Clone(req.Extra),
			ProxyID: req.ProxyID, ProxyIDs: req.ProxyIDs, Concurrency: service.ResolveAccountCreateConcurrency(service.PlatformOpenAI, service.AccountTypeOAuth, req.Concurrency), ConcurrencyProvided: req.Concurrency != nil,
			Priority: req.Priority, RateMultiplier: req.RateMultiplier, LoadFactor: req.LoadFactor, GroupIDs: req.GroupIDs, ExpiresAt: req.ExpiresAt, AutoPauseOnExpired: req.AutoPauseOnExpired,
			SkipDefaultGroupBind: req.SkipDefaultGroupBind, SkipMixedChannelCheck: req.ConfirmMixedChannelRisk, AwaitingCodexAuthorization: true,
		})
		if e == nil {
			result.AccountID = account.ID
			_, e = h.openaiOAuthService.SaveCodexLogin(ctx, account.ID, service.CodexLoginInput{Enabled: req.AutoRecover, LoginEmail: entry.Email, Password: entry.Password, TOTPSecret: entry.TOTPSecret})
			if e == nil {
				result.Task, e = h.openaiOAuthService.QueueCodexLogin(ctx, account.ID)
			}
		}
		entries[i].Password = ""
		entries[i].TOTPSecret = ""
		cancel()
		if e != nil {
			result.ErrorCode = infraerrors.Reason(e)
			if result.ErrorCode == "" {
				result.ErrorCode = "authorization_setup_failed"
			}
		}
		results = append(results, result)
	}
	response.Success(c, gin.H{"results": results, "runtime": h.openaiOAuthService.ExcelLoginRuntimeStatus()})
}
