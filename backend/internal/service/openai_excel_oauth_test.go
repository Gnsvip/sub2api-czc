//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
	"maps"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Reuse upstream refresh/concurrency cases with the local independent grant adapter.
type excelGrantReader struct {
	AccountRepository
	account *Account
}

type excelLoginTestRepo struct {
	*excelReauthTestRepo
	ExcelLoginRepository
	config   *ExcelLoginConfig
	task     *ExcelLoginTask
	claimed  int
	failures []string
}

func (r *excelLoginTestRepo) GetExcelLoginConfig(context.Context, int64) (*ExcelLoginConfig, error) {
	if r.config == nil {
		return nil, nil
	}
	v := *r.config
	return &v, nil
}
func (r *excelLoginTestRepo) GetExcelLoginTask(context.Context, int64) (*ExcelLoginTask, error) {
	return r.task, nil
}
func (r *excelLoginTestRepo) SaveExcelLoginConfig(_ context.Context, c *ExcelLoginConfig, expected int64) (bool, error) {
	if (r.config == nil && expected != 0) || (r.config != nil && r.config.Revision != expected) {
		return false, nil
	}
	v := *c
	v.Revision = expected + 1
	r.config = &v
	return true, nil
}
func (r *excelLoginTestRepo) ClaimExcelLogin(context.Context, string) (*ExcelLoginTask, error) {
	r.claimed++
	return r.task, nil
}
func (r *excelLoginTestRepo) FailExcelLogin(_ context.Context, _ int64, _, code string) (bool, error) {
	r.failures = append(r.failures, code)
	return true, nil
}
func TestExcelLoginConfigAndWorkerBoundaries(t *testing.T) {
	grant, reader, base := newExcelGrantTestService(t)
	repo := &excelLoginTestRepo{excelReauthTestRepo: base}
	grant.repo = repo
	oauth := grant.oauth
	grant.settings = &settingRepoStub{values: map[string]string{SettingKeyExcelBPSEnabled: "true"}}
	input := ExcelLoginInput{Enabled: true, Password: "private-password", TOTPSecret: "JBSWY3DPEHPK3PXP"}
	state, err := oauth.SaveExcelLogin(context.Background(), 42, input)
	require.NoError(t, err)
	require.True(t, state.Config.PasswordConfigured)
	require.True(t, state.Config.TOTPConfigured)
	encoded, err := json.Marshal(state)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), input.Password)
	require.NotContains(t, string(encoded), input.TOTPSecret)
	require.NotContains(t, string(encoded), "encrypted:")
	_, err = oauth.SaveExcelLogin(context.Background(), 42, input)
	require.Error(t, err, "stale settings revision")
	input.Revision = 1
	input.Password = ""
	input.TOTPSecret = ""
	input.ClearTOTP = true
	state, err = oauth.SaveExcelLogin(context.Background(), 42, input)
	require.NoError(t, err)
	require.True(t, state.Config.PasswordConfigured)
	require.False(t, state.Config.TOTPConfigured)
	repo.task = &ExcelLoginTask{ID: 8, AccountID: 42, ConfigRevision: 2, Status: "running"}
	grant.settings = &settingRepoStub{}
	claim, err := oauth.ClaimExcelLogin(context.Background(), "worker")
	require.NoError(t, err)
	require.Nil(t, claim)
	require.Zero(t, repo.claimed)
	grant.settings = &settingRepoStub{values: map[string]string{SettingKeyExcelBPSEnabled: "true"}}
	claim, err = oauth.ClaimExcelLogin(context.Background(), "worker")
	require.NoError(t, err)
	require.Equal(t, "excel", claim.OAuthProfile)
	require.Equal(t, "local_worker", claim.Engine)
	require.Equal(t, "private-password", claim.Password)
	require.Empty(t, claim.TOTPSecret)
	require.Equal(t, reader.account.GetCredential("chatgpt_account_id"), claim.ExpectedWorkspace)
	repo.task.ConfigRevision = 1
	claim, err = oauth.ClaimExcelLogin(context.Background(), "worker")
	require.NoError(t, err)
	require.Nil(t, claim)
	require.Contains(t, repo.failures, "config_changed")
	require.NoError(t, oauth.FailExcelLogin(context.Background(), 8, "worker", "unlabelled PRIVATE SECRET"))
	require.Equal(t, "worker_failed", repo.failures[len(repo.failures)-1])
	grant.loginWorkerToken = strings.Repeat("test", 16)
	require.False(t, oauth.ExcelLoginWorkerAuthenticated(""))
	require.False(t, oauth.ExcelLoginWorkerAuthenticated("wrong"))
	require.True(t, oauth.ExcelLoginWorkerAuthenticated(grant.loginWorkerToken))
	input.Revision = 2
	input.TOTPSecret = "123456"
	_, err = oauth.SaveExcelLogin(context.Background(), 42, input)
	require.Error(t, err)
	input.TOTPSecret = ""
	input.LoginEmail = "other@example.com"
	_, err = oauth.SaveExcelLogin(context.Background(), 42, input)
	require.Error(t, err)
}

func (r *excelGrantReader) GetByID(context.Context, int64) (*Account, error) { return r.account, nil }

type excelGrantCipher struct{}

func (excelGrantCipher) Encrypt(s string) (string, error) { return "encrypted:" + s, nil }
func (excelGrantCipher) Decrypt(s string) (string, error) {
	return strings.TrimPrefix(s, "encrypted:"), nil
}

type excelReauthTestRepo struct {
	OpenAIExcelOAuthRepository
	mu         sync.Mutex
	ciphertext string
	replaceErr error
	diagnosis  ExcelBPSCredentialState
	account    *Account
}

func (r *excelReauthTestRepo) SaveExcelCredentials(_ context.Context, id int64, expected map[string]any, ciphertext string, diagnosis ExcelBPSCredentialState) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.replaceErr != nil {
		return false, r.replaceErr
	}
	if r.account == nil || r.account.ID != id || !reflect.DeepEqual(expected, r.account.Credentials) {
		return false, nil
	}
	r.ciphertext = ciphertext
	r.diagnosis = diagnosis
	return true, nil
}
func (r *excelReauthTestRepo) GetExcelCredentials(context.Context, int64) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ciphertext, nil
}
func (r *excelReauthTestRepo) ReplaceExcelCredentials(_ context.Context, _ int64, expected, replacement string, diagnosis ...ExcelBPSCredentialState) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.replaceErr != nil {
		return false, r.replaceErr
	}
	if r.ciphertext != expected {
		return false, nil
	}
	r.ciphertext = replacement
	if len(diagnosis) > 0 {
		r.diagnosis = diagnosis[0]
	}
	return true, nil
}
func (r *excelReauthTestRepo) DeleteExcelCredentials(ctx context.Context, id int64, expected string, diagnosis ...ExcelBPSCredentialState) error {
	_, err := r.ReplaceExcelCredentials(ctx, id, expected, "", diagnosis...)
	return err
}
func newExcelGrantTestService(t *testing.T) (*OpenAIExcelOAuthService, *excelGrantReader, *excelReauthTestRepo) {
	t.Helper()
	a := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "old-access", "refresh_token": "old-refresh", "chatgpt_account_id": "acct-1", "chatgpt_user_id": "user-1", "email": "user@example.com", "plan_type": "pro"}, Extra: map[string]any{"openai_excel_bps": true}}
	reader := &excelGrantReader{account: a}
	repo := &excelReauthTestRepo{account: a}
	svc := &OpenAIExcelOAuthService{repo: repo, accounts: reader, encryptor: excelGrantCipher{}, encryptionKeyConfigured: true, oauth: NewOpenAIOAuthService(nil, nil)}
	svc.oauth.excelGrants = svc
	return svc, reader, repo
}
func excelTestJWT(m map[string]any) string {
	b, _ := json.Marshal(m)
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".test"
}

type excelGrantExchangeClient struct {
	excelRefreshTestClient
	callsExchange int
}

func (c *excelGrantExchangeClient) ExchangeCode(ctx context.Context, code, verifier, redirect, proxy, clientID string) (*openai.TokenResponse, error) {
	c.callsExchange++
	if clientID != openai.ExcelClientID {
		return nil, errors.New("wrong OAuth client")
	}
	return c.RefreshTokenWithClientID(ctx, "", proxy, clientID)
}
func TestExcelOAuthManualAuthorizationPreservesPrimaryAndBindsSession(t *testing.T) {
	for _, scenario := range []string{"success", "wrong account", "wrong state", "wrong identity", "changed account", "storage failure", "ordinary exchange"} {
		t.Run(scenario, func(t *testing.T) {
			svc, reader, repo := newExcelGrantTestService(t)
			before := maps.Clone(reader.account.Credentials)
			creds := excelTestCredentials("manual", time.Now().Add(time.Hour))
			client := &excelGrantExchangeClient{excelRefreshTestClient: excelRefreshTestClient{credentials: creds}}
			svc.oauth.oauthClient = client
			begun, err := svc.oauth.BeginExcelAuthorization(context.Background(), 42)
			require.NoError(t, err)
			session, ok := svc.oauth.sessionStore.Get(begun.SessionID)
			require.True(t, ok)
			input := &OpenAIExchangeCodeInput{SessionID: begun.SessionID, Code: "code", State: session.State}
			id := int64(42)
			switch scenario {
			case "wrong account":
				id = 43
			case "wrong state":
				input.State = "not-bound"
			case "wrong identity":
				creds["access_token"] = excelTestJWT(map[string]any{"client_id": openai.ExcelClientID, "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "other", "user_id": "user-1"}})
			case "changed account":
				reader.account.Credentials["access_token"] = "replaced"
			case "storage failure":
				repo.replaceErr = errors.New("disk unavailable")
			case "ordinary exchange":
				_, err = svc.oauth.ExchangeCode(context.Background(), input)
				require.Error(t, err)
				require.Zero(t, client.callsExchange)
				return
			}
			state, err := svc.oauth.CompleteExcelAuthorization(context.Background(), id, input)
			if scenario != "success" {
				require.Error(t, err)
				require.Empty(t, repo.ciphertext)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "not_expired", state.Status)
			require.Equal(t, before, reader.account.Credentials)
			require.True(t, strings.HasPrefix(repo.ciphertext, "encrypted:"))
			token, err := svc.ExcelAccessToken(context.Background(), reader.account, nil)
			require.NoError(t, err)
			require.Equal(t, creds["access_token"], token)
			_, err = svc.oauth.CompleteExcelAuthorization(context.Background(), id, input)
			require.Error(t, err)
		})
	}
}

func TestExcelOAuthMissingGrantNeverUsesPrimaryToken(t *testing.T) {
	svc, reader, _ := newExcelGrantTestService(t)
	gateway := excelBPSTestService(nil)
	gateway.openAITokenProvider = &OpenAITokenProvider{openAIOAuthService: svc.oauth}
	token, err := gateway.getExcelBPSAccessToken(context.Background(), reader.account)
	require.Error(t, err)
	require.Empty(t, token)
	require.Equal(t, "old-access", reader.account.GetCredential("access_token"))
}
func excelTestCredentials(tokenLabel string, expiry time.Time) map[string]any {
	auth := map[string]any{"chatgpt_account_id": "acct-1", "user_id": "user-1"}
	return map[string]any{
		"client_id": openai.ExcelClientID, "refresh_token": "excel-refresh-" + tokenLabel, "chatgpt_account_id": "acct-1",
		"access_token": excelTestJWT(map[string]any{"client_id": openai.ExcelClientID, "exp": expiry.Unix(), "jti": tokenLabel, "https://api.openai.com/auth": auth}),
		// sid is a session identifier in Excel; workspace comes from the access token.
		"id_token": excelTestJWT(map[string]any{"aud": []string{openai.ExcelClientID}, "email": "user@example.com", "sid": "excel-session", "exp": expiry.Unix(), "https://api.openai.com/auth": map[string]any{"user_id": "user-1"}}),
	}
}

func storeExcelTestCredentials(t *testing.T, svc *OpenAIExcelOAuthService, repo *excelReauthTestRepo, credentials map[string]any) {
	t.Helper()
	raw, err := json.Marshal(credentials)
	require.NoError(t, err)
	repo.ciphertext, err = svc.encryptor.Encrypt(string(raw))
	require.NoError(t, err)
}

type excelRefreshTestClient struct {
	OpenAIOAuthClient
	calls                  int
	credentials            map[string]any
	refreshToken, clientID string
	err                    error
}

func (c *excelRefreshTestClient) RefreshTokenWithClientID(_ context.Context, rt, _ string, clientID string) (*openai.TokenResponse, error) {
	c.calls++
	c.refreshToken = rt
	c.clientID = clientID
	if c.err != nil {
		return nil, c.err
	}
	return &openai.TokenResponse{AccessToken: excelMapString(c.credentials, "access_token"), RefreshToken: excelMapString(c.credentials, "refresh_token"), IDToken: excelMapString(c.credentials, "id_token"), ExpiresIn: 3600}, nil
}

type excelRefreshLockTestCache struct {
	OpenAITokenCache
	acquisitions int
	releases     int
	granted      bool
	err          error
	key          string
}

func (c *excelRefreshLockTestCache) AcquireRefreshLock(_ context.Context, key string, _ time.Duration) (bool, error) {
	c.acquisitions++
	c.key = key
	return c.granted, c.err
}

func (c *excelRefreshLockTestCache) ReleaseRefreshLock(context.Context, string) error {
	c.releases++
	return nil
}

func TestExcelOAuthRefreshIsIndependentAndConcurrent(t *testing.T) {
	for name, rejected := range map[string]bool{"expired": false, "rejected before expiry": true} {
		t.Run(name, func(t *testing.T) {
			svc, reader, repo := newExcelGrantTestService(t)
			before := maps.Clone(reader.account.Credentials)
			expiry := time.Now().Add(-time.Hour)
			if rejected {
				expiry = time.Now().Add(time.Hour)
			}
			stale := excelTestCredentials("old", expiry)
			storeExcelTestCredentials(t, svc, repo, stale)
			if rejected {
				require.NoError(t, svc.invalidateExcelAccessToken(context.Background(), reader.account.ID, excelMapString(stale, "access_token"), nil))
			}
			fresh := excelTestCredentials("fresh", time.Now().Add(time.Hour))
			client := &excelRefreshTestClient{credentials: fresh}
			svc.oauth.oauthClient = client
			var wg sync.WaitGroup
			results := make(chan string, 12)
			errs := make(chan error, 12)
			for i := 0; i < 12; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					token, err := svc.ExcelAccessToken(context.Background(), reader.account, nil)
					results <- token
					errs <- err
				}()
			}
			wg.Wait()
			close(results)
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}
			for token := range results {
				require.Equal(t, fresh["access_token"], token)
			}
			require.Equal(t, 1, client.calls)
			require.Equal(t, openai.ExcelClientID, client.clientID)
			require.Equal(t, stale["refresh_token"], client.refreshToken)
			require.Equal(t, before, reader.account.Credentials)
			plain, err := svc.encryptor.Decrypt(repo.ciphertext)
			require.NoError(t, err)
			require.Contains(t, plain, fresh["refresh_token"])
		})
	}
}

func TestExcelOAuthRefreshFailureDoesNotReturnUnsavedToken(t *testing.T) {
	svc, reader, repo := newExcelGrantTestService(t)
	storeExcelTestCredentials(t, svc, repo, excelTestCredentials("expired", time.Now().Add(-time.Hour)))
	previous := repo.ciphertext
	repo.replaceErr = errors.New("disk full")
	svc.oauth.oauthClient = &excelRefreshTestClient{credentials: excelTestCredentials("fresh", time.Now().Add(time.Hour))}
	token, err := svc.ExcelAccessToken(context.Background(), reader.account, nil)
	require.Error(t, err)
	require.Empty(t, token)
	require.Equal(t, previous, repo.ciphertext)
	require.Equal(t, "old-access", reader.account.GetCredential("access_token"))
}

func TestExcelOAuthReadyTokenDoesNotLockButRefreshFailsClosed(t *testing.T) {
	svc, reader, repo := newExcelGrantTestService(t)
	ready := excelTestCredentials("fresh", time.Now().Add(time.Hour))
	storeExcelTestCredentials(t, svc, repo, ready)
	cache := &excelRefreshLockTestCache{err: errors.New("Redis unavailable")}
	token, err := svc.ExcelAccessToken(context.Background(), reader.account, cache)
	require.NoError(t, err)
	require.Equal(t, ready["access_token"], token)
	require.Zero(t, cache.acquisitions)
	storeExcelTestCredentials(t, svc, repo, excelTestCredentials("expired", time.Now().Add(-time.Hour)))
	client := &excelRefreshTestClient{credentials: ready}
	svc.oauth.oauthClient = client
	token, err = svc.ExcelAccessToken(context.Background(), reader.account, cache)
	require.Error(t, err)
	require.Empty(t, token)
	require.Zero(t, client.calls)
	require.Equal(t, "openai:excel:account:42", cache.key)
	require.NotEqual(t, OpenAITokenCacheKey(reader.account), cache.key)
}

func TestExcelOAuthRejectedTokenUsesRefreshLock(t *testing.T) {
	svc, reader, repo := newExcelGrantTestService(t)
	credentials := excelTestCredentials("rejected", time.Now().Add(time.Hour))
	storeExcelTestCredentials(t, svc, repo, credentials)
	require.NoError(t, svc.invalidateExcelAccessToken(context.Background(), reader.account.ID, excelMapString(credentials, "access_token"), nil))
	marked := repo.ciphertext
	client := &excelRefreshTestClient{credentials: excelTestCredentials("rotated", time.Now().Add(time.Hour))}
	svc.oauth.oauthClient = client
	cache := &excelRefreshLockTestCache{granted: false}
	token, err := svc.ExcelAccessToken(context.Background(), reader.account, cache)
	require.Error(t, err)
	require.Empty(t, token)
	require.Zero(t, client.calls)
	require.Equal(t, marked, repo.ciphertext)
	cache.granted = true
	token, err = svc.ExcelAccessToken(context.Background(), reader.account, cache)
	require.NoError(t, err)
	require.Equal(t, client.credentials["access_token"], token)
	require.Equal(t, 1, client.calls)
}

type excelAuditInterleavingClient struct {
	excelRefreshTestClient
	interleave func()
}

func (c *excelAuditInterleavingClient) RefreshTokenWithClientID(ctx context.Context, rt, proxy, client string) (*openai.TokenResponse, error) {
	c.interleave()
	return c.excelRefreshTestClient.RefreshTokenWithClientID(ctx, rt, proxy, client)
}

func TestExcelOAuthRecovery401DuringRefreshPreservesRotatedRT(t *testing.T) {
	svc, reader, repo := newExcelGrantTestService(t)
	old := excelTestCredentials("old", time.Now().Add(time.Minute))
	fresh := excelTestCredentials("rotated", time.Now().Add(time.Hour))
	storeExcelTestCredentials(t, svc, repo, old)
	svc.oauth.oauthClient = &excelAuditInterleavingClient{
		excelRefreshTestClient: excelRefreshTestClient{credentials: fresh},
		// Model a 401 arriving after the refresh reads its CAS snapshot but
		// before the token endpoint returns its rotated credentials.
		interleave: func() {
			require.NoError(t, svc.invalidateExcelAccessToken(context.Background(), reader.account.ID, excelMapString(old, "access_token"), nil))
		},
	}
	token, err := svc.ExcelAccessToken(context.Background(), reader.account, nil)
	plain, decodeErr := svc.encryptor.Decrypt(repo.ciphertext)
	require.NoError(t, decodeErr)
	t.Logf("rotated RT persisted=%t; refresh error=%v", strings.Contains(plain, excelMapString(fresh, "refresh_token")), err)
	require.NoError(t, err, "a 401 must not invalidate the in-flight refresh CAS snapshot")
	require.Equal(t, fresh["access_token"], token)
	require.Contains(t, plain, fresh["refresh_token"])
}

func TestExcelOAuthRecoveryInvalidGrantAfterConcurrentReplacement(t *testing.T) {
	svc, reader, repo := newExcelGrantTestService(t)
	old := excelTestCredentials("old", time.Now().Add(-time.Minute))
	fresh := excelTestCredentials("replaced", time.Now().Add(time.Hour))
	storeExcelTestCredentials(t, svc, repo, old)
	svc.oauth.oauthClient = &excelAuditInterleavingClient{
		excelRefreshTestClient: excelRefreshTestClient{err: errors.New("invalid_grant")},
		interleave:             func() { storeExcelTestCredentials(t, svc, repo, fresh) },
	}
	token, err := svc.ExcelAccessToken(context.Background(), reader.account, nil)
	require.NoError(t, err, "reread the newer RT before reporting reauthorization")
	require.Equal(t, fresh["access_token"], token)
}

func TestExcelOAuthRecovery401DistributedLock(t *testing.T) {
	for _, tc := range []struct {
		name    string
		granted bool
		err     error
	}{
		{name: "owned by another replica"},
		{name: "Redis unavailable", err: errors.New("unavailable")},
		{name: "acquired", granted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, reader, repo := newExcelGrantTestService(t)
			credentials := excelTestCredentials("current", time.Now().Add(time.Hour))
			storeExcelTestCredentials(t, svc, repo, credentials)
			before := repo.ciphertext
			cache := &excelRefreshLockTestCache{granted: tc.granted, err: tc.err}
			gateway := openAIClientToolsTestService(nil)
			svc.oauth.excelGrants = svc
			gateway.openAITokenProvider = &OpenAITokenProvider{tokenCache: cache, openAIOAuthService: svc.oauth}
			gateway.handleExcelBPSUnauthorized(context.Background(), reader.account, 401, nil, nil, excelMapString(credentials, "access_token"))
			require.Equal(t, 1, cache.acquisitions)
			require.Equal(t, "openai:excel:account:42", cache.key)
			if tc.granted {
				require.NotEqual(t, before, repo.ciphertext)
				require.Equal(t, 1, cache.releases)
			} else {
				require.Equal(t, before, repo.ciphertext, "lock contention/error must not mutate a refresh snapshot")
				require.Zero(t, cache.releases, "never release another replica's lock")
			}
			// Failure/contended acquisition must also release the local mutex.
			unlock, err := lockExcelCredentials(context.Background(), reader.account.ID, nil, false)
			require.NoError(t, err)
			require.NotNil(t, unlock)
			unlock()
		})
	}
}

func TestExcelOAuthRecoveryConcurrentReplacementWins(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprintf("invalid_grant=%t", failure), func(t *testing.T) {
			for _, rejected := range []bool{false, true} {
				t.Run(fmt.Sprintf("replacement_rejected=%t", rejected), func(t *testing.T) {
					svc, reader, repo := newExcelGrantTestService(t)
					old := excelTestCredentials("old", time.Now().Add(-time.Hour))
					rotated := excelTestCredentials("refresh-response", time.Now().Add(time.Hour))
					replacement := excelTestCredentials("new-login", time.Now().Add(time.Hour))
					if rejected {
						replacement[excelAccessTokenRejectedKey] = true
					}
					storeExcelTestCredentials(t, svc, repo, old)
					var refreshErr error
					if failure {
						refreshErr = errors.New("invalid_grant")
					}
					svc.oauth.oauthClient = &excelAuditInterleavingClient{
						excelRefreshTestClient: excelRefreshTestClient{credentials: rotated, err: refreshErr},
						interleave:             func() { storeExcelTestCredentials(t, svc, repo, replacement) },
					}
					token, err := svc.ExcelAccessToken(context.Background(), reader.account, nil)
					if rejected {
						require.Error(t, err)
						require.Empty(t, token)
					} else {
						require.NoError(t, err)
						require.Equal(t, replacement["access_token"], token)
					}
					plain, err := svc.encryptor.Decrypt(repo.ciphertext)
					require.NoError(t, err)
					require.Contains(t, plain, replacement["refresh_token"])
					require.NotContains(t, plain, rotated["refresh_token"])
				})
			}
		})
	}
}

func TestExcelOAuthRecoveryRejectsReplacementIdentity(t *testing.T) {
	svc, reader, repo := newExcelGrantTestService(t)
	storeExcelTestCredentials(t, svc, repo, excelTestCredentials("old", time.Now().Add(-time.Hour)))
	replacement := excelTestCredentials("other", time.Now().Add(time.Hour))
	replacement["chatgpt_account_id"] = "different-account"
	svc.oauth.oauthClient = &excelAuditInterleavingClient{
		excelRefreshTestClient: excelRefreshTestClient{err: errors.New("invalid_grant")},
		interleave:             func() { storeExcelTestCredentials(t, svc, repo, replacement) },
	}
	token, err := svc.ExcelAccessToken(context.Background(), reader.account, nil)
	require.Error(t, err)
	require.Empty(t, token)
	require.NotEmpty(t, repo.ciphertext, "do not delete an independently replaced grant")
}

func TestExcelBPSActivationWaitsForIndependentGrant(t *testing.T) {
	grant, reader, repo := newExcelGrantTestService(t)
	gateway := &OpenAIGatewayService{openAITokenProvider: &OpenAITokenProvider{openAIOAuthService: grant.oauth}}
	before := maps.Clone(reader.account.Credentials)
	require.NoError(t, gateway.PrepareExcelBPSActivation(context.Background(), reader.account))
	require.True(t, reader.account.Extra[ExcelBPSAuthorizationPendingKey].(bool))
	require.False(t, reader.account.IsExcelBPSEnabled())
	raw, err := json.Marshal(excelTestCredentials("ready", time.Now().Add(time.Hour)))
	require.NoError(t, err)
	repo.ciphertext, err = grant.encryptor.Encrypt(string(raw))
	require.NoError(t, err)
	require.NoError(t, gateway.PrepareExcelBPSActivation(context.Background(), reader.account))
	require.True(t, reader.account.IsExcelBPSEnabled())
	require.Equal(t, before, reader.account.Credentials)
	repo.ciphertext = "invalid"
	require.Error(t, gateway.PrepareExcelBPSActivation(context.Background(), reader.account))
	reader.account.Extra["openai_excel_bps"] = false
	require.NoError(t, gateway.PrepareExcelBPSActivation(context.Background(), reader.account))
}

func TestCodexLoginImportPreservesPasswordAndRejectsBadRowsWithoutSecrets(t *testing.T) {
	const secret = "JBSWY3DPEHPK3PXP"
	entries, err := ParseCodexLoginEntries("one@example.com----space $ pass----part!----" + secret + "\r\n\r\ntwo@example.com----second-pass----")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, "space $ pass----part!", entries[0].Password)
	require.Equal(t, secret, entries[0].TOTPSecret)
	require.Empty(t, entries[1].TOTPSecret)
	raw, err := json.Marshal(entries)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "space $ pass")
	require.NotContains(t, string(raw), secret)
	for _, invalid := range []string{
		"one@example.com----sensitive-password----123456",
		"not-an-email----sensitive-password----" + secret,
		"one@example.com--------" + secret,
		"one@example.com----sensitive-password----\nONE@example.com----another-password----",
		strings.Repeat("x", (256<<10)+1),
	} {
		_, err := ParseCodexLoginEntries(invalid)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "sensitive-password")
		require.NotContains(t, err.Error(), secret)
	}
}

func TestCodexLoginRecoveryOnlyUsesAuthenticationFailures(t *testing.T) {
	for _, tc := range []struct {
		message  string
		expected bool
	}{
		{CodexAuthorizationPending, true},
		{"Token revoked (401): account authentication permanently revoked", true},
		{"Unauthorized (401): account authentication failed permanently", true},
		{`Authentication failed (401): {"error":{"code":"token_revoked"},"status":401}`, true},
		{"Authentication failed (401): expired credentials", true},
		{"Authentication failed (401): account_deactivated", false},
		{"Authentication failed (401): cyber_policy", false},
		{"Authentication failed (403): permission denied", false},
		{"untrusted text: Authentication failed (401): token_revoked", false},
		{"Token refresh failed (non-retryable): refresh_token_reused", true},
		{"Token refresh failed (non-retryable): invalid_grant", true},
		{"Token refresh failed (non-retryable): invalid_client", false},
		{"Cyber policy blocked (403)", false},
		{"account_deactivated", false},
		{"rate limit 429", false},
		{"network timeout", false},
		{"untrusted text: refresh_token_reused", false},
	} {
		require.Equal(t, tc.expected, CodexLoginCanRecoverState(StatusError, tc.message), tc.message)
		require.False(t, CodexLoginCanRecoverState(StatusDisabled, tc.message))
		require.False(t, CodexLoginCanRecoverState(StatusActive, tc.message))
	}
}

func codexLoginTestCredentials(t *testing.T, clientID, workspace, user, email string, plans ...string) map[string]any {
	t.Helper()
	jwt := func(claims map[string]any) string {
		b, err := json.Marshal(claims)
		require.NoError(t, err)
		return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".synthetic"
	}
	plan := "pro"
	if len(plans) > 0 {
		plan = plans[0]
	}
	auth := map[string]any{"chatgpt_account_id": workspace, "chatgpt_user_id": user, "chatgpt_plan_type": plan}
	return map[string]any{"client_id": clientID, "refresh_token": "synthetic-refresh",
		"access_token": jwt(map[string]any{"client_id": clientID, "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": auth}),
		"id_token":     jwt(map[string]any{"aud": []string{clientID}, "exp": time.Now().Add(time.Hour).Unix(), "email": email, "https://api.openai.com/auth": auth})}
}
func TestCodexLoginRejectsExcelAndDifferentIdentity(t *testing.T) {
	stored := map[string]any{"email": "one@example.com", "chatgpt_account_id": "workspace", "chatgpt_user_id": "user"}
	valid := codexLoginTestCredentials(t, openai.ClientID, "workspace", "user", "one@example.com")
	require.NoError(t, ValidateCodexLoginIdentity(stored, valid))
	require.NoError(t, ValidateCodexLoginIdentity(map[string]any{"email": "one@example.com"}, valid), "initial authorization binds the imported email")
	for _, args := range [][4]string{
		{openai.ExcelClientID, "workspace", "user", "one@example.com"},
		{openai.ClientID, "other-workspace", "user", "one@example.com"},
		{openai.ClientID, "workspace", "other-user", "one@example.com"},
		{openai.ClientID, "workspace", "user", "other@example.com"},
	} {
		require.Error(t, ValidateCodexLoginIdentity(stored, codexLoginTestCredentials(t, args[0], args[1], args[2], args[3])))
	}
	wrongID := codexLoginTestCredentials(t, openai.ClientID, "other-workspace", "user", "one@example.com")
	valid["id_token"] = wrongID["id_token"]
	require.Error(t, ValidateCodexLoginIdentity(stored, valid))
	for _, tc := range []struct {
		name, from, workspace, user, email, plan string
		allowed                                  bool
	}{
		{"same person personal fallback", "workspace", "personal", "user", "one@example.com", "free", true},
		{"personal paid plan", "workspace", "personal", "user", "one@example.com", "pro", true},
		{"no fallback evidence", "", "personal", "user", "one@example.com", "free", false},
		{"wrong original", "another", "personal", "user", "one@example.com", "free", false},
		{"another person", "workspace", "personal", "other-user", "one@example.com", "free", false},
		{"another email", "workspace", "personal", "user", "other@example.com", "free", false},
		{"another team", "workspace", "team", "user", "one@example.com", "business", false},
		{"unknown plan", "workspace", "personal", "user", "one@example.com", "", false},
		{"not a workspace change", "workspace", "workspace", "user", "one@example.com", "free", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := codexLoginTestCredentials(t, openai.ClientID, tc.workspace, tc.user, tc.email, tc.plan)
			candidate[CodexWorkspaceFallbackFrom] = tc.from
			candidate["plan_type"] = "free" // Unverified metadata cannot turn a team token into a personal token.
			err := ValidateCodexLoginIdentity(stored, candidate)
			if tc.allowed {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	personal := codexLoginTestCredentials(t, openai.ClientID, "personal", "user", "one@example.com", "free")
	personal[CodexWorkspaceFallbackFrom] = "workspace"
	require.Error(t, ValidateCodexLoginIdentity(map[string]any{"email": "one@example.com", "chatgpt_account_id": "workspace"}, personal))
	require.Equal(t, "workspace", stored["chatgpt_account_id"], "fallback validation must not modify the stored snapshot")
}

func TestCodexLoginPendingAccountCannotBeScheduled(t *testing.T) {
	a, err := buildAccountForCreate(&CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"email": "one@example.com"}, AwaitingCodexAuthorization: true}, nil)
	require.NoError(t, err)
	require.Equal(t, StatusError, a.Status)
	require.Equal(t, CodexAuthorizationPending, a.ErrorMessage)
	require.False(t, a.Schedulable)
	require.False(t, a.IsSchedulable())
}

type codexLoginTestRepo struct {
	*excelLoginTestRepo
	CodexLoginRepository
	finished    int
	scans       int
	allowFinish bool
	write       map[string]any
	astra       *CodexLoginAstraResult
}

func (r *codexLoginTestRepo) GetCodexLoginConfig(ctx context.Context, id int64) (*ExcelLoginConfig, error) {
	return r.GetExcelLoginConfig(ctx, id)
}
func (r *codexLoginTestRepo) SaveCodexLoginConfig(ctx context.Context, c *ExcelLoginConfig, revision int64) (bool, error) {
	return r.SaveExcelLoginConfig(ctx, c, revision)
}
func (r *codexLoginTestRepo) GetCodexLoginTask(context.Context, int64) (*ExcelLoginTask, error) {
	return r.task, nil
}
func (r *codexLoginTestRepo) GetLoginTaskByID(context.Context, int64) (*ExcelLoginTask, error) {
	return r.task, nil
}
func (r *codexLoginTestRepo) ClaimCodexLogin(context.Context, string) (*ExcelLoginTask, error) {
	r.claimed++
	return r.task, nil
}
func (r *codexLoginTestRepo) HasActiveCodexLogins(context.Context) (bool, error) {
	r.scans++
	return false, nil
}
func (r *codexLoginTestRepo) ListCodexLoginCandidates(context.Context) ([]int64, error) {
	return nil, nil
}
func (r *codexLoginTestRepo) FinishCodexLogin(_ context.Context, _ int64, _ string, c map[string]any, astra *CodexLoginAstraResult) (bool, error) {
	r.finished++
	r.write = c
	r.astra = astra
	return r.allowFinish, nil
}

func TestCodexLoginSecretsVerificationAndBPSIsolation(t *testing.T) {
	grant, reader, base := newExcelGrantTestService(t)
	repo := &codexLoginTestRepo{excelLoginTestRepo: &excelLoginTestRepo{excelReauthTestRepo: base}, allowFinish: true}
	grant.repo = repo
	grant.settings = &settingRepoStub{values: map[string]string{SettingKeyExcelBPSEnabled: "false"}}
	oauth := grant.oauth
	oauth.codexLoginAstraProbe = func(context.Context, *Account) CodexLoginAstraResult {
		return CodexLoginAstraResult{Verdict: "healthy", SampledAt: time.Now()}
	}
	ctx := context.Background()
	disabled := false
	preferred := "first-workspace"
	input := CodexLoginInput{PreferredWorkspaceID: &preferred, Enabled: &disabled, Password: "synthetic private $ password", TOTPSecret: "JBSWY3DPEHPK3PXP"}
	state, err := oauth.SaveCodexLogin(ctx, 42, input)
	require.NoError(t, err)
	require.False(t, state.Config.Enabled)
	require.True(t, state.Config.PasswordConfigured)
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	for _, secret := range []string{input.Password, input.TOTPSecret, "encrypted:"} {
		require.NotContains(t, string(raw), secret)
	}
	repo.task = &ExcelLoginTask{ID: 9, AccountID: 42, OAuthProfile: "codex", Status: "running", WorkerID: "worker", ConfigRevision: 1}
	claim, err := oauth.ClaimCodexLogin(ctx, "worker")
	require.NoError(t, err)
	require.NotNil(t, claim)
	require.Equal(t, "codex", claim.OAuthProfile)
	require.Equal(t, input.Password, claim.Password)
	require.Equal(t, "acct-1", claim.ExpectedWorkspace)
	require.Equal(t, preferred, claim.PreferredWorkspaceID)
	excel, err := oauth.ClaimExcelLogin(ctx, "worker")
	require.NoError(t, err)
	require.Nil(t, excel)
	input.PreferredWorkspaceID = nil // Older clients preserve the saved preference.
	input.Password = ""
	input.TOTPSecret = ""
	input.Revision = 1
	state, err = oauth.SaveCodexLogin(ctx, 42, input)
	require.NoError(t, err)
	require.EqualValues(t, 1, state.Config.Revision, "unchanged settings must not cancel work or reset the failure budget")
	require.Equal(t, preferred, state.Config.PreferredWorkspaceID)
	svc := &OpenAIGatewayService{openAITokenProvider: &OpenAITokenProvider{openAIOAuthService: oauth}}
	svc.syncExcelBPS403Recovery(ctx)
	require.Equal(t, 1, repo.scans, "Codex queue must scan even with BPS disabled")
	verified, invalidated := 0, 0
	oauth.codexLoginVerify = func(_ context.Context, a *Account) error {
		verified++
		require.Equal(t, reader.account.ID, a.ID)
		return errors.New("temporary failure")
	}
	oauth.codexLoginCredentialsChanged = func(context.Context, *Account) { invalidated++ }
	credentials := codexLoginTestCredentials(t, openai.ClientID, "acct-1", "user-1", "user@example.com")
	_, err = oauth.CompleteCodexLogin(ctx, 9, "other-worker", credentials)
	require.Error(t, err)
	require.Zero(t, verified)
	require.Zero(t, repo.finished)
	_, err = oauth.CompleteCodexLogin(ctx, 9, "worker", credentials)
	require.Error(t, err)
	require.Equal(t, 1, verified)
	require.Zero(t, repo.finished)
	require.Zero(t, invalidated)
	require.Contains(t, repo.failures, "verification_failed")
	oauth.codexLoginVerify = func(context.Context, *Account) error { verified++; return nil }
	repo.allowFinish = false
	_, err = oauth.CompleteCodexLogin(ctx, 9, "worker", credentials)
	require.Error(t, err)
	require.Zero(t, invalidated)
	repo.allowFinish = true
	result, err := oauth.CompleteCodexLogin(ctx, 9, "worker", credentials)
	require.NoError(t, err)
	require.Equal(t, "succeeded", result.Status)
	require.Equal(t, 1, invalidated)
	require.Equal(t, credentials["access_token"], repo.write["access_token"])
	require.Equal(t, "old-access", reader.account.GetCredential("access_token"), "stored snapshot must not be mutated before the repository CAS")
	personal := codexLoginTestCredentials(t, openai.ClientID, "personal", "user-1", "user@example.com", "free")
	personal[CodexWorkspaceFallbackFrom] = "acct-1"
	personal[CodexPersonalWorkspaceOnly] = true
	oauth.codexLoginVerify = func(_ context.Context, candidate *Account) error {
		require.Equal(t, "personal", candidate.GetCredential("chatgpt_account_id"))
		require.Equal(t, "free", candidate.GetCredential("plan_type"))
		return nil
	}
	result, err = oauth.CompleteCodexLogin(ctx, 9, "worker", personal)
	require.NoError(t, err)
	require.Equal(t, "succeeded_personal_workspace_only", result.Stage)
	require.Equal(t, "acct-1", repo.write[CodexWorkspaceFallbackFrom])
	require.Equal(t, "", repo.write["organization_id"])
	require.Equal(t, "", repo.write["subscription_expires_at"])
	require.Nil(t, repo.write["subscription_is_delinquent"])
	require.Equal(t, "acct-1", reader.account.GetCredential("chatgpt_account_id"))
	for _, mode := range []string{OpenAIAuthModeAgentIdentity, OpenAIAuthModePersonalAccessToken} {
		reader.account.Credentials["auth_mode"] = mode
		_, err = oauth.CodexLoginStatus(ctx, 42)
		require.Error(t, err)
	}
}

func TestCodexLoginAstraGateBeforeCredentialCommit(t *testing.T) {
	for _, tc := range []struct {
		name, verdict, stage string
		initial, unavailable bool
	}{
		{"healthy recovery", "healthy", "succeeded", false, false},
		{"degraded recovery", "degraded", "succeeded_astra_degraded", false, false},
		{"unknown recovery", "unknown", "succeeded_astra_unknown", false, false},
		{"verifier unavailable", "unknown", "succeeded_astra_unknown", false, true},
		{"first authorization unchanged", "", "succeeded", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grant, reader, base := newExcelGrantTestService(t)
			repo := &codexLoginTestRepo{excelLoginTestRepo: &excelLoginTestRepo{excelReauthTestRepo: base}, allowFinish: true}
			grant.repo = repo
			if tc.initial {
				reader.account.Credentials = map[string]any{"email": "user@example.com"}
				reader.account.ErrorMessage = CodexAuthorizationPending
			}
			repo.config = &ExcelLoginConfig{AccountID: 42, LoginEmail: "user@example.com", Revision: 1}
			repo.task = &ExcelLoginTask{ID: 9, AccountID: 42, OAuthProfile: "codex", Status: "running", WorkerID: "worker", ConfigRevision: 1}
			verified, probed := false, false
			grant.oauth.codexLoginVerify = func(context.Context, *Account) error { verified = true; return nil }
			credentials := codexLoginTestCredentials(t, openai.ClientID, "acct-1", "user-1", "user@example.com")
			if !tc.unavailable {
				grant.oauth.codexLoginAstraProbe = func(ctx context.Context, candidate *Account) CodexLoginAstraResult {
					probed = true
					require.True(t, verified)
					require.Zero(t, repo.finished, "no new token or routing state is committed before the probe")
					require.Equal(t, credentials["access_token"], candidate.GetCredential("access_token"))
					_, bounded := ctx.Deadline()
					require.True(t, bounded)
					return CodexLoginAstraResult{Verdict: tc.verdict, SampledAt: time.Now()}
				}
			}
			result, err := grant.oauth.CompleteCodexLogin(context.Background(), 9, "worker", credentials)
			require.NoError(t, err)
			require.Equal(t, "succeeded", result.Status)
			require.Equal(t, tc.stage, result.Stage)
			require.Equal(t, !tc.initial && !tc.unavailable, probed)
			require.Equal(t, 1, repo.finished)
			require.Equal(t, credentials["access_token"], repo.write["access_token"])
			require.Empty(t, repo.failures, "a quality failure must not schedule another browser login")
			if tc.initial {
				require.Nil(t, repo.astra)
			} else {
				require.NotNil(t, repo.astra)
				require.Equal(t, tc.verdict == "healthy", repo.astra.Healthy())
			}
		})
	}
}

func TestCodexLoginDefaultRecoveryPreservesExistingChoices(t *testing.T) {
	on, off := true, false
	for _, tc := range []struct {
		name              string
		existing, enabled *bool
		want              bool
	}{
		{"new configuration defaults on", nil, nil, true},
		{"new configuration can opt out", nil, &off, false},
		{"new configuration explicitly enabled", nil, &on, true},
		{"existing disabled stays disabled", &off, nil, false},
		{"existing enabled stays enabled", &on, nil, true},
		{"existing disabled can be enabled", &off, &on, true},
		{"existing enabled can be disabled", &on, &off, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grant, _, base := newExcelGrantTestService(t)
			repo := &codexLoginTestRepo{excelLoginTestRepo: &excelLoginTestRepo{excelReauthTestRepo: base}}
			grant.repo = repo
			if tc.existing != nil {
				repo.config = &ExcelLoginConfig{AccountID: 42, Enabled: *tc.existing, LoginEmail: "user@example.com", PasswordCiphertext: "encrypted", Revision: 3}
			}
			state, err := grant.oauth.CodexLoginStatus(context.Background(), 42)
			require.NoError(t, err)
			if tc.existing == nil {
				require.True(t, state.Config.Enabled)
				require.Nil(t, repo.config, "reading defaults must not create a saved configuration")
			} else {
				require.Equal(t, *tc.existing, state.Config.Enabled)
			}
			state, err = grant.oauth.SaveCodexLogin(context.Background(), 42, CodexLoginInput{
				Enabled: tc.enabled, Password: "synthetic-password", Revision: state.Config.Revision,
			})
			require.NoError(t, err)
			require.Equal(t, tc.want, state.Config.Enabled)
			require.Equal(t, tc.want, repo.config.Enabled)
		})
	}
}

func TestCodexLoginFailureDiagnosticsNeverPersistArbitraryOutput(t *testing.T) {
	valid := "codex_browser_failed stage=protocol_connecting action=submit_email reason=timeout elapsed_ms=16130 action_ms=15001 http_status=200 response=document"
	protocol := strings.Replace(valid, "codex_browser_failed", "codex_protocol_failed", 1)
	require.NotNil(t, ParseCodexBrowserDiagnostic(protocol))
	require.Equal(t, "timeout", safeLoginFailureCode(protocol))
	diagnostic := ParseCodexBrowserDiagnostic(valid)
	require.NotNil(t, diagnostic)
	require.Equal(t, "submit_email", diagnostic.Action)
	require.Equal(t, 16130, diagnostic.ElapsedMS)
	require.Equal(t, "timeout", safeLoginFailureCode(valid))
	grant, _, base := newExcelGrantTestService(t)
	repo := &excelLoginTestRepo{excelReauthTestRepo: base}
	grant.repo = repo
	require.NoError(t, grant.oauth.FailExcelLogin(context.Background(), 8, "worker", valid))
	require.Equal(t, valid, repo.failures[0])
	for _, invalid := range []string{
		valid + " password=PRIVATE",
		strings.Replace(valid, "submit_email", "private_email", 1),
		strings.Replace(valid, "protocol_connecting", "private_stage", 1),
		strings.Replace(valid, "reason=timeout", "reason=private_reason", 1),
		strings.Replace(valid, "elapsed_ms=16130", "elapsed_ms=360001", 1),
		strings.Replace(valid, "action_ms=15001", "action_ms=16131", 1),
		strings.Replace(valid, "http_status=200", "http_status=999", 1),
		strings.Replace(valid, "response=document", "response=none", 1),
	} {
		require.Nil(t, ParseCodexBrowserDiagnostic(invalid))
		require.NoError(t, grant.oauth.FailExcelLogin(context.Background(), 8, "worker", invalid))
		require.Equal(t, "worker_failed", repo.failures[len(repo.failures)-1])
	}
	for _, tc := range []struct{ input, want string }{
		{"browser_unavailable", "browser_unavailable"},
		{"browser_start_failed", "browser_start_failed"},
		{"browser_dependency_failed", "browser_dependency_failed"},
		{"workspace_unavailable", "workspace_unavailable"},
		{"workspace_selection_required", "workspace_selection_required"},
		{"workspace_unavailable private-secret", "worker_failed"},
		{"browser_start_failed private-secret", "worker_failed"},
		{"local_protocol_failed stage=web_login reason=password_rejected elapsed_ms=10 exit=1", "password_rejected"},
		{"local_protocol_failed stage=mfa_challenge reason=totp_rejected elapsed_ms=20 exit=1", "totp_rejected"},
		{"local_protocol_failed stage=workspace_select reason=workspace_mismatch elapsed_ms=20 exit=1", "identity_mismatch"},
		{"local_protocol_failed stage=web_login reason=account_deactivated elapsed_ms=20 exit=1", "account_blocked"},
		{"local_protocol_failed stage=web_login reason=transport_error elapsed_ms=20 exit=1", "transport_error"},
		{"local_protocol_failed stage=web_login reason=password_rejected elapsed_ms=10 exit=1 private-secret", "worker_failed"},
		{"password_rejected private-secret", "worker_failed"},
	} {
		require.Equal(t, tc.want, safeLoginFailureCode(tc.input))
	}
	require.False(t, CodexLoginCanRecoverState(StatusError, "Unauthorized (401): account_deactivated"))
	require.False(t, CodexLoginCanRecoverState(StatusError, "Unauthorized (401): cyber_policy"))
}

func TestCodexLoginFirstWorkspacePreferenceAndPersonalNotice(t *testing.T) {
	for _, raw := range []string{"", " workspace-id_1 "} {
		id, err := NormalizeCodexPreferredWorkspaceID(raw)
		require.NoError(t, err)
		require.Equal(t, strings.TrimSpace(raw), id)
	}
	for _, raw := range []string{"name with spaces", "https://example.com", strings.Repeat("a", 129)} {
		_, err := NormalizeCodexPreferredWorkspaceID(raw)
		require.Error(t, err)
	}
	for _, tc := range []struct {
		name, preferred, plan, stage string
		reject                       bool
	}{
		{"automatic business", "", "team", "succeeded", false},
		{"specified business", "workspace", "business", "succeeded", false},
		{"wrong specified workspace", "another", "team", "", true},
		{"only personal", "", "free", "succeeded_personal_workspace_only", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grant, reader, base := newExcelGrantTestService(t)
			repo := &codexLoginTestRepo{excelLoginTestRepo: &excelLoginTestRepo{excelReauthTestRepo: base}, allowFinish: true}
			grant.repo = repo
			reader.account.Credentials = map[string]any{"email": "user@example.com"}
			reader.account.ErrorMessage = CodexAuthorizationPending
			repo.config = &ExcelLoginConfig{AccountID: 42, LoginEmail: "user@example.com", Revision: 1, PreferredWorkspaceID: tc.preferred}
			repo.task = &ExcelLoginTask{ID: 9, AccountID: 42, OAuthProfile: "codex", Status: "running", WorkerID: "worker", ConfigRevision: 1}
			verified := false
			grant.oauth.codexLoginVerify = func(context.Context, *Account) error { verified = true; return nil }
			credentials := codexLoginTestCredentials(t, openai.ClientID, "workspace", "user", "user@example.com", tc.plan)
			if tc.plan == "free" {
				credentials[CodexPersonalWorkspaceOnly] = true
			}
			result, err := grant.oauth.CompleteCodexLogin(context.Background(), 9, "worker", credentials)
			if tc.reject {
				require.Error(t, err)
				require.False(t, verified)
				require.Zero(t, repo.finished)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.stage, result.Stage)
				require.True(t, verified)
				require.Nil(t, repo.astra)
				if tc.plan == "free" {
					require.Equal(t, true, repo.write[CodexPersonalWorkspaceOnly])
				}
			}
		})
	}
	team := codexLoginTestCredentials(t, openai.ClientID, "workspace", "user", "user@example.com", "team")
	team[CodexPersonalWorkspaceOnly] = true
	require.Error(t, ValidateCodexLoginIdentity(map[string]any{"email": "user@example.com"}, team))
}
