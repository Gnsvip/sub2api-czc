package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIntelligentTestAnimalsAreUniqueAndNumerous(t *testing.T) {
	t.Parallel()
	require.GreaterOrEqual(t, len(intelligentTestAnimals), 100)
	seen := map[string]bool{}
	for _, animal := range intelligentTestAnimals {
		require.NotEmpty(t, animal)
		require.False(t, seen[animal], "duplicate animal %q", animal)
		seen[animal] = true
	}
}

func TestPickIntelligentTestAnimalAvoidsLastChoice(t *testing.T) {
	t.Parallel()
	first := pickIntelligentTestAnimal("")
	for i := 0; i < 256; i++ {
		next := pickIntelligentTestAnimal(first)
		require.NotEqual(t, first, next)
		require.Equal(t, next, intelligentJudgeAnimal(intelligentAnimalHTMLPrompt(next)))
		for _, excluded := range []string{"抹香鲸", "海豚", "海马", "金鱼", "眼镜蛇", "蚓螈", "水母", "海星", "蜗牛", "蚯蚓", "佛法僧"} {
			require.NotEqual(t, excluded, next)
		}
		first = next
	}
}

func TestIntelligentAnimalHTMLPromptInsertsName(t *testing.T) {
	t.Parallel()
	prompt := intelligentAnimalHTMLPrompt("火烈鸟")
	require.Contains(t, prompt, "绘制一个火烈鸟骑自行车")
	require.Contains(t, prompt, "不要依赖我本地的AGENTS.md")
}

func TestAdminPelicanRunPicksRandomAnimal(t *testing.T) {
	t.Parallel()
	svc := &IntelligentTestService{}
	req := svc.withAdminPelicanAnimal(IntelligentTestEnqueue{TestTypes: []string{"pelican", "candy"}})
	require.Empty(t, req.Prompts)
	require.NotNil(t, req.PromptFor)
	prompt := req.PromptFor(1, "pelican")
	require.Contains(t, prompt, "骑自行车的2D动画")
	require.NotEmpty(t, intelligentJudgeAnimal(prompt), "prompt %q should name a known subject", prompt)
	require.Empty(t, req.PromptFor(1, "candy"))
	kept := svc.withAdminPelicanAnimal(IntelligentTestEnqueue{TestTypes: []string{"pelican"}, Prompts: map[string]string{"pelican": "keep"}})
	require.Equal(t, "keep", kept.Prompts["pelican"])
	require.Nil(t, kept.PromptFor)
	scheduled := svc.withAdminPelicanAnimal(IntelligentTestEnqueue{TestTypes: []string{"pelican"}, Source: IntelligentTestSourcePelicanSchedule})
	require.Empty(t, scheduled.Prompts)
	require.Nil(t, scheduled.PromptFor)
}

func TestAdminPelicanBatchPicksDifferentAnimals(t *testing.T) {
	t.Parallel()
	svc := &IntelligentTestService{}
	req := svc.withAdminPelicanAnimal(IntelligentTestEnqueue{TestTypes: []string{"pelican"}})
	first := IntelligentTestPromptOverride(req, 11, "pelican")
	second := IntelligentTestPromptOverride(req, 12, "pelican")
	require.NotEqual(t, first, second)
	require.Equal(t, "keep-all", IntelligentTestPromptOverride(IntelligentTestEnqueue{
		Prompts:   map[string]string{"pelican": "keep-all"},
		PromptFor: req.PromptFor,
	}, 13, "pelican"))
}

func TestPelicanSlotKeyTruncatesToThirtyMinutes(t *testing.T) {
	t.Parallel()
	now, err := time.Parse(time.RFC3339, "2026-09-20T03:37:44Z")
	require.NoError(t, err)
	require.Equal(t, "pelican-slot-202609200330", pelicanSlotKey(now))
	same, err := time.Parse(time.RFC3339, "2026-09-20T03:39:59Z")
	require.NoError(t, err)
	require.Equal(t, "pelican-slot-202609200330", pelicanSlotKey(same))
	next, err := time.Parse(time.RFC3339, "2026-09-20T04:00:00Z")
	require.NoError(t, err)
	require.Equal(t, "pelican-slot-202609200400", pelicanSlotKey(next))
}

func TestPelicanInBeijingWindowIsEightToMidnight(t *testing.T) {
	t.Parallel()
	parse := func(s string) time.Time {
		t.Helper()
		ts, err := time.Parse(time.RFC3339, s)
		require.NoError(t, err)
		return ts
	}
	require.True(t, pelicanInBeijingWindow(parse("2026-09-20T00:00:00Z")))  // 08:00
	require.True(t, pelicanInBeijingWindow(parse("2026-09-20T15:59:59Z")))  // 23:59
	require.False(t, pelicanInBeijingWindow(parse("2026-09-20T16:00:00Z"))) // 00:00
	require.False(t, pelicanInBeijingWindow(parse("2026-09-19T23:59:59Z"))) // 07:59
}

func TestPelicanNextBoundaryAlignsToThirtyMinutes(t *testing.T) {
	t.Parallel()
	now, err := time.Parse(time.RFC3339, "2026-09-19T23:50:00Z") // 07:50 Beijing
	require.NoError(t, err)
	next := pelicanNextBoundary(now)
	require.Equal(t, "2026-09-20T08:00:00+08:00", next.Format(time.RFC3339))
	require.True(t, pelicanInBeijingWindow(next))
	inside, err := time.Parse(time.RFC3339, "2026-09-20T00:07:00Z") // 08:07 Beijing
	require.NoError(t, err)
	require.Equal(t, "2026-09-20T08:30:00+08:00", pelicanNextBoundary(inside).Format(time.RFC3339))
}

func TestVisualJudgeUsesRequestedAnimal(t *testing.T) {
	for _, animal := range []string{"豆娘", "海豚", "鹈鹕", "大王酸浆鱿", "小熊猫", "哆啦A梦", "江户川柯南", "路飞"} {
		prompt := intelligentAnimalHTMLPrompt(animal)
		require.Contains(t, prompt, "创建一个HTML，内容是SVG绘制一个"+animal+"骑自行车")
		require.Contains(t, prompt, "不使用 JavaScript")
		require.NotContains(t, prompt, "带脚")
		require.NotContains(t, prompt, "脚踩")
		require.Equal(t, animal, intelligentJudgeAnimal(prompt))
	}
	require.Equal(t, "鹈鹕", intelligentJudgeAnimal("Generate a pelican riding a bicycle."))
	require.Empty(t, intelligentJudgeAnimal("unrelated custom task"))
}
