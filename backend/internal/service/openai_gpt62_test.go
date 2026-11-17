//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGPT62IdentityEffortAndCompatibility(t *testing.T) {
	for _, base := range []string{"gpt-6.2-astra", "gpt-6.2-sol"} {
		t.Run(base, func(t *testing.T) {
			require.Contains(t, openai.DefaultModelIDs(), base)
			require.True(t, isOpenAIOAuthServableModel(base))
			require.True(t, isOpenAICodexImageInputModel(base))
			require.True(t, shouldAutoInjectPromptCacheKeyForCompat(base))
			require.True(t, openai.RequiresResponsesForToolCalls(base))
			for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
				model := "openai/" + strings.ToUpper(base) + "-" + effort
				require.Equal(t, base, normalizeKnownOpenAICodexModel(model))
				require.Equal(t, base, normalizeCodexModel(model))
				require.Equal(t, base, NormalizeOpenAICompatRequestedModel(model))
				require.Equal(t, base, normalizeOpenAIModelForUpstream(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, model))
				require.Equal(t, effort, normalizeOpenAIReasoningEffortForModel(effort, model))
				req := &apicompat.AnthropicRequest{Model: model}
				applyOpenAICompatModelNormalization(req)
				require.Equal(t, base, req.Model)
				require.Equal(t, effort, req.OutputConfig.Effort)
				converted, err := apicompat.AnthropicToResponses(req)
				require.NoError(t, err)
				require.Equal(t, effort, converted.Reasoning.Effort)
				chat, err := apicompat.ChatCompletionsToResponses(&apicompat.ChatCompletionsRequest{Model: base, ReasoningEffort: effort})
				require.NoError(t, err)
				require.Equal(t, base, chat.Model)
				require.Equal(t, effort, chat.Reasoning.Effort)
			}
			for _, effort := range []string{"none", "minimal"} {
				for _, field := range []string{`"reasoning":{"effort":"` + effort + `"}`, `"reasoning_effort":"` + effort + `"`, `"output_config":{"effort":"` + effort + `"}`, `"model":"` + base + `-` + effort + `"`} {
					require.ErrorContains(t, validateGPT61CompatRequest([]byte("{"+field+"}"), base), base+" does not support")
				}
				_, err := apicompat.ChatCompletionsToResponses(&apicompat.ChatCompletionsRequest{Model: base, ReasoningEffort: effort})
				require.ErrorContains(t, err, base+" does not support")
			}
			require.Error(t, validateGPT61CompatRequest([]byte(`{"thinking":{"type":"disabled"}}`), base))
			require.False(t, openai.IsGPT62ModelSpelling(base+"-preview"))
			require.False(t, openai.IsGPT62ModelSpelling(base+"-ultra"))
			require.Equal(t, base, normalizeKnownOpenAICodexModel(base+"-openai-compact"))
			body := []byte(`{"temperature":0.7,"top_p":0.9,"logprobs":true,"top_logprobs":1,"include":["reasoning.encrypted_content","message.output_text.logprobs"],"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`)
			got, changed, err := normalizeGPT6ResponsesSampling(body, base)
			require.NoError(t, err)
			require.True(t, changed)
			for _, key := range []string{"temperature", "top_p", "logprobs", "top_logprobs"} {
				require.False(t, gjson.GetBytes(got, key).Exists())
			}
			require.Equal(t, `["reasoning.encrypted_content"]`, gjson.GetBytes(got, "include").Raw)
			require.Equal(t, gjson.GetBytes(body, "input").Raw, gjson.GetBytes(got, "input").Raw)
		})
	}
	require.Equal(t, "gpt-6-astra", normalizeKnownOpenAICodexModel("gpt-6"))
	require.Equal(t, "gpt-6.1-sol", normalizeKnownOpenAICodexModel("gpt-6.1-sol"))
}

func TestGPT62OfficialCatalogAndRouteCapabilities(t *testing.T) {
	for _, tc := range []struct {
		model    string
		window   int
		metadata []byte
	}{
		{"gpt-6.2-astra", 272000, openai.CodexGPT62AstraMetadata},
		{"gpt-6.2-sol", 500000, openai.CodexGPT62SolMetadata},
	} {
		t.Run(tc.model, func(t *testing.T) {
			body, err := BuildCodexModelsManifest([]string{tc.model})
			require.NoError(t, err)
			models := decodeCodexManifestModels(t, body)
			require.Len(t, models, 1)
			m := models[0]
			require.Equal(t, tc.model, m["slug"])
			require.EqualValues(t, tc.window, m["context_window"])
			require.EqualValues(t, 872000, m["max_context_window"])
			require.Equal(t, []string{"low", "medium", "high", "xhigh", "max", "ultra"}, effortsFromManifestModel(t, m))
			require.Equal(t, []any{"text", "image"}, m["input_modalities"])
			require.Equal(t, "xhigh", m["multi_agent_reasoning_effort"])
			require.Equal(t, "v2", m["multi_agent_version"])
			require.Equal(t, "low", m["default_reasoning_level"])
			wantPrompt := gjson.GetBytes(tc.metadata, "model_messages.instructions_template").String()
			require.NotEmpty(t, wantPrompt)
			require.Equal(t, wantPrompt, openai.CodexBaseInstructionsForModel(tc.model))
			require.Equal(t, wantPrompt, m["model_messages"].(map[string]any)["instructions_template"])
			for _, accountType := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
				baseURL := "https://api.openai.com"
				if accountType == AccountTypeOAuth {
					baseURL = "https://chatgpt.com"
				}
				account := &Account{Platform: PlatformOpenAI, Type: accountType, Credentials: map[string]any{"base_url": baseURL}}
				caps := accountCodexToolCapabilities(account, tc.model)
				require.Equal(t, accountType == AccountTypeOAuth, string(caps["use_responses_lite"]) == "true")
				require.Contains(t, string(caps["supports_search_tool"]), "true")
				if accountType == AccountTypeAPIKey {
					require.Contains(t, string(caps["service_tiers"]), "ultrafast")
					adjusted, err := adjustAPIKeyCodexModelsManifest(body, account)
					require.NoError(t, err)
					require.False(t, gjson.GetBytes(adjusted, "models.0.use_responses_lite").Bool())
				} else {
					require.NotContains(t, string(caps["service_tiers"]), "ultrafast")
				}
			}
		})
	}
}

func TestGPT62ChatOnlyRouteRejectsTools(t *testing.T) {
	for _, model := range []string{"gpt-6.2-astra", "gpt-6.2-sol"} {
		for _, protocol := range []string{"chat", "responses", "messages"} {
			body := `{"model":"public","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`
			if protocol == "responses" {
				body = `{"model":"public","input":"hi","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`
			}
			if protocol == "messages" {
				body = `{"model":"public","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`
			}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+protocol, strings.NewReader(body))
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"public": model}}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			var err error
			switch protocol {
			case "chat":
				_, err = svc.forwardAsRawChatCompletions(context.Background(), c, account, []byte(body), "")
			case "responses":
				_, err = svc.forwardResponsesViaRawChatCompletions(context.Background(), c, account, []byte(body), time.Now())
			case "messages":
				_, err = svc.forwardAnthropicViaRawChatCompletions(context.Background(), c, account, []byte(body), "")
			}
			require.ErrorContains(t, err, model+" requires Responses")
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		}
	}
}

func TestGPT62PricingFallbackTiersAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		model                string
		input, output, cache float64
	}{
		{"gpt-6.2-astra", 12e-6, 60e-6, 0.6e-6},
		{"gpt-6.2-sol", 4e-6, 20e-6, 0.2e-6},
	} {
		t.Run(tc.model, func(t *testing.T) {
			for _, ps := range []*PricingService{nil, {pricingData: map[string]*LiteLLMModelPricing{}}} {
				svc := NewBillingService(&config.Config{}, ps)
				pricing, err := svc.getModelPricingAt("openai/"+tc.model+"-max", time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC))
				require.NoError(t, err)
				require.InDelta(t, tc.input, pricing.InputPricePerToken, 1e-12)
				require.InDelta(t, tc.output, pricing.OutputPricePerToken, 1e-12)
				require.InDelta(t, tc.cache, pricing.CacheReadPricePerToken, 1e-12)
				require.InDelta(t, tc.input*1.25, pricing.CacheCreationPricePerToken, 1e-12)
				require.InDelta(t, tc.input*2.5, *pricing.CacheCreation12hPricePerToken, 1e-12)
				for tier, factor := range map[string]float64{"": 1, "priority": 2, "ultrafast": 3} {
					cost := svc.computeTokenBreakdown(pricing, UsageTokens{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 20, CacheCreationTokens: 4, CacheCreation30mTokens: 2, CacheCreation12hTokens: 2}, 1, tier, false)
					want := (100*tc.input + 10*tc.output + 20*tc.cache + 2*tc.input*1.25 + 2*tc.input*2.5) * factor
					require.InDelta(t, want, cost.TotalCost, 1e-10, tier)
				}
				cost := svc.computeTokenBreakdown(pricing, UsageTokens{InputTokens: 272001, OutputTokens: 10}, 1, "", true)
				require.True(t, cost.LongContextBillingApplied)
				require.InDelta(t, 272001*tc.input*2+10*tc.output*1.5, cost.TotalCost, 1e-9)
				custom := &ModelPricing{InputPricePerToken: 7e-6, OutputPricePerToken: 11e-6, CacheCreationPriceExplicit: true, CacheCreationPricePerToken: 0}
				got := svc.applyModelSpecificPricingPolicyEx(tc.model, custom, false, time.Now())
				require.Equal(t, custom.InputPricePerToken, got.InputPricePerToken)
				require.Equal(t, custom.OutputPricePerToken, got.OutputPricePerToken)
				require.Zero(t, got.CacheCreationPricePerToken)
				require.Nil(t, got.CacheCreation12hPricePerToken)
			}
		})
	}
	cutoff := time.Date(2027, 3, 1, 8, 0, 0, 0, time.UTC)
	require.Equal(t, 3.0, gpt62UltrafastMultiplierAt(cutoff.Add(-time.Nanosecond)))
	require.Equal(t, 6.0, gpt62UltrafastMultiplierAt(cutoff))
}

func TestGPT62BundledOfficialPricing(t *testing.T) {
	raw, err := os.ReadFile("../../resources/model-pricing/model_prices_and_context_window.json")
	require.NoError(t, err)
	var entries map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &entries))
	for model, rates := range openAIGPT62FallbackPricing {
		require.Contains(t, entries, model)
		entry := gjson.ParseBytes(entries[model])
		require.InDelta(t, rates.InputCostPerToken, entry.Get("input_cost_per_token").Float(), 1e-12)
		require.InDelta(t, rates.OutputCostPerToken, entry.Get("output_cost_per_token").Float(), 1e-12)
		require.InDelta(t, rates.CacheReadInputTokenCost, entry.Get("cache_read_input_token_cost").Float(), 1e-12)
		require.EqualValues(t, 1050000, entry.Get("context_window").Int())
	}
}
