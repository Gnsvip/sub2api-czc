package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestCodexRecoverySessionCreditsAcrossGroups(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := &gatewayCache{rdb: client}
	at := time.Now().UTC()
	grant := at.Format(time.RFC3339Nano)
	require.NoError(t, cache.StartCodexRecovery(ctx, 17, at))
	base := service.CodexRecoveryBinding{GroupID: 1, Primary: "new", Legacy: "old", TTL: time.Hour, LegacyTTL: time.Minute, BindNow: true, WriteLegacy: true}
	require.NoError(t, cache.SetSessionAccountID(ctx, 1, "old", 37, time.Hour))
	ok, err := cache.ClaimCodexRecoverySession(ctx, 17, grant, base)
	require.NoError(t, err)
	require.False(t, ok)
	require.False(t, server.Exists(buildSessionKey(1, "new")))

	var wg sync.WaitGroup
	wins := make(chan bool, 32)
	for i := 0; i < cap(wins); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := base
			b.GroupID = int64(1 + i%3)
			b.Primary = fmt.Sprintf("session-%d", i)
			b.Legacy = ""
			claimed, err := cache.ClaimCodexRecoverySession(ctx, 17, grant, b)
			if err != nil {
				t.Errorf("claim: %v", err)
			}
			wins <- claimed
		}(i)
	}
	wg.Wait()
	close(wins)
	count := 0
	for claimed := range wins {
		if claimed {
			count++
		}
	}
	require.Equal(t, 2, count)
	require.NoError(t, cache.StartCodexRecovery(ctx, 17, at))
	available, err := cache.CodexRecoveryAvailable(ctx, 17, grant)
	require.NoError(t, err)
	require.False(t, available, "a duplicate callback cannot grant two more sessions")
	at = at.Add(time.Second)
	grant = at.Format(time.RFC3339Nano)
	require.NoError(t, cache.StartCodexRecovery(ctx, 17, at))
	base.BindNow, base.Legacy, base.Primary = false, "", "deferred"
	for i := 0; i < 3; i++ {
		ok, err := cache.ClaimCodexRecoverySession(ctx, 17, grant, base)
		require.NoError(t, err)
		require.True(t, ok)
	}
	require.False(t, server.Exists(buildSessionKey(1, "deferred")))
	base.Primary, base.BindNow = "another", true
	ok, err = cache.ClaimCodexRecoverySession(ctx, 17, grant, base)
	require.NoError(t, err)
	require.True(t, ok, "repeated requests for one session consume one credit")
	server.FastForward(service.CodexRecoveryWindow + time.Second)
	available, err = cache.CodexRecoveryAvailable(ctx, 17, grant)
	require.NoError(t, err)
	require.False(t, available)
}

func TestGatewayCacheLiveCallIdentityAndController(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	cache, ok := NewGatewayCache(client).(service.LiveCallStore)
	require.True(t, ok)
	otherInstance, ok := NewGatewayCache(client).(service.LiveCallStore)
	require.True(t, ok)
	record := &service.LiveCallRecord{
		CallID:                    "call_secret",
		CallHash:                  HashLiveCallID("call_secret"),
		AccountID:                 11,
		APIKeyID:                  22,
		UserID:                    33,
		GroupID:                   44,
		LeaseID:                   "lease",
		Model:                     "gpt-live-test",
		AttestationCiphertext:     "encrypted-attestation",
		UpstreamContextCiphertext: "encrypted-call-context",
		CreatedAt:                 time.Now(),
		ExpiresAt:                 time.Now().Add(time.Hour),
		Controller:                service.LiveControllerPending,
	}
	require.NoError(t, cache.SaveLiveCall(context.Background(), record, time.Hour))

	loaded, err := otherInstance.GetLiveCall(context.Background(), record.CallHash)
	require.NoError(t, err)
	require.Equal(t, record.CallID, loaded.CallID)
	require.Equal(t, record.AccountID, loaded.AccountID)
	require.Equal(t, record.AttestationCiphertext, loaded.AttestationCiphertext)
	require.Equal(t, record.UpstreamContextCiphertext, loaded.UpstreamContextCiphertext)

	claimed, err := cache.ClaimLiveController(context.Background(), record.CallHash, service.LiveControllerObserver, "observer-1")
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = cache.ClaimLiveController(context.Background(), record.CallHash, service.LiveControllerProxy, "proxy-1")
	require.NoError(t, err)
	require.True(t, claimed)
	controller, err := cache.GetLiveController(context.Background(), record.CallHash)
	require.NoError(t, err)
	require.Equal(t, service.LiveControllerProxy, controller)

	released, err := cache.ReleaseLiveController(context.Background(), record.CallHash, "proxy-1")
	require.NoError(t, err)
	require.True(t, released)
	closed, err := cache.MarkLiveCallClosed(context.Background(), record.CallHash, time.Hour)
	require.NoError(t, err)
	require.True(t, closed)
	closed, err = cache.MarkLiveCallClosed(context.Background(), record.CallHash, time.Hour)
	require.NoError(t, err)
	require.False(t, closed)
}
