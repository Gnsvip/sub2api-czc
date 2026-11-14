package repository

import (
	"context"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProxyPoolPersistenceOrderingAndRollback(t *testing.T) {
	_, client := newUserEntRepo(t)
	ctx := context.Background()
	ids := []int64{}
	for range 3 {
		proxy, err := client.Proxy.Create().SetName("proxy").SetProtocol("http").SetHost("127.0.0.1").SetPort(8080).Save(ctx)
		require.NoError(t, err)
		ids = append(ids, proxy.ID)
	}
	account := &service.Account{Name: "pool", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{}, Extra: map[string]any{}, Status: service.StatusActive, Concurrency: 2, ProxyIDs: []int64{ids[2], ids[0], ids[1]}}
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	require.NoError(t, createAccountRecord(ctx, tx.Client(), account))
	require.NoError(t, tx.Commit())
	repo := newAccountRepositoryWithSQL(client, nil, nil)
	got, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, account.ProxyIDs, got.ProxyIDs)
	require.Len(t, got.Proxies, 3)
	batch, err := repo.GetByIDs(ctx, []int64{account.ID})
	require.NoError(t, err)
	require.Equal(t, account.ProxyIDs, batch[0].ProxyIDs)
	tx, err = client.Tx(ctx)
	require.NoError(t, err)
	require.Error(t, replaceAccountProxyPool(ctx, tx.Client(), account.ID, []int64{ids[1], 999}))
	require.NoError(t, tx.Rollback())
	got, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, account.ProxyIDs, got.ProxyIDs)
	tx, err = client.Tx(ctx)
	require.NoError(t, err)
	require.NoError(t, replaceAccountProxyPool(ctx, tx.Client(), account.ID, []int64{ids[0]}))
	require.NoError(t, tx.Commit())
	got, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Empty(t, got.ProxyIDs)
}

func TestProxyPoolCreateRespectsCallerTransaction(t *testing.T) {
	_, client := newUserEntRepo(t)
	ctx := context.Background()
	_, err := client.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS scheduler_outbox(id INTEGER PRIMARY KEY,event_type TEXT,account_id BIGINT,group_id BIGINT,payload BLOB,dedup_key TEXT)`)
	require.NoError(t, err)
	_, err = client.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS proxy_test_outbox_dedup ON scheduler_outbox(dedup_key) WHERE dedup_key IS NOT NULL`)
	require.NoError(t, err)
	ids := []int64{}
	for range 2 {
		p, err := client.Proxy.Create().SetName("proxy").SetProtocol("http").SetHost("127.0.0.1").SetPort(8080).Save(ctx)
		require.NoError(t, err)
		ids = append(ids, p.ID)
	}
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	txctx := dbent.NewTxContext(ctx, tx)
	a := &service.Account{Name: "transaction", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{}, Extra: map[string]any{}, Status: service.StatusActive, Concurrency: 2, ProxyID: &ids[0], ProxyIDs: ids}
	repo := newAccountRepositoryWithSQL(client, nil, nil)
	require.NoError(t, repo.Create(txctx, a))
	got, err := repo.GetByID(txctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, ids, got.ProxyIDs)
	require.NoError(t, tx.Rollback())
	exists, err := client.Account.Query().Exist(ctx)
	require.NoError(t, err)
	require.False(t, exists)
	count, err := client.AccountProxy.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
}
