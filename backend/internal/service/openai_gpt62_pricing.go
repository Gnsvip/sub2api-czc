package service

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// Official Standard API prices, verified 2026-11-18:
// https://developers.openai.com/api/docs/models/gpt-6.2-astra
// https://developers.openai.com/api/docs/models/gpt-6.2-sol
var openAIGPT62FallbackPricing = map[string]*LiteLLMModelPricing{
	"gpt-6.2-astra": newGPT62OfficialPricing(8e-6, 40e-6, 0.4e-6),
	"gpt-6.2-sol":   newGPT62OfficialPricing(2e-6, 10e-6, 0.1e-6),
}

func newGPT62OfficialPricing(input, output, cacheRead float64) *LiteLLMModelPricing {
	return &LiteLLMModelPricing{
		InputCostPerToken: input, OutputCostPerToken: output,
		CacheReadInputTokenCost: cacheRead, CacheCreationInputTokenCost: input * 1.25,
		InputCostPerTokenPriority: input * 2, OutputCostPerTokenPriority: output * 2,
		CacheReadInputTokenCostPriority: cacheRead * 2, CacheCreationInputTokenCostPriority: input * 2.5,
		LongContextInputTokenThreshold: 272_000, LongContextInputCostMultiplier: 2, LongContextOutputCostMultiplier: 1.5,
		SupportsServiceTier: true, SupportsPromptCaching: true, LiteLLMProvider: "openai", Mode: "chat",
	}
}

// Default site pricing follows the existing family policy: Astra 1.5x,
// Sol 2x. Callers must not apply this override to operator-defined price cards.
func applyGPT62APIBillingRates(model string, pricing *ModelPricing) {
	base := openai.CanonicalGPT62Model(model)
	rates := openAIGPT62FallbackPricing[base]
	if pricing == nil || rates == nil {
		return
	}
	multiplier := gpt6SolAPIBillingMultiplier
	if base == "gpt-6.2-astra" {
		multiplier = gpt6AstraAPIBillingMultiplier
	}
	pricing.InputPricePerToken = rates.InputCostPerToken * multiplier
	pricing.OutputPricePerToken = rates.OutputCostPerToken * multiplier
	pricing.CacheReadPricePerToken = rates.CacheReadInputTokenCost * multiplier
	pricing.CacheCreationPricePerToken = rates.CacheCreationInputTokenCost * multiplier
	price12h := pricing.InputPricePerToken * 2.5
	pricing.CacheCreation12hPricePerToken = &price12h
	if pricing.LongContextInputThreshold <= 0 {
		pricing.LongContextInputThreshold = openAIAPILongContextInputTokenThreshold
		pricing.LongContextInputMultiplier = openAIAPILongContextInputMultiplier
		pricing.LongContextOutputMultiplier = openAIAPILongContextOutputMultiplier
		pricing.LongContextThresholdInclusive = false
	}
}

func gpt62UltrafastMultiplierAt(at time.Time) float64 {
	if at.IsZero() {
		at = time.Now()
	}
	// March 1, 2027 00:00 Pacific is still PST (UTC-8).
	if at.Before(time.Date(2027, time.March, 1, 8, 0, 0, 0, time.UTC)) {
		return 3
	}
	return 6
}
