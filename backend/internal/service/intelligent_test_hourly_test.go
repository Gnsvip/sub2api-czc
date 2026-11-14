package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type hourlyPelicanRepo struct {
	IntelligentTestRepository
	setting          *IntelligentTestSetting
	candidateGroupID int64
	adminID          int64
	accountIDs       []int64
	candidates       []PelicanCandidate
	enqueued         []IntelligentTestEnqueue
	actors           []int64
	attempts         []PelicanSlotAttempt
}

func (r *hourlyPelicanRepo) Settings(context.Context) ([]IntelligentTestSetting, error) {
	if r.setting != nil {
		return []IntelligentTestSetting{*r.setting}, nil
	}
	return []IntelligentTestSetting{{TestType: "pelican", Enabled: true, Config: IntelligentTestConfig{ScheduleEnabled: true, GroupID: 42}}}, nil
}
func (r *hourlyPelicanRepo) FirstAdminUserID(context.Context) (int64, error) { return r.adminID, nil }
func (r *hourlyPelicanRepo) ListPelicanCandidates(_ context.Context, groupID int64) ([]PelicanCandidate, error) {
	r.candidateGroupID = groupID
	if len(r.candidates) > 0 {
		return r.candidates, nil
	}
	out := make([]PelicanCandidate, 0, len(r.accountIDs))
	for _, id := range r.accountIDs {
		out = append(out, PelicanCandidate{ID: id, HasTicket: true})
	}
	return out, nil
}
func (r *hourlyPelicanRepo) Enqueue(_ context.Context, actor int64, req IntelligentTestEnqueue) (*IntelligentTestEnqueued, error) {
	r.actors = append(r.actors, actor)
	r.enqueued = append(r.enqueued, req)
	return &IntelligentTestEnqueued{}, nil
}
func (r *hourlyPelicanRepo) ClaimID(context.Context, int64) (*IntelligentTestRecord, error) {
	return nil, nil
}
func (r *hourlyPelicanRepo) ListPelicanSlotAttempts(context.Context, string) ([]PelicanSlotAttempt, error) {
	return r.attempts, nil
}

func pelicanNoonBeijing(t *testing.T) time.Time {
	t.Helper()
	now, err := time.Parse(time.RFC3339, "2026-09-20T04:00:00Z")
	require.NoError(t, err)
	return now
}

func TestRunScheduledPelicanEnqueuesRandomAnimalPrompt(t *testing.T) {
	repo := &hourlyPelicanRepo{adminID: 3, accountIDs: []int64{11, 12}}
	svc := &IntelligentTestService{repo: repo}
	svc.runScheduledPelicanAt(context.Background(), pelicanNoonBeijing(t))
	require.Equal(t, []int64{3}, repo.actors)
	require.Len(t, repo.enqueued, 1)
	require.Len(t, repo.enqueued[0].AccountIDs, 1)
	require.Contains(t, []int64{11, 12}, repo.enqueued[0].AccountIDs[0])
	require.Equal(t, []string{"pelican"}, repo.enqueued[0].TestTypes)
	require.Equal(t, "gpt-6-astra", repo.enqueued[0].Models["pelican"])
	prompt := repo.enqueued[0].Prompts["pelican"]
	require.Contains(t, prompt, "骑自行车的2D动画")
	require.Contains(t, prompt, "不要使用任何skill")
	require.NotEmpty(t, intelligentJudgeAnimal(prompt), "prompt %q should name a known subject", prompt)
	require.Regexp(t, `^pelican-slot-\d{12}$`, repo.enqueued[0].IdempotencyKey)
	require.Equal(t, IntelligentTestSourcePelicanSchedule, repo.enqueued[0].Source)
	require.EqualValues(t, 42, repo.candidateGroupID)
	require.EqualValues(t, 42, repo.enqueued[0].ScheduledGroupID)
}

func TestScheduledPelicanRequiresExplicitSwitchAndGroup(t *testing.T) {
	for _, setting := range []IntelligentTestSetting{
		{TestType: "pelican", Enabled: true}, // An old installation must opt in.
		{TestType: "pelican", Enabled: true, Config: IntelligentTestConfig{GroupID: 42}},
		{TestType: "pelican", Enabled: true, Config: IntelligentTestConfig{ScheduleEnabled: true}},
		{TestType: "pelican", Config: IntelligentTestConfig{ScheduleEnabled: true, GroupID: 42}},
	} {
		repo := &hourlyPelicanRepo{setting: &setting, adminID: 3, accountIDs: []int64{11}}
		(&IntelligentTestService{repo: repo}).runScheduledPelicanAt(context.Background(), pelicanNoonBeijing(t))
		require.Empty(t, repo.enqueued)
		require.Zero(t, repo.candidateGroupID)
	}
}

type userPelicanListRepo struct {
	IntelligentTestRepository
	filter  IntelligentTestFilter
	setting *IntelligentTestSetting
	called  bool
}

func (r *userPelicanListRepo) Settings(context.Context) ([]IntelligentTestSetting, error) {
	if r.setting != nil {
		return []IntelligentTestSetting{*r.setting}, nil
	}
	return []IntelligentTestSetting{{TestType: "pelican", Enabled: true, UserVisible: true, Config: IntelligentTestConfig{GroupID: 77}}}, nil
}

func (r *userPelicanListRepo) UserPelicanTests(_ context.Context, f IntelligentTestFilter) (*UserPelicanTests, error) {
	r.filter = f
	r.called = true
	return &UserPelicanTests{Items: []UserPelicanTest{}, Page: f.Page, PageSize: f.PageSize}, nil
}

func TestUserPelicanTestsListsLastTwentyFourHours(t *testing.T) {
	repo := &userPelicanListRepo{}
	svc := &IntelligentTestService{repo: repo}
	out, err := svc.UserPelicanTests(context.Background(), 9, IntelligentTestFilter{Page: 3, PageSize: 12, GroupID: 999})
	require.NoError(t, err)
	require.Equal(t, 1, repo.filter.Page)
	require.Equal(t, pelicanUserPageSize, repo.filter.PageSize)
	require.Equal(t, pelicanUserPageSize, out.PageSize)
	require.EqualValues(t, 77, repo.filter.GroupID)
}

func TestPelicanVisibilityRevokesResultAccess(t *testing.T) {
	for _, setting := range []IntelligentTestSetting{
		{TestType: "pelican", Enabled: true, UserVisible: false},
		{TestType: "pelican", Enabled: false, UserVisible: true},
	} {
		repo := &userPelicanListRepo{setting: &setting}
		svc := &IntelligentTestService{repo: repo}
		visible, err := svc.PelicanVisible(context.Background(), 9)
		require.NoError(t, err)
		require.False(t, visible)
		_, err = svc.UserPelicanTests(context.Background(), 9, IntelligentTestFilter{})
		require.ErrorIs(t, err, ErrIntelligentTestNotFound)
		require.False(t, repo.called)
	}
	visible, err := (&IntelligentTestService{repo: &userPelicanListRepo{}}).PelicanVisible(context.Background(), 9)
	require.NoError(t, err)
	require.True(t, visible, "manual results can remain visible while the timer is off")
}

type purgePelicanRepo struct {
	IntelligentTestRepository
	deleted int64
}

func (r *purgePelicanRepo) DeleteStalePelicanTests(context.Context) (int64, error) {
	r.deleted = 4
	return r.deleted, nil
}

func TestPurgeStalePelicanTestsDeletesOlderThanTwentyFourHours(t *testing.T) {
	repo := &purgePelicanRepo{}
	svc := &IntelligentTestService{repo: repo}
	svc.purgeStalePelicanTests(context.Background())
	require.Equal(t, int64(4), repo.deleted)
}

func TestRunScheduledPelicanSkipsWithoutAdminOrAccounts(t *testing.T) {
	now := pelicanNoonBeijing(t)
	empty := &hourlyPelicanRepo{adminID: 0, accountIDs: []int64{1}}
	(&IntelligentTestService{repo: empty}).runScheduledPelicanAt(context.Background(), now)
	require.Empty(t, empty.enqueued)

	noAccounts := &hourlyPelicanRepo{adminID: 8}
	(&IntelligentTestService{repo: noAccounts}).runScheduledPelicanAt(context.Background(), now)
	require.Empty(t, noAccounts.enqueued)
}

func TestRunScheduledPelicanUsesOnlyListedTicketedAccounts(t *testing.T) {
	repo := &hourlyPelicanRepo{adminID: 3, accountIDs: []int64{42}}
	svc := &IntelligentTestService{repo: repo}
	svc.runScheduledPelicanAt(context.Background(), pelicanNoonBeijing(t))
	require.Equal(t, []int64{42}, repo.enqueued[0].AccountIDs)
}

func TestPickPelicanAccountIDPrefersHealthyHTTP(t *testing.T) {
	t.Parallel()
	cands := []PelicanCandidate{
		{ID: 11, HasTicket: true, HasWS: true},
		{ID: 12, HasHealthyHTTP: true},
		{ID: 13, HasWS: true},
	}
	require.Equal(t, int64(12), pickPelicanAccountID(cands, nil))
	// 排除 12 后 11 与 13 同属 unusedWS 层级，函数按设计随机挑选其一。
	got := pickPelicanAccountID(cands, map[int64]struct{}{12: {}})
	require.Contains(t, []int64{11, 13}, got)
}

func TestPickPelicanAccountIDPrefersLiveWS(t *testing.T) {
	t.Parallel()
	cands := []PelicanCandidate{
		{ID: 11, HasTicket: true},
		{ID: 12, HasWS: true},
		{ID: 13},
	}
	require.Equal(t, int64(12), pickPelicanAccountID(cands, nil))
	require.Equal(t, int64(11), pickPelicanAccountID(cands, map[int64]struct{}{12: {}}))
	onlySchedulable := []PelicanCandidate{{ID: 11}, {ID: 13}}
	got := pickPelicanAccountID(onlySchedulable, nil)
	require.Contains(t, []int64{11, 13}, got)
}

func TestPickPelicanAccountIDPrefersUnusedTicket(t *testing.T) {
	t.Parallel()
	cands := []PelicanCandidate{{ID: 11, HasTicket: false}, {ID: 12, HasTicket: true}, {ID: 13, HasTicket: false}}
	require.Equal(t, int64(12), pickPelicanAccountID(cands, nil))
	got := pickPelicanAccountID(cands, map[int64]struct{}{12: {}})
	require.Contains(t, []int64{11, 13}, got)
}

func TestRunScheduledPelicanPrefersHTTPHealthyAccount(t *testing.T) {
	repo := &hourlyPelicanRepo{
		adminID: 3,
		candidates: []PelicanCandidate{
			{ID: 11, HasTicket: true, HasWS: true},
			{ID: 12, HasHealthyHTTP: true},
			{ID: 13, HasWS: true},
		},
	}
	svc := &IntelligentTestService{repo: repo}
	svc.runScheduledPelicanAt(context.Background(), pelicanNoonBeijing(t))
	require.Equal(t, []int64{12}, repo.enqueued[0].AccountIDs)
}

func TestRunScheduledPelicanPrefersTicketedAccount(t *testing.T) {
	repo := &hourlyPelicanRepo{
		adminID: 3,
		candidates: []PelicanCandidate{
			{ID: 11, HasTicket: false},
			{ID: 12, HasTicket: true},
		},
	}
	svc := &IntelligentTestService{repo: repo}
	svc.runScheduledPelicanAt(context.Background(), pelicanNoonBeijing(t))
	require.Equal(t, []int64{12}, repo.enqueued[0].AccountIDs)
}

func TestRunScheduledPelicanPrefersLiveWSAccount(t *testing.T) {
	repo := &hourlyPelicanRepo{
		adminID: 3,
		candidates: []PelicanCandidate{
			{ID: 11, HasTicket: true},
			{ID: 12, HasWS: true},
			{ID: 13},
		},
	}
	svc := &IntelligentTestService{repo: repo}
	svc.runScheduledPelicanAt(context.Background(), pelicanNoonBeijing(t))
	require.Equal(t, []int64{12}, repo.enqueued[0].AccountIDs)
}

type pelicanLiveWSRunner struct {
	counts map[int64][openAICookieWSSlotCount]int
}

func (pelicanLiveWSRunner) RunIntelligentTest(context.Context, *IntelligentTestRecord) error {
	return nil
}

func (r pelicanLiveWSRunner) OpenAICookieWSVerifiedCounts(id int64) [openAICookieWSSlotCount]int {
	return r.counts[id]
}

type pelicanHTTPHealthRunner struct {
	verdicts map[int64]string
}

func (pelicanHTTPHealthRunner) RunIntelligentTest(context.Context, *IntelligentTestRecord) error {
	return nil
}

func (r pelicanHTTPHealthRunner) OpenAITiboHTTPVerdict(id int64) string {
	return r.verdicts[id]
}

func TestRunScheduledPelicanMarksHealthyHTTPFromRunner(t *testing.T) {
	repo := &hourlyPelicanRepo{
		adminID: 3,
		candidates: []PelicanCandidate{
			{ID: 11, HasTicket: true, HasWS: true},
			{ID: 12},
			{ID: 13, HasHealthyHTTP: true},
		},
	}
	svc := &IntelligentTestService{
		repo: repo,
		runner: pelicanHTTPHealthRunner{verdicts: map[int64]string{
			12: string(openAITiboHealthy),
			13: string(openAITiboDegraded),
		}},
	}
	svc.runScheduledPelicanAt(context.Background(), pelicanNoonBeijing(t))
	require.Equal(t, []int64{12}, repo.enqueued[0].AccountIDs)
}

func TestRunScheduledPelicanMarksLiveWSFromRunner(t *testing.T) {
	repo := &hourlyPelicanRepo{
		adminID: 3,
		candidates: []PelicanCandidate{
			{ID: 11, HasTicket: true},
			{ID: 12},
			{ID: 13},
		},
	}
	svc := &IntelligentTestService{
		repo:   repo,
		runner: pelicanLiveWSRunner{counts: map[int64][openAICookieWSSlotCount]int{12: {0, 1, 0}}},
	}
	svc.runScheduledPelicanAt(context.Background(), pelicanNoonBeijing(t))
	require.Equal(t, []int64{12}, repo.enqueued[0].AccountIDs)
}

func TestRunScheduledPelicanSkipsOutsideBeijingWindow(t *testing.T) {
	repo := &hourlyPelicanRepo{adminID: 3, accountIDs: []int64{42}}
	svc := &IntelligentTestService{repo: repo}
	night, err := time.Parse(time.RFC3339, "2026-09-20T16:30:00Z")
	require.NoError(t, err)
	svc.runScheduledPelicanAt(context.Background(), night)
	require.Empty(t, repo.enqueued)
}

func TestRunScheduledPelicanMidnightFinalSlot(t *testing.T) {
	// The same slot key survives timer jitter, retries and process restarts.
	midnight := time.Date(2026, 11, 15, 0, 0, 0, 20*int(time.Millisecond), pelicanBeijingLocation())
	for _, tc := range []struct {
		name     string
		attempts []PelicanSlotAttempt
		wantKey  string
	}{
		{name: "first run", wantKey: "pelican-slot-202611141600"},
		{name: "retry", attempts: []PelicanSlotAttempt{{AccountID: 11, Status: "failed"}}, wantKey: "pelican-slot-202611141600-r1"},
		{name: "already completed", attempts: []PelicanSlotAttempt{{AccountID: 11, Status: "completed", HasSVG: true}}},
		{name: "still running", attempts: []PelicanSlotAttempt{{AccountID: 11, Status: "running"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &hourlyPelicanRepo{adminID: 3, accountIDs: []int64{11, 12}, attempts: tc.attempts}
			svc := &IntelligentTestService{repo: repo}
			at := midnight
			if len(tc.attempts) > 0 {
				at = at.Add(pelicanRetryInterval)
			}
			svc.runScheduledPelicanAt(context.Background(), at)
			if tc.wantKey == "" {
				require.Empty(t, repo.enqueued)
				return
			}
			require.Len(t, repo.enqueued, 1)
			require.Equal(t, tc.wantKey, repo.enqueued[0].IdempotencyKey)
			svc.runScheduledPelicanAt(context.Background(), midnight.Add(pelicanScheduleInterval))
			require.Len(t, repo.enqueued, 1, "00:30 must not enqueue a new slot")
		})
	}
}

func TestPelicanAttemptKeyAddsRetrySuffix(t *testing.T) {
	t.Parallel()
	now := pelicanNoonBeijing(t)
	require.Equal(t, "pelican-slot-202609200400", pelicanAttemptKey(now, 0))
	require.Equal(t, "pelican-slot-202609200400-r1", pelicanAttemptKey(now, 1))
	require.Equal(t, "pelican-slot-202609200400-r2", pelicanAttemptKey(now, 2))
}

func TestRunScheduledPelicanRetriesFailedSlotOnAnotherAccount(t *testing.T) {
	repo := &hourlyPelicanRepo{
		adminID:    3,
		accountIDs: []int64{11, 12},
		attempts:   []PelicanSlotAttempt{{AccountID: 11, Status: "rate_limited"}},
	}
	svc := &IntelligentTestService{repo: repo}
	svc.runScheduledPelicanAt(context.Background(), pelicanNoonBeijing(t))
	require.Len(t, repo.enqueued, 1)
	require.Equal(t, []int64{12}, repo.enqueued[0].AccountIDs)
	require.Equal(t, "pelican-slot-202609200400-r1", repo.enqueued[0].IdempotencyKey)
}

func TestRunScheduledPelicanRetriesCompletedWithoutSVG(t *testing.T) {
	repo := &hourlyPelicanRepo{
		adminID:    3,
		accountIDs: []int64{11, 12},
		attempts:   []PelicanSlotAttempt{{AccountID: 11, Status: "completed", HasSVG: false}},
	}
	svc := &IntelligentTestService{repo: repo}
	svc.runScheduledPelicanAt(context.Background(), pelicanNoonBeijing(t))
	require.Len(t, repo.enqueued, 1)
	require.Equal(t, "pelican-slot-202609200400-r1", repo.enqueued[0].IdempotencyKey)
	require.Equal(t, []int64{12}, repo.enqueued[0].AccountIDs)
}

func TestRunScheduledPelicanSkipsSuccessfulSVGSlot(t *testing.T) {
	repo := &hourlyPelicanRepo{
		adminID:    3,
		accountIDs: []int64{11, 12},
		attempts:   []PelicanSlotAttempt{{AccountID: 11, Status: "completed", HasSVG: true}},
	}
	svc := &IntelligentTestService{repo: repo}
	svc.runScheduledPelicanAt(context.Background(), pelicanNoonBeijing(t))
	require.Empty(t, repo.enqueued)
}

func TestRunScheduledPelicanSkipsInFlightSlot(t *testing.T) {
	repo := &hourlyPelicanRepo{
		adminID:    3,
		accountIDs: []int64{11, 12},
		attempts:   []PelicanSlotAttempt{{AccountID: 11, Status: "running"}},
	}
	svc := &IntelligentTestService{repo: repo}
	svc.runScheduledPelicanAt(context.Background(), pelicanNoonBeijing(t))
	require.Empty(t, repo.enqueued)
}

func TestRunScheduledPelicanStopsAfterMaxAttempts(t *testing.T) {
	repo := &hourlyPelicanRepo{
		adminID:    3,
		accountIDs: []int64{11, 12, 13},
		attempts: []PelicanSlotAttempt{
			{AccountID: 11, Status: "failed"},
			{AccountID: 12, Status: "rate_limited"},
			{AccountID: 13, Status: "completed", HasSVG: false},
		},
	}
	svc := &IntelligentTestService{repo: repo}
	svc.runScheduledPelicanAt(context.Background(), pelicanNoonBeijing(t))
	require.Empty(t, repo.enqueued)
}

func TestPelicanLoopWaitRetriesInsideWindowAndSleepsOvernight(t *testing.T) {
	t.Parallel()
	noon := pelicanNoonBeijing(t)
	require.Equal(t, pelicanRetryInterval, pelicanLoopWait(noon))
	nearBoundary, err := time.Parse(time.RFC3339, "2026-09-20T04:28:00Z")
	require.NoError(t, err)
	require.InDelta(t, (2 * time.Minute).Seconds(), pelicanLoopWait(nearBoundary).Seconds(), 1)
	halfHourBoundary, err := time.Parse(time.RFC3339, "2026-09-20T04:29:00Z") // 12:29 Beijing
	require.NoError(t, err)
	require.InDelta(t, time.Minute.Seconds(), pelicanLoopWait(halfHourBoundary).Seconds(), 1)
	nearMidnight := time.Date(2026, 9, 20, 23, 59, 58, 0, pelicanBeijingLocation())
	require.Equal(t, 2*time.Second, pelicanLoopWait(nearMidnight))
	require.Equal(t, pelicanRetryInterval, pelicanLoopWait(nearMidnight.Add(2*time.Second)))
	night, err := time.Parse(time.RFC3339, "2026-09-20T16:30:00Z")
	require.NoError(t, err)
	nextEight := time.Date(2026, 9, 21, 8, 0, 0, 0, pelicanBeijingLocation())
	require.Equal(t, nextEight.Sub(night), pelicanLoopWait(night))
}
