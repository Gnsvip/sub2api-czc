package service

import (
	"context"
	"maps"
	"net/http"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const OpenAIAstraProbeIntervalExtraKey = "openai_astra_probe_interval_minutes"

// Zero inherits the deployment's cadence. Overrides retain the existing probe
// budget, confirmation spacing, quota holds and rejection backoff.
func (a *Account) OpenAIAstraProbeIntervalMinutes() int {
	if a == nil {
		return 0
	}
	var n int
	switch v := a.Extra[OpenAIAstraProbeIntervalExtraKey].(type) {
	case int:
		n = v
	case float64:
		if v != float64(int(v)) {
			return 0
		}
		n = int(v)
	}
	if n < 5 || n > 1440 {
		return 0
	}
	return n
}

func (s *adminServiceImpl) SetOpenAIAstraProbeInterval(ctx context.Context, id int64, minutes int) (*Account, error) {
	if minutes != 0 && (minutes < 5 || minutes > 1440) {
		return nil, infraerrors.BadRequest("ASTRA_PROBE_INTERVAL_INVALID", "Use 0 for the default, or 5–1440 minutes")
	}
	a, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !isOpenAITiboAccount(a) {
		return nil, infraerrors.BadRequest("ASTRA_PROBE_UNSUPPORTED", "Astra probing is only available for OpenAI accounts")
	}
	if err = s.accountRepo.UpdateExtra(ctx, id, map[string]any{OpenAIAstraProbeIntervalExtraKey: minutes}); err != nil {
		return nil, err
	}
	return s.accountRepo.GetByID(ctx, id)
}

func (s *OpenAIGatewayService) openAITiboConfigForAccount(a *Account) openAITiboSettings {
	cfg := s.openAITiboRouteConfig()
	if minutes := a.OpenAIAstraProbeIntervalMinutes(); minutes > 0 {
		cfg.interval = time.Duration(minutes) * time.Minute
		cfg.spread = 0
	}
	return cfg
}

// Caller holds state.mu. Re-schedule ordinary cadence only; a setting edit must
// not erase a pending confirmation, error backoff, quota hold or hourly budget.
func (s *OpenAIGatewayService) configureOpenAITiboIntervalLocked(state *openAITiboAccountState, a *Account, cfg openAITiboSettings) {
	r := &state.http
	if r.interval != cfg.interval {
		if (r.interval != 0 || a.OpenAIAstraProbeIntervalMinutes() > 0) && !r.inflight && !r.lastSampleAt.IsZero() && len(r.pending) == 0 && !r.dwellDeferred && r.unknownStreak == 0 && r.clientErrorStreak == 0 {
			r.nextProbeAt = r.lastSampleAt.Add(cfg.interval)
		}
		r.interval = cfg.interval
	}
}

func (s *AccountTestService) RefreshOpenAIAstraProbeInterval(a *Account) {
	if s == nil || s.openaiGatewayService == nil || a == nil {
		return
	}
	g := s.openaiGatewayService
	if state := g.loadOpenAITiboAccountState(a.ID); state != nil {
		state.mu.Lock()
		g.configureOpenAITiboIntervalLocked(state, a, g.openAITiboConfigForAccount(a))
		state.mu.Unlock()
	}
}

func manualOpenAITiboAccount(a *Account) *Account {
	if a == nil {
		return nil
	}
	view := *a
	view.Extra = maps.Clone(a.Extra)
	if view.Extra == nil {
		view.Extra = map[string]any{}
	}
	view.Extra[OpenAIAstraProbeEnabledExtraKey] = true
	return &view
}

func (s *AccountTestService) RunOpenAIAstraProbe(ctx context.Context, id int64) error {
	if s == nil || s.openaiGatewayService == nil {
		return infraerrors.ServiceUnavailable("ASTRA_PROBE_UNAVAILABLE", "Astra probe service is unavailable")
	}
	return s.openaiGatewayService.RunOpenAIAstraProbe(ctx, id)
}

// A manual probe shares the normal single-flight, concurrency and budget. It
// bypasses the automatic toggle/cadence only, and never saves that toggle.
func (s *OpenAIGatewayService) RunOpenAIAstraProbe(ctx context.Context, id int64) error {
	if s == nil || s.tiboRouteDisabled || s.httpUpstream == nil {
		return infraerrors.ServiceUnavailable("ASTRA_PROBE_UNAVAILABLE", "Astra probe service is unavailable")
	}
	a, err := s.latestOpenAITiboAccount(ctx, id)
	if err != nil {
		return err
	}
	if !isOpenAITiboAccount(a) {
		return infraerrors.BadRequest("ASTRA_PROBE_UNSUPPORTED", "Astra probing is only available for OpenAI accounts")
	}
	now := time.Now()
	view := manualOpenAITiboAccount(a)
	hold := s.openAITiboHoldFor(s.withOpenAIQuotaAutoPauseContext(ctx), view, now)
	if hold.parked || !hold.until.IsZero() {
		return infraerrors.New(http.StatusConflict, "ASTRA_PROBE_HELD", "Account is unavailable or cooling down")
	}
	cfg := s.openAITiboConfigForAccount(a)
	state := s.openAITiboAccountState(id)
	state.mu.Lock()
	s.seedOpenAITiboStateLocked(state, a.Extra, now, cfg)
	s.configureOpenAITiboIntervalLocked(state, a, cfg)
	before := state.http.lastSampleAt
	done := s.startOpenAITiboProbeCtxLocked(context.Background(), state, openAITiboRouteHTTP, nil, true, cfg, now, true)
	state.mu.Unlock()
	if done == nil {
		return infraerrors.New(http.StatusTooManyRequests, "ASTRA_PROBE_BUDGET", "Hourly probe budget reached; try again later")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}
	state.mu.Lock()
	completed := state.http.lastSampleAt.After(before)
	state.mu.Unlock()
	if !completed {
		return infraerrors.New(http.StatusConflict, "ASTRA_PROBE_NOT_STARTED", "Probe could not start; retry after the account becomes available")
	}
	return nil
}
