package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"sort"
	"sync"
	"time"
)

const bpsSessionLimit = 4096
const bpsSessionIdleTTL = 30 * time.Minute
const bpsProxyCooldown = time.Minute

var errBPSProxyUnavailable = errors.New("BPS session proxy pool has no available exit")

type bpsProxySource interface {
	ListActive(context.Context) ([]Proxy, error)
}

type bpsSessionBinding struct {
	proxy    Proxy
	active   int
	lastUsed time.Time
}

// Bindings are process-local and bounded. A live response pins its exit until
// Close; failures only affect future requests, never replay a submitted prompt.
type bpsSessionPool struct {
	mu       sync.Mutex
	bindings map[[32]byte]*bpsSessionBinding
	cooldown map[int64]time.Time
}

func (p *bpsSessionPool) acquire(scope string, proxies []Proxy, now time.Time) (*Proxy, func(bool), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bindings == nil {
		p.bindings = make(map[[32]byte]*bpsSessionBinding)
		p.cooldown = make(map[int64]time.Time)
	}
	for id, until := range p.cooldown {
		if !now.Before(until) {
			delete(p.cooldown, id)
		}
	}
	for key, binding := range p.bindings {
		if binding.active == 0 && now.Sub(binding.lastUsed) >= bpsSessionIdleTTL {
			delete(p.bindings, key)
		}
	}
	eligible := make(map[int64]Proxy)
	for _, proxy := range proxies {
		if proxy.ID > 0 && proxy.IsActive() && !proxy.IsExpired(now) && proxy.Host != "" && proxy.Port > 0 {
			switch proxy.Protocol {
			case "http", "https", "socks5", "socks5h":
				eligible[proxy.ID] = proxy
			}
		}
	}
	key := sha256.Sum256([]byte(scope))
	binding := p.bindings[key]
	if binding != nil {
		candidate, exists := eligible[binding.proxy.ID]
		valid := exists && candidate.URL() == binding.proxy.URL() && !now.Before(p.cooldown[candidate.ID])
		if !valid {
			// An in-flight session must not straddle exits, even when its proxy
			// is removed, expires or enters cooldown during another request.
			if binding.active > 0 {
				return nil, nil, errBPSProxyUnavailable
			}
			delete(p.bindings, key)
			binding = nil
		}
	}
	if binding == nil {
		if len(p.bindings) >= bpsSessionLimit {
			var oldest [32]byte
			var oldestTime time.Time
			for k, b := range p.bindings {
				if b.active == 0 && (oldestTime.IsZero() || b.lastUsed.Before(oldestTime)) {
					oldest, oldestTime = k, b.lastUsed
				}
			}
			if oldestTime.IsZero() {
				return nil, nil, errBPSProxyUnavailable
			}
			delete(p.bindings, oldest)
		}
		loads, sessions := map[int64]int{}, map[int64]int{}
		for _, b := range p.bindings {
			loads[b.proxy.ID] += b.active
			sessions[b.proxy.ID]++
		}
		ids := make([]int64, 0, len(eligible))
		for id := range eligible {
			if !now.Before(p.cooldown[id]) {
				ids = append(ids, id)
			}
		}
		sort.Slice(ids, func(i, j int) bool {
			a, b := ids[i], ids[j]
			if loads[a] != loads[b] {
				return loads[a] < loads[b]
			}
			if sessions[a] != sessions[b] {
				return sessions[a] < sessions[b]
			}
			return a < b
		})
		if len(ids) == 0 {
			return nil, nil, errBPSProxyUnavailable
		}
		binding = &bpsSessionBinding{proxy: eligible[ids[0]]}
		p.bindings[key] = binding
	}
	binding.active++
	binding.lastUsed = now
	proxy := binding.proxy
	var once sync.Once
	release := func(failed bool) {
		once.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			binding.active--
			binding.lastUsed = time.Now()
			if failed {
				p.cooldown[proxy.ID] = time.Now().Add(bpsProxyCooldown)
			}
		})
	}
	return &proxy, release, nil
}
