package service

// Independent grant validation, refresh locking, rotation recovery and 401 CAS
// are adapted from ranxi2001/sub2api@11f25089 openai_excel_oauth.go.
import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/reauthruntime"
	"strings"
	"sync"
	"time"
)

type OpenAIExcelOAuthRepository interface {
	GetExcelCredentials(context.Context, int64) (string, error)
	SaveExcelCredentials(context.Context, int64, map[string]any, string, ExcelBPSCredentialState) (bool, error)
	ReplaceExcelCredentials(context.Context, int64, string, string, ...ExcelBPSCredentialState) (bool, error)
	DeleteExcelCredentials(context.Context, int64, string, ...ExcelBPSCredentialState) error
}

type OpenAIExcelOAuthService struct {
	settings                SettingRepository
	loginWorker             *reauthruntime.Manager
	loginWorkerToken        string
	repo                    OpenAIExcelOAuthRepository
	accounts                AccountRepository
	oauth                   *OpenAIOAuthService
	encryptor               SecretEncryptor
	encryptionKeyConfigured bool
}

func excelMapString(m map[string]any, key string) string {
	value, _ := m[key].(string)
	return strings.TrimSpace(value)
}
func firstNonEmptyExcel(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
func (s *OpenAIExcelOAuthService) excelProxyURL(ctx context.Context, id *int64) (string, error) {
	if id == nil {
		return "", nil
	}
	p, err := s.oauth.proxyRepo.GetByID(ctx, *id)
	if err != nil {
		return "", errors.New("Excel authorization proxy is unavailable")
	}
	if p == nil {
		return "", errors.New("Excel authorization proxy was removed")
	}
	return p.URL(), nil
}
func validateExcelIdentity(account *Account, info *OpenAITokenInfo) error {
	if account == nil || info == nil {
		return errors.New("missing account identity")
	}
	found := false
	for _, pair := range [][2]string{{account.GetCredential("chatgpt_account_id"), info.ChatGPTAccountID}, {account.GetCredential("chatgpt_user_id"), info.ChatGPTUserID}, {strings.ToLower(account.GetCredential("email")), strings.ToLower(info.Email)}} {
		old, current := strings.TrimSpace(pair[0]), strings.TrimSpace(pair[1])
		if old == "" {
			continue
		}
		found = true
		if current == "" || subtle.ConstantTimeCompare([]byte(old), []byte(current)) != 1 {
			return infraerrors.Conflict("OPENAI_EXCEL_IDENTITY_MISMATCH", "Excel authorization belongs to a different account or workspace")
		}
	}
	if !found {
		return infraerrors.BadRequest("OPENAI_EXCEL_IDENTITY_REQUIRED", "The account has no stored identity to verify")
	}
	return nil
}

// Claims are consistency checks on the authenticated worker / TLS token
// exchange result, not a replacement for token signature verification.
func excelOAuthTokenInfo(credentials map[string]any) (*OpenAITokenInfo, error) {
	return decodeExcelOAuthTokenInfo(credentials, false)
}

func decodeExcelOAuthTokenInfo(credentials map[string]any, allowExpired bool) (*OpenAITokenInfo, error) {
	if excelMapString(credentials, "client_id") != openai.ExcelClientID || excelMapString(credentials, "refresh_token") == "" {
		return nil, errors.New("excel OAuth client or refresh token is missing")
	}
	access := excelMapString(credentials, "access_token")
	parts := strings.Split(access, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid Excel access token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("invalid Excel access token")
	}
	var client struct {
		ClientID string `json:"client_id"`
		AZP      string `json:"azp"`
	}
	if json.Unmarshal(payload, &client) != nil || firstNonEmptyExcel(client.ClientID, client.AZP) != openai.ExcelClientID {
		return nil, errors.New("excel access token client mismatch")
	}
	at, err := openai.DecodeIDToken(access)
	if err != nil || at.Exp <= 0 || (!allowExpired && at.Exp <= time.Now().Unix()) || at.OpenAIAuth == nil || at.OpenAIAuth.ChatGPTAccountID == "" {
		return nil, errors.New("excel access token identity or expiry is missing")
	}
	id, err := openai.DecodeIDToken(excelMapString(credentials, "id_token"))
	if err != nil {
		return nil, errors.New("invalid Excel identity token")
	}
	audienceOK := false
	for _, aud := range id.Aud {
		audienceOK = audienceOK || aud == openai.ExcelClientID
	}
	if !audienceOK {
		return nil, errors.New("excel identity token client mismatch")
	}
	if workspace := excelMapString(credentials, "chatgpt_account_id"); workspace != "" && workspace != at.OpenAIAuth.ChatGPTAccountID {
		return nil, errors.New("excel workspace mismatch")
	}
	return &OpenAITokenInfo{AccessToken: access, RefreshToken: excelMapString(credentials, "refresh_token"),
		IDToken: excelMapString(credentials, "id_token"), ClientID: openai.ExcelClientID, ExpiresAt: at.Exp,
		Email: id.Email, ChatGPTAccountID: at.OpenAIAuth.ChatGPTAccountID,
		ChatGPTUserID: firstNonEmptyExcel(at.OpenAIAuth.ChatGPTUserID, at.OpenAIAuth.UserID, id.GetUserInfo().ChatGPTUserID)}, nil
}

var excelOAuthRefreshLocks sync.Map

// Stored inside the encrypted grant. A resource-server 401 rejects the access
// token, not necessarily the refresh token; refresh it on the next token read.
const excelAccessTokenRejectedKey = "_access_token_rejected"

// Both the refresh writer and 401 marker use the same local/distributed lock.
// A 401 never waits behind a refresh or modifies its snapshot: the refresh
// already owns recovery, and a failed attempt remains due for the next request.
func lockExcelCredentials(ctx context.Context, id int64, tokenCache OpenAITokenCache, wait bool) (func(), error) {
	key := fmt.Sprintf("openai:excel:account:%d", id)
	actual, _ := excelOAuthRefreshLocks.LoadOrStore(key, newContextMutex())
	mu, ok := actual.(*contextMutex)
	if !ok {
		return nil, errors.New("excel refresh lock is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if wait {
		if err := mu.Lock(ctx); err != nil {
			return nil, err
		}
	} else {
		select {
		case mu.token <- struct{}{}:
		default:
			return nil, nil // Refresh owns the grant; do not change its CAS snapshot.
		}
	}
	// Fail closed on distributed-lock errors: Excel refresh tokens rotate.
	if tokenCache != nil {
		acquired, err := tokenCache.AcquireRefreshLock(ctx, key, 60*time.Second)
		if err != nil || !acquired {
			mu.Unlock()
			if err == nil && !wait {
				return nil, nil
			}
			return nil, errors.New("excel credential refresh is busy or unavailable")
		}
		return func() {
			defer mu.Unlock()
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			_ = tokenCache.ReleaseRefreshLock(cleanup, key)
		}, nil
	}
	return mu.Unlock, nil
}

func (s *OpenAIExcelOAuthService) ExcelAccessToken(ctx context.Context, account *Account, tokenCache OpenAITokenCache) (string, error) {
	repo := s.repo
	if repo == nil {
		return "", errors.New("excel credential storage is unavailable")
	}
	if account == nil {
		return "", errors.New("account is missing")
	}
	// Ready grants are read without a distributed lock so concurrent BPS
	// requests on multiple API replicas can use the same access token.
	_, cached, fresh, err := s.readExcelGrant(ctx, account)
	if err != nil {
		return "", err
	}
	claims, err := openai.DecodeIDToken(excelMapString(cached, "access_token"))
	if err != nil {
		return "", errors.New("invalid saved Excel access token")
	}
	if cached[excelAccessTokenRejectedKey] != true && claims.Exp > time.Now().Add(openAITokenRefreshSkew).Unix() {
		return validatedExcelAccessToken(fresh, cached)
	}
	unlock, err := lockExcelCredentials(ctx, account.ID, tokenCache, true)
	if err != nil {
		return "", err
	}
	defer unlock()
	// Leave time for detached persistence (5s) and lock release (2s) within
	// the 60s lease, including the reread and the token endpoint call.
	lockedCtx, cancelLocked := context.WithTimeout(ctx, 50*time.Second)
	defer cancelLocked()
	ctx = lockedCtx
	ciphertext, credentials, fresh, err := s.readExcelGrant(ctx, account)
	if err != nil {
		return "", err
	}
	accessClaims, err := openai.DecodeIDToken(excelMapString(credentials, "access_token"))
	if err != nil {
		return "", errors.New("invalid saved Excel access token")
	}
	if credentials[excelAccessTokenRejectedKey] == true || accessClaims.Exp <= time.Now().Add(openAITokenRefreshSkew).Unix() {
		oldInfo, err := decodeExcelOAuthTokenInfo(credentials, true)
		if err == nil {
			err = validateExcelIdentity(fresh, oldInfo)
		}
		if err != nil {
			return "", errors.New("saved Excel identity does not match the account")
		}
		proxyURL, err := s.excelProxyURL(ctx, fresh.ProxyID)
		if err != nil {
			return "", err
		}
		refreshCtx, stop := context.WithTimeout(ctx, 45*time.Second)
		response, err := s.oauth.oauthClient.RefreshTokenWithClientID(refreshCtx, excelMapString(credentials, "refresh_token"), proxyURL, openai.ExcelClientID)
		stop()
		if err != nil || response == nil {
			if isInvalidGrantError(err) {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				if token, changed, recoverErr := s.recoverExcelRefreshRace(cleanup, account, ciphertext); changed || recoverErr != nil {
					return token, recoverErr
				}
				if err := repo.DeleteExcelCredentials(cleanup, account.ID, ciphertext); err != nil {
					return "", errors.New("failed to clear invalid Excel credentials; retry later")
				}
				// Reauthorization can replace the grant after our first reread.
				// Conditional deletion preserves it; recover it instead of lying
				// to this caller that a new login is required.
				if token, changed, recoverErr := s.recoverExcelRefreshRace(cleanup, account, ciphertext); changed || recoverErr != nil {
					return token, recoverErr
				}
				return "", errors.New("excel OAuth refresh token is invalid; authorize Excel BPS again in Account Management")
			}
			return "", errors.New("excel OAuth refresh failed; credentials were retained, retry later")
		}
		credentials["access_token"] = response.AccessToken
		if response.RefreshToken != "" {
			credentials["refresh_token"] = response.RefreshToken
		}
		if response.IDToken != "" {
			credentials["id_token"] = response.IDToken
		}
		info, err := excelOAuthTokenInfo(credentials)
		if err == nil {
			err = validateExcelIdentity(fresh, info)
		}
		if err != nil {
			return "", errors.New("refreshed Excel identity does not match the account")
		}
		raw, err := json.Marshal(s.oauth.BuildAccountCredentials(info))
		if err != nil {
			return "", err
		}
		replacement, err := s.encryptor.Encrypt(string(raw))
		if err != nil {
			return "", errors.New("failed to encrypt refreshed Excel credentials")
		}
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		changed, err := repo.ReplaceExcelCredentials(persistCtx, account.ID, ciphertext, replacement, ExcelBPSGrantState(info.ExpiresAt))
		if err != nil {
			return "", errors.New("excel credentials could not be saved")
		}
		if !changed {
			if token, replaced, recoverErr := s.recoverExcelRefreshRace(persistCtx, account, ciphertext); replaced || recoverErr != nil {
				return token, recoverErr
			}
			return "", errors.New("excel credentials changed or could not be saved")
		}
	}
	return validatedExcelAccessToken(fresh, credentials)
}

// An invalid_grant may refer to an old RT consumed by another writer. Read
// current storage before classifying failure; never expose an unsaved token.
func (s *OpenAIExcelOAuthService) recoverExcelRefreshRace(ctx context.Context, account *Account, expected string) (string, bool, error) {
	repo := s.repo
	if repo == nil {
		return "", false, errors.New("excel credential storage is unavailable")
	}
	current, err := repo.GetExcelCredentials(ctx, account.ID)
	if err != nil {
		return "", false, errors.New("failed to reread Excel credentials; retry later")
	}
	if current == "" || current == expected {
		return "", false, nil
	}
	_, credentials, fresh, err := s.readExcelGrant(ctx, account)
	if err != nil {
		return "", true, err
	}
	if credentials[excelAccessTokenRejectedKey] == true {
		return "", true, errors.New("excel credentials changed and still need refresh; retry later")
	}
	token, err := validatedExcelAccessToken(fresh, credentials)
	return token, true, err
}

func validatedExcelAccessToken(account *Account, credentials map[string]any) (string, error) {
	info, err := excelOAuthTokenInfo(credentials)
	if err == nil {
		err = validateExcelIdentity(account, info)
	}
	if err != nil {
		return "", errors.New("saved Excel identity does not match the account")
	}
	return info.AccessToken, nil
}

func (s *OpenAIExcelOAuthService) readExcelGrant(ctx context.Context, account *Account) (string, map[string]any, *Account, error) {
	repo := s.repo
	if repo == nil {
		return "", nil, nil, errors.New("excel credential storage is unavailable")
	}
	ciphertext, err := repo.GetExcelCredentials(ctx, account.ID)
	if err != nil {
		return "", nil, nil, errors.New("failed to read Excel credentials")
	}
	if ciphertext == "" {
		if account.GetCredential("client_id") == openai.ExcelClientID {
			return "", nil, nil, errExcelLegacyCredentials
		}
		return "", nil, nil, infraerrors.ServiceUnavailable("OPENAI_EXCEL_AUTH_REQUIRED", "Authorize Excel BPS for this account in Account Management")
	}
	plain, err := s.encryptor.Decrypt(ciphertext)
	if err != nil {
		return "", nil, nil, errors.New("failed to decrypt Excel credentials")
	}
	var credentials map[string]any
	if json.Unmarshal([]byte(plain), &credentials) != nil {
		return "", nil, nil, errors.New("invalid saved Excel credentials")
	}
	fresh, err := s.accounts.GetByID(ctx, account.ID)
	if err != nil {
		return "", nil, nil, err
	}
	return ciphertext, credentials, fresh, nil
}

var errExcelLegacyCredentials = errors.New("legacy Excel credentials")

// Reject only the access token actually used by this request. Preserve its RT
// for the normal refresh flow, and never overwrite a concurrently rotated grant.
func (s *OpenAIExcelOAuthService) invalidateExcelAccessToken(ctx context.Context, id int64, token string, cache OpenAITokenCache) error {
	repo := s.repo
	if repo == nil || token == "" {
		return nil
	}
	unlock, err := lockExcelCredentials(ctx, id, cache, false)
	if err != nil {
		return err
	}
	if unlock == nil {
		return nil
	}
	defer unlock()
	ciphertext, err := repo.GetExcelCredentials(ctx, id)
	if err != nil || ciphertext == "" {
		return err
	}
	plain, err := s.encryptor.Decrypt(ciphertext)
	if err != nil {
		return errors.New("failed to decrypt Excel grant")
	}
	var credentials map[string]any
	if json.Unmarshal([]byte(plain), &credentials) != nil {
		return errors.New("invalid saved Excel grant")
	}
	if excelMapString(credentials, "access_token") != token || credentials[excelAccessTokenRejectedKey] == true {
		return nil
	}
	credentials[excelAccessTokenRejectedKey] = true
	raw, err := json.Marshal(credentials)
	if err != nil {
		return errors.New("failed to encode rejected Excel access token")
	}
	replacement, err := s.encryptor.Encrypt(string(raw))
	if err != nil {
		return errors.New("failed to encrypt rejected Excel access token")
	}
	// Resource-server rejection describes the AT, not the RT. Keep diagnostics
	// at auth_failed until refresh determines whether this grant can recover.
	_, err = repo.ReplaceExcelCredentials(ctx, id, ciphertext, replacement, ExcelBPSGrantFailure(""))
	return err
}
