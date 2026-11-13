package service

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

func (s *OpenAIOAuthService) excelAuthorizationAccount(ctx context.Context, id int64) (*Account, error) {
	if s.excelGrants == nil || s.excelGrants.repo == nil || !s.excelGrants.encryptionKeyConfigured {
		return nil, infraerrors.ServiceUnavailable("OPENAI_EXCEL_STORAGE_UNAVAILABLE", "A persistent credential encryption key is required for independent Excel authorization")
	}
	account, err := s.excelGrants.accounts.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !QualityBPSEligible(account) || account.IsOpenAIPersonalAccessToken() || account.IsExcelOAuth() {
		return nil, infraerrors.BadRequest("OPENAI_EXCEL_ACCOUNT_INVALID", "Independent Excel authorization requires a non-free ChatGPT OAuth account")
	}
	return account, nil
}

func (s *OpenAIOAuthService) BeginExcelAuthorization(ctx context.Context, id int64) (*OpenAIAuthURLResult, error) {
	account, err := s.excelAuthorizationAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	result, err := s.GenerateAuthURL(ctx, account.ProxyID, "", openai.OAuthPlatformExcel)
	if err != nil {
		return nil, err
	}
	original, ok := s.sessionStore.Get(result.SessionID)
	if !ok {
		return nil, errors.New("authorization session unavailable")
	}
	session := *original
	session.ExcelAccountID = id
	session.ExcelExpectedCredentials = maps.Clone(account.Credentials)
	s.sessionStore.Set(result.SessionID, &session)
	return result, nil
}

func (s *OpenAIOAuthService) CompleteExcelAuthorization(ctx context.Context, id int64, input *OpenAIExchangeCodeInput) (*ExcelBPSCredentialState, error) {
	account, err := s.excelAuthorizationAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	session, ok := s.sessionStore.Get(input.SessionID)
	if !ok || session.ExcelAccountID != id || session.ClientID != openai.ExcelClientID {
		return nil, infraerrors.BadRequest("OPENAI_EXCEL_SESSION_MISMATCH", "Excel authorization session is expired or belongs to another account")
	}
	bound := *input
	bound.ExcelAccountID = id
	bound.ProxyID = nil
	bound.RedirectURI = ""
	info, err := s.ExchangeCode(ctx, &bound)
	if err != nil {
		return nil, err
	}
	credentials := s.BuildAccountCredentials(info)
	checked, err := excelOAuthTokenInfo(credentials)
	if err == nil {
		err = validateExcelIdentity(account, checked)
	}
	if err != nil {
		return nil, infraerrors.BadRequest("OPENAI_EXCEL_IDENTITY_MISMATCH", "Excel authorization identity or workspace does not match this account")
	}
	raw, err := json.Marshal(credentials)
	if err != nil {
		return nil, err
	}
	ciphertext, err := s.excelGrants.encryptor.Encrypt(string(raw))
	if err != nil {
		return nil, errors.New("failed to encrypt Excel authorization")
	}
	state := ExcelBPSGrantState(checked.ExpiresAt)
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	changed, err := s.excelGrants.repo.SaveExcelCredentials(persistCtx, id, session.ExcelExpectedCredentials, ciphertext, state)
	if err != nil {
		return nil, errors.New("failed to save Excel authorization")
	}
	if !changed {
		return nil, infraerrors.Conflict("OPENAI_EXCEL_ACCOUNT_CHANGED", "The account credentials changed during authorization; authorize again")
	}
	return &state, nil
}

func (s *OpenAIOAuthService) ExcelAuthorizationState(ctx context.Context, id int64) (*ExcelBPSCredentialState, error) {
	account, err := s.excelAuthorizationAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	view := *account
	view.Extra = maps.Clone(account.Extra)
	if view.Extra == nil {
		view.Extra = make(map[string]any)
	}
	view.Extra[OpenAIExcelBPSEnabledKey] = true
	state := ExcelBPSCredentialStateFromAccount(&view, time.Now())
	if state == nil {
		state = &ExcelBPSCredentialState{Status: "unknown"}
	}
	ciphertext, err := s.excelGrants.repo.GetExcelCredentials(ctx, id)
	if err != nil {
		return nil, errors.New("failed to read Excel authorization status")
	}
	if ciphertext == "" {
		state.Status = "missing"
		state.ExpiresAt = nil
	}
	return state, nil
}
