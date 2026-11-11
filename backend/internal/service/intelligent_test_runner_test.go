package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAutoPelicanWorkerVisualScoring(t *testing.T) {
	python := os.Getenv("SUB2API_PELICAN_TEST_PYTHON")
	if python == "" {
		t.Skip("auto-pelican runtime is installed in GitHub Actions")
	}
	var requests atomic.Int64
	var fail atomic.Bool
	var failNext atomic.Bool
	var animalRequests atomic.Int64
	var characterRequests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if fail.Load() {
			http.Error(w, "mock judge failure", http.StatusServiceUnavailable)
			return
		}
		if failNext.Swap(false) {
			http.Error(w, "one transient mock judge failure", http.StatusBadGateway)
			return
		}
		var request struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Messages) != 2 {
			t.Error("missing blind visual judge request")
			http.Error(w, "bad request", 400)
			return
		}
		if strings.Contains(string(request.Messages[0].Content), "豆娘") {
			animalRequests.Add(1)
			require.Contains(t, string(request.Messages[0].Content), "Do not require a beak")
		}
		if strings.Contains(string(request.Messages[0].Content), "哆啦A梦") {
			characterRequests.Add(1)
			require.Contains(t, string(request.Messages[0].Content), "anime character")
			require.Contains(t, string(request.Messages[0].Content), "do not require species-specific animal features")
		}
		var parts []map[string]any
		if json.Unmarshal(request.Messages[1].Content, &parts) != nil || len(parts) != 3 {
			t.Error("judge must receive first frame and six-frame montage")
		}
		checks := map[string]bool{}
		for _, key := range []string{"pelican_identifiable", "pelican_long_bill", "pelican_throat_pouch", "pelican_eye", "pelican_wing", "pelican_legs_or_feet", "bicycle_identifiable", "bicycle_two_wheels", "bicycle_frame_connects_wheels", "bicycle_handlebars", "bicycle_saddle", "bicycle_pedals_or_crank", "bicycle_chain_or_chainring", "pelican_mounted", "feet_on_pedals", "wing_on_handlebars", "plausible_scale", "subjects_fully_in_frame", "no_detached_parts", "no_major_artifacts", "motion_visible", "wheels_rotate", "legs_pedal", "parts_stay_attached_in_motion"} {
			checks[key] = true
		}
		scores := map[string]any{}
		for _, key := range []string{"prompt_fidelity", "pelican_quality", "bicycle_quality", "interaction_spatial_correctness", "animation_quality", "visual_coherence", "aesthetic_quality"} {
			scores[key] = map[string]any{"score": 4, "reason": "mock visual evidence"}
		}
		judgement, _ := json.Marshal(map[string]any{"checklist": checks, "scores": scores, "observations": "mock six-frame inspection"})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "object": "chat.completion", "created": 1, "model": "mock-vision", "choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(judgement)}}}})
	}))
	t.Cleanup(server.Close)
	t.Setenv("SUB2API_PELICAN_PYTHON", python)
	t.Setenv("JUDGE_BASE_URL", server.URL+"/v1")
	t.Setenv("JUDGE_MODEL", "mock-vision")
	t.Setenv("JUDGE_API_KEY", "fixture-only")
	t.Setenv("JUDGE_PROTOCOL", "chat_completions")
	t.Setenv("JUDGE_SAMPLES", "3")
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><circle cx="20" cy="50" r="10"><animate attributeName="cx" values="20;80;20" dur="2s" repeatCount="indefinite"/></circle></svg>`
	result := runAutoPelicanJudge(context.Background(), svg, intelligentAnimalHTMLPrompt("鹈鹕"), 70)
	require.Equal(t, "passed", result.State, result.Reason)
	require.InDelta(t, 80, *result.Score, 0.01)
	require.EqualValues(t, 3, requests.Load())
	require.Len(t, result.FrameTimes, 6)
	result = runAutoPelicanJudge(context.Background(), "将以下内容保存为 HTML。\n```html\n"+`<!DOCTYPE html><html style="padding:0"><head><meta charset="utf-8"><title>动画</title></head><body style="margin: 0px 0; padding:0; min-height:100vh; display:grid; place-items:center; background:#eaf4ef">`+svg+"</body></html>\n```\n可直接打开。", intelligentAnimalHTMLPrompt("豆娘"), 70)
	require.Equal(t, "passed", result.State, result.Reason)
	require.Equal(t, "animal-judge-v1", result.Rubric)
	require.EqualValues(t, 3, animalRequests.Load(), "every review must judge the requested species")
	require.EqualValues(t, 6, requests.Load())
	result = runAutoPelicanJudge(context.Background(), svg, intelligentAnimalHTMLPrompt("哆啦A梦"), 50)
	require.Equal(t, "passed", result.State, result.Reason)
	require.Equal(t, "animal-judge-v1", result.Rubric)
	require.Equal(t, 50, result.Threshold)
	require.EqualValues(t, 3, characterRequests.Load(), "every review must judge the requested character")
	require.EqualValues(t, 9, requests.Load())
	failNext.Store(true)
	result = runAutoPelicanJudge(context.Background(), svg, intelligentAnimalHTMLPrompt("鹈鹕"), 70)
	require.Equal(t, "passed", result.State, result.Reason)
	require.EqualValues(t, 3, result.SampleCount)
	require.EqualValues(t, 13, requests.Load(), "a transient 502 is retried without discarding successful samples")
	for _, unsafe := range []string{
		svg + `<script>draw()</script>`,
		`<html><body style="opacity:0">` + svg + `</body></html>`,
		`<html><body style="margin:0;position:fixed">` + svg + `</body></html>`,
		`<html><body style="margin:0;display:none">` + svg + `</body></html>`,
		`<html><body>` + svg + `<p>another visible object</p></body></html>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><image href="file:///etc/passwd"/></svg>`,
		`<svg viewBox="0 0 100 100"><use href="#x"><set attributeName="href" to="/etc/passwd"/></use></svg>`,
		`<style>circle { fill: red; }</style>` + svg,
		"```svg\n" + svg + "\n```\n```svg\n" + svg + "\n```",
	} {
		result = runAutoPelicanJudge(context.Background(), unsafe, intelligentAnimalHTMLPrompt("鹈鹕"), 70)
		require.Equal(t, "unavailable", result.State)
		require.Nil(t, result.Score)
	}
	require.EqualValues(t, 9, requests.Load(), "unsupported renders must never call a judge")
	fail.Store(true)
	result = runAutoPelicanJudge(context.Background(), svg, intelligentAnimalHTMLPrompt("鹈鹕"), 70)
	require.Equal(t, "unavailable", result.State)
	require.Nil(t, result.Score, "transport failures must never become zero-quality scores")
}

func TestAutoPelicanOutputLimit(t *testing.T) {
	var output limitedPelicanOutput
	// os/exec copies child stdout through io.Copy; ReaderFrom must not bypass
	// the limit when a child emits an unexpectedly large response.
	_, err := io.Copy(&output, io.LimitReader(strings.NewReader(strings.Repeat("x", 33<<10)), 33<<10))
	require.Error(t, err)
	require.LessOrEqual(t, len(output.Bytes()), 32<<10)
}

func TestAutoPelicanScoreIsDisplayOnly(t *testing.T) {
	score := 35.0
	svc := &IntelligentTestService{visualJudge: func(context.Context, string, string, int) *PelicanVisualAssessment {
		return &PelicanVisualAssessment{State: "anomalous", Score: &score, Threshold: 70}
	}}
	record := &IntelligentTestRecord{TestType: "pelican", Status: "completed", ConfigSnapshot: &IntelligentTestConfig{VisualJudgeEnabled: true}}
	// No repository is provided: scoring cannot reach account/group writes.
	svc.scorePelican(context.Background(), record)
	require.Equal(t, "completed", record.Status)
	require.Equal(t, 35.0, *record.Score)
	public := PublicIntelligentAssessment(record.Evaluation)
	require.Equal(t, "anomalous", public["visual_judgement"].(*PelicanVisualAssessment).State)
	require.Equal(t, "insufficient_evidence", public["capability_verdict"])
	malformed := decodePelicanVisualAssessment([]byte(`{"state":"passed","score":0}`), 70)
	require.Equal(t, "unavailable", malformed.State)
	require.Nil(t, malformed.Score)
	failed := decodePelicanVisualAssessment([]byte(`{"state":"unavailable","score":0,"reason":"offline"}`), 70)
	require.Nil(t, failed.Score)
}

func TestResolveIntelligentTestModelDefaultsChatGPTToGpt6Astra(t *testing.T) {
	t.Parallel()
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.Equal(t, "gpt-6-astra", resolveIntelligentTestModel(oauth, ""))
	require.Equal(t, "gpt-6-astra", resolveIntelligentTestModel(oauth, "  "))
	require.Equal(t, "gpt-5.3-codex", resolveIntelligentTestModel(oauth, "gpt-5.3-codex"))
	require.Equal(t, "", resolveIntelligentTestModel(&Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}, ""))
	require.Equal(t, "", resolveIntelligentTestModel(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, ""))
}

func TestPelicanUsesHTTPTransport(t *testing.T) {
	t.Parallel()
	pelican := context.WithValue(context.Background(), intelligentRunKey{}, &intelligentRunContext{testType: "pelican"})
	candy := context.WithValue(context.Background(), intelligentRunKey{}, &intelligentRunContext{testType: "candy"})
	require.True(t, pelicanUsesHTTPTransport(pelican, true))
	require.False(t, pelicanUsesHTTPTransport(pelican, false))
	require.False(t, pelicanUsesHTTPTransport(candy, true))
	require.False(t, pelicanUsesHTTPTransport(context.Background(), true))
}

func TestApplyIntelligentPayloadPromptPinsReasoningByTestType(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		testType string
		effort   string
	}{
		{"tibo", "xhigh"},
		{"candy", "low"},
		{"pelican", "low"},
		{"custom", "low"},
		{"", "low"},
	} {
		t.Run(tc.testType, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), intelligentRunKey{}, &intelligentRunContext{prompt: "test prompt", testType: tc.testType})

			responses := createOpenAITestPayload("gpt-6-astra", true)
			applyIntelligentPayloadPrompt(ctx, responses)
			raw, err := json.Marshal(responses)
			require.NoError(t, err)
			require.Equal(t, "gpt-6-astra", responses["model"])
			require.Equal(t, map[string]any{"effort": tc.effort}, responses["reasoning"])
			require.Contains(t, string(raw), `"text":"test prompt"`)

			chat := createOpenAIChatCompletionsTestPayload("gpt-6-astra", "hi")
			applyIntelligentPayloadPrompt(ctx, chat)
			require.Equal(t, "gpt-6-astra", chat["model"])
			require.Equal(t, tc.effort, chat["reasoning_effort"])
			require.Nil(t, chat["reasoning"])

			claude, err := createTestPayload("claude-sonnet-4-5-20250929")
			require.NoError(t, err)
			applyIntelligentPayloadPrompt(ctx, claude)
			require.NotContains(t, claude, "reasoning")
			require.NotContains(t, claude, "reasoning_effort")
		})
	}

	untouched := createOpenAITestPayload("gpt-6-astra", true)
	applyIntelligentPayloadPrompt(context.Background(), untouched)
	_, hasReasoning := untouched["reasoning"]
	require.False(t, hasReasoning)
	input := mustTestValue[[]map[string]any](t, untouched["input"])
	content := mustTestValue[[]map[string]any](t, input[0]["content"])
	require.Equal(t, "hi", content[0]["text"])
}

func TestClassifyIntelligentErrorTreatsCodexPlanGateAsModelError(t *testing.T) {
	t.Parallel()
	msg := `API returned 400: {"detail":"The 'gpt-5.3-codex' model is not supported when using Codex with a ChatGPT account."}`
	require.Equal(t, "model_error", classifyIntelligentError(400, msg))
	require.Equal(t, "model_error", classifyIntelligentError(0, msg))
	require.Equal(t, "request_error", classifyIntelligentError(400, "API returned 400: invalid json"))
	require.Equal(t, "account_error", classifyIntelligentError(0, "account not found"))
}
