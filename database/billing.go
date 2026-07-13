package database

import (
	"encoding/json"
	"strings"
	"sync/atomic"
)

const longContextThreshold = 272000

type ModelPricing struct {
	InputPricePerMToken             float64
	InputPricePerMTokenPriority     float64
	OutputPricePerMToken            float64
	OutputPricePerMTokenPriority    float64
	CacheReadPricePerMToken         float64
	CacheReadPricePerMTokenPriority float64

	LongInputPricePerMToken             float64
	LongInputPricePerMTokenPriority     float64
	LongOutputPricePerMToken            float64
	LongOutputPricePerMTokenPriority    float64
	LongCacheReadPricePerMToken         float64
	LongCacheReadPricePerMTokenPriority float64
}

type modelPricingRule struct {
	model   string
	pricing ModelPricing
}

type runtimeModelPriceJSON struct {
	Input     float64 `json:"input"`
	Output    float64 `json:"output"`
	CacheRead float64 `json:"cache_read"`
}

type CostBreakdown struct {
	InputCost                 float64 `json:"input_cost"`
	OutputCost                float64 `json:"output_cost"`
	CacheReadCost             float64 `json:"cache_read_cost"`
	TotalCost                 float64 `json:"total_cost"`
	InputPricePerMToken       float64 `json:"input_price_per_mtoken"`
	OutputPricePerMToken      float64 `json:"output_price_per_mtoken"`
	CacheReadPricePerMToken   float64 `json:"cache_read_price_per_mtoken"`
	ServiceTierCostMultiplier float64 `json:"service_tier_cost_multiplier"`
	LongContext               bool    `json:"long_context"`
	LongContextThreshold      int     `json:"long_context_threshold"`
}

var (
	defaultModelPricing = &ModelPricing{InputPricePerMToken: 1.0, OutputPricePerMToken: 2.0}

	// runtimeModelPricingRules holds admin-configured overrides (exact then longest prefix).
	runtimeModelPricingRules atomic.Value // []modelPricingRule

	defaultGrokPricingRules = []modelPricingRule{
		{model: "grok-4.5", pricing: ModelPricing{InputPricePerMToken: 2.0, OutputPricePerMToken: 6.0, CacheReadPricePerMToken: 0.5}},
		{model: "grok-4.3", pricing: ModelPricing{InputPricePerMToken: 1.25, OutputPricePerMToken: 2.5, CacheReadPricePerMToken: 0.2}},
		{model: "grok-build-0.1", pricing: ModelPricing{InputPricePerMToken: 1.0, OutputPricePerMToken: 2.0, CacheReadPricePerMToken: 0.2}},
		{model: "grok-latest", pricing: ModelPricing{InputPricePerMToken: 1.25, OutputPricePerMToken: 2.5, CacheReadPricePerMToken: 0.2}},
		{model: "grok", pricing: ModelPricing{InputPricePerMToken: 1.25, OutputPricePerMToken: 2.5, CacheReadPricePerMToken: 0.2}},
	}

	modelPricingRules = []modelPricingRule{
		{model: "gpt-5.5", pricing: ModelPricing{
			InputPricePerMToken:                 5.0,
			InputPricePerMTokenPriority:         12.5,
			OutputPricePerMToken:                30.0,
			OutputPricePerMTokenPriority:        75.0,
			CacheReadPricePerMToken:             0.5,
			CacheReadPricePerMTokenPriority:     1.25,
			LongInputPricePerMToken:             10.0,
			LongInputPricePerMTokenPriority:     25.0,
			LongOutputPricePerMToken:            45.0,
			LongOutputPricePerMTokenPriority:    112.5,
			LongCacheReadPricePerMToken:         1.0,
			LongCacheReadPricePerMTokenPriority: 2.5,
		}},
		{model: "gpt-5.5-pro", pricing: ModelPricing{
			InputPricePerMToken:              30.0,
			InputPricePerMTokenPriority:      75.0,
			OutputPricePerMToken:             180.0,
			OutputPricePerMTokenPriority:     450.0,
			LongInputPricePerMToken:          60.0,
			LongInputPricePerMTokenPriority:  150.0,
			LongOutputPricePerMToken:         270.0,
			LongOutputPricePerMTokenPriority: 675.0,
		}},
		{model: "gpt-5.4-mini", pricing: ModelPricing{InputPricePerMToken: 0.75, OutputPricePerMToken: 4.5, CacheReadPricePerMToken: 0.075}},
		{model: "gpt-5.4-nano", pricing: ModelPricing{InputPricePerMToken: 0.2, OutputPricePerMToken: 1.25, CacheReadPricePerMToken: 0.02}},
		{model: "gpt-5.4", pricing: ModelPricing{
			InputPricePerMToken:                 2.5,
			InputPricePerMTokenPriority:         5.0,
			OutputPricePerMToken:                15.0,
			OutputPricePerMTokenPriority:        30.0,
			CacheReadPricePerMToken:             0.25,
			CacheReadPricePerMTokenPriority:     0.5,
			LongInputPricePerMToken:             5.0,
			LongInputPricePerMTokenPriority:     10.0,
			LongOutputPricePerMToken:            22.5,
			LongOutputPricePerMTokenPriority:    45.0,
			LongCacheReadPricePerMToken:         0.5,
			LongCacheReadPricePerMTokenPriority: 1.0,
		}},
		{model: "gpt-5.4-pro", pricing: ModelPricing{
			InputPricePerMToken:              30.0,
			InputPricePerMTokenPriority:      75.0,
			OutputPricePerMToken:             180.0,
			OutputPricePerMTokenPriority:     450.0,
			LongInputPricePerMToken:          60.0,
			LongInputPricePerMTokenPriority:  150.0,
			LongOutputPricePerMToken:         270.0,
			LongOutputPricePerMTokenPriority: 675.0,
		}},
		{model: "gpt-5.3-codex-spark", pricing: ModelPricing{
			InputPricePerMToken:             1.25,
			InputPricePerMTokenPriority:     2.5,
			OutputPricePerMToken:            10.0,
			OutputPricePerMTokenPriority:    20.0,
			CacheReadPricePerMToken:         0.125,
			CacheReadPricePerMTokenPriority: 0.25,
		}},
		{model: "gpt-5.3-codex", pricing: ModelPricing{
			InputPricePerMToken:             1.75,
			InputPricePerMTokenPriority:     3.5,
			OutputPricePerMToken:            14.0,
			OutputPricePerMTokenPriority:    28.0,
			CacheReadPricePerMToken:         0.175,
			CacheReadPricePerMTokenPriority: 0.35,
		}},
		{model: "gpt-5.2", pricing: ModelPricing{
			InputPricePerMToken:             1.75,
			InputPricePerMTokenPriority:     3.5,
			OutputPricePerMToken:            14.0,
			OutputPricePerMTokenPriority:    28.0,
			CacheReadPricePerMToken:         0.175,
			CacheReadPricePerMTokenPriority: 0.35,
		}},
		{model: "gpt-4o-mini", pricing: ModelPricing{InputPricePerMToken: 0.15, OutputPricePerMToken: 0.6}},
		{model: "gpt-4o", pricing: ModelPricing{InputPricePerMToken: 2.5, OutputPricePerMToken: 10.0}},
		{model: "gpt-4-turbo", pricing: ModelPricing{InputPricePerMToken: 10.0, OutputPricePerMToken: 30.0}},
		{model: "gpt-4", pricing: ModelPricing{InputPricePerMToken: 30.0, OutputPricePerMToken: 60.0}},
		{model: "gpt-3.5-turbo", pricing: ModelPricing{InputPricePerMToken: 0.5, OutputPricePerMToken: 1.5}},
	}
)

// SetRuntimeModelPricing parses admin model pricing JSON into a thread-safe cache.
// Empty / "{}" clears the override so built-in defaults apply.
func SetRuntimeModelPricing(raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		runtimeModelPricingRules.Store([]modelPricingRule(nil))
		return
	}
	var parsed map[string]runtimeModelPriceJSON
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil || len(parsed) == 0 {
		runtimeModelPricingRules.Store([]modelPricingRule(nil))
		return
	}
	rules := make([]modelPricingRule, 0, len(parsed))
	for model, price := range parsed {
		model = normalizeBillingModelName(model)
		if model == "" {
			continue
		}
		rules = append(rules, modelPricingRule{
			model: model,
			pricing: ModelPricing{
				InputPricePerMToken:     price.Input,
				OutputPricePerMToken:    price.Output,
				CacheReadPricePerMToken: price.CacheRead,
			},
		})
	}
	runtimeModelPricingRules.Store(rules)
}

// DefaultModelPricingJSON returns built-in Grok default pricing for admin APIs.
func DefaultModelPricingJSON() string {
	return `{"grok":{"input":1.25,"output":2.5,"cache_read":0.2},"grok-4.3":{"input":1.25,"output":2.5,"cache_read":0.2},"grok-4.5":{"input":2,"output":6,"cache_read":0.5},"grok-build-0.1":{"input":1,"output":2,"cache_read":0.2},"grok-latest":{"input":1.25,"output":2.5,"cache_read":0.2}}`
}

func GetModelPricing(model string) *ModelPricing {
	normalized := normalizeBillingModelName(model)
	if pricing := runtimeModelPricing(normalized); pricing != nil {
		return pricing
	}
	if pricing := pricingFromRules(normalized, defaultGrokPricingRules); pricing != nil {
		return pricing
	}
	if pricing := claudeFamilyPricing(normalized); pricing != nil {
		return pricing
	}
	if pricing := geminiFamilyPricing(normalized); pricing != nil {
		return pricing
	}
	if codexModel, ok := normalizeCodexBillingModel(normalized); ok {
		normalized = codexModel
	}
	if pricing := modelRulePricing(normalized); pricing != nil {
		return pricing
	}
	return defaultModelPricing
}

func runtimeModelPricing(model string) *ModelPricing {
	v := runtimeModelPricingRules.Load()
	if v == nil {
		return nil
	}
	rules, ok := v.([]modelPricingRule)
	if !ok || len(rules) == 0 {
		return nil
	}
	return pricingFromRules(model, rules)
}

func CalculateCost(inputTokens, outputTokens, cachedTokens int, model string, serviceTier string) float64 {
	return CalculateCostBreakdown(inputTokens, outputTokens, cachedTokens, model, serviceTier).TotalCost
}

func CalculateCostBreakdown(inputTokens, outputTokens, cachedTokens int, model string, serviceTier string) CostBreakdown {
	pricing := GetModelPricing(model)
	isLong := inputTokens > longContextThreshold
	longContextApplied := false

	inputPrice := pricing.InputPricePerMToken
	outputPrice := pricing.OutputPricePerMToken
	cacheReadPrice := pricing.CacheReadPricePerMToken

	if isLong && pricing.LongInputPricePerMToken > 0 {
		longContextApplied = true
		inputPrice = pricing.LongInputPricePerMToken
		outputPrice = pricing.LongOutputPricePerMToken
		if pricing.LongCacheReadPricePerMToken > 0 {
			cacheReadPrice = pricing.LongCacheReadPricePerMToken
		}
	}

	tierMultiplier := serviceTierCostMultiplier(serviceTier)
	if usePriorityPricing(serviceTier, pricing) {
		tierMultiplier = 1
		if isLong && pricing.LongInputPricePerMTokenPriority > 0 {
			inputPrice = pricing.LongInputPricePerMTokenPriority
		} else if pricing.InputPricePerMTokenPriority > 0 {
			inputPrice = pricing.InputPricePerMTokenPriority
		}
		if isLong && pricing.LongOutputPricePerMTokenPriority > 0 {
			outputPrice = pricing.LongOutputPricePerMTokenPriority
		} else if pricing.OutputPricePerMTokenPriority > 0 {
			outputPrice = pricing.OutputPricePerMTokenPriority
		}
		if isLong && pricing.LongCacheReadPricePerMTokenPriority > 0 {
			cacheReadPrice = pricing.LongCacheReadPricePerMTokenPriority
		} else if pricing.CacheReadPricePerMTokenPriority > 0 {
			cacheReadPrice = pricing.CacheReadPricePerMTokenPriority
		}
	}

	if cachedTokens < 0 {
		cachedTokens = 0
	}
	if cachedTokens > inputTokens {
		cachedTokens = inputTokens
	}

	uncachedInputTokens := inputTokens
	if cacheReadPrice > 0 {
		uncachedInputTokens = inputTokens - cachedTokens
	}

	inputCost := float64(uncachedInputTokens) / 1000000.0 * inputPrice
	cacheReadCost := float64(cachedTokens) / 1000000.0 * cacheReadPrice
	outputCost := float64(outputTokens) / 1000000.0 * outputPrice

	return CostBreakdown{
		InputCost:                 inputCost * tierMultiplier,
		OutputCost:                outputCost * tierMultiplier,
		CacheReadCost:             cacheReadCost * tierMultiplier,
		TotalCost:                 (inputCost + cacheReadCost + outputCost) * tierMultiplier,
		InputPricePerMToken:       inputPrice * tierMultiplier,
		OutputPricePerMToken:      outputPrice * tierMultiplier,
		CacheReadPricePerMToken:   cacheReadPrice * tierMultiplier,
		ServiceTierCostMultiplier: tierMultiplier,
		LongContext:               longContextApplied,
		LongContextThreshold:      longContextThreshold,
	}
}

func normalizeBillingModelName(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	model = strings.TrimLeft(model, "/")
	model = strings.TrimPrefix(model, "models/")
	model = strings.TrimPrefix(model, "publishers/google/models/")
	if idx := strings.LastIndex(model, "/publishers/google/models/"); idx != -1 {
		model = model[idx+len("/publishers/google/models/"):]
	}
	if idx := strings.LastIndex(model, "/models/"); idx != -1 {
		model = model[idx+len("/models/"):]
	} else if idx := strings.LastIndex(model, "/"); idx != -1 {
		model = model[idx+1:]
	}
	return strings.TrimLeft(model, "/")
}

func normalizeCodexBillingModel(model string) (string, bool) {
	compact := strings.NewReplacer(" ", "-", "_", "-").Replace(strings.ToLower(model))
	switch {
	case strings.Contains(compact, "gpt-5.5-pro") || strings.Contains(compact, "gpt5-5-pro") || strings.Contains(compact, "gpt5.5-pro"):
		return "gpt-5.5-pro", true
	case strings.Contains(compact, "gpt-5.5") || strings.Contains(compact, "gpt5-5") || strings.Contains(compact, "gpt5.5"):
		return "gpt-5.5", true
	case strings.Contains(compact, "gpt-5.4-mini") || strings.Contains(compact, "gpt5-4-mini") || strings.Contains(compact, "gpt5.4-mini"):
		return "gpt-5.4-mini", true
	case strings.Contains(compact, "gpt-5.4-nano") || strings.Contains(compact, "gpt5-4-nano") || strings.Contains(compact, "gpt5.4-nano"):
		return "gpt-5.4-nano", true
	case strings.Contains(compact, "gpt-5.4-pro") || strings.Contains(compact, "gpt5-4-pro") || strings.Contains(compact, "gpt5.4-pro"):
		return "gpt-5.4-pro", true
	case strings.Contains(compact, "gpt-5.4") || strings.Contains(compact, "gpt5-4") || strings.Contains(compact, "gpt5.4"):
		return "gpt-5.4", true
	case strings.Contains(compact, "gpt-5.2") || strings.Contains(compact, "gpt5-2") || strings.Contains(compact, "gpt5.2"):
		return "gpt-5.2", true
	case strings.Contains(compact, "gpt-5.3-codex-spark") || strings.Contains(compact, "gpt5-3-codex-spark") || strings.Contains(compact, "gpt5.3-codex-spark"):
		return "gpt-5.3-codex-spark", true
	case strings.Contains(compact, "gpt-5.3-codex") || strings.Contains(compact, "gpt5-3-codex") || strings.Contains(compact, "gpt5.3-codex"):
		return "gpt-5.3-codex", true
	case strings.Contains(compact, "gpt-5.3") || strings.Contains(compact, "gpt5-3") || strings.Contains(compact, "gpt5.3"):
		return "gpt-5.3-codex", true
	case strings.Contains(compact, "codex-auto-review"):
		// Codex internal auto-review model. ChatGPT backend API only
		// (chatgpt.com/backend-api/codex). Not available via public API.
		// Official catalog: Plus/Pro/Team/Business only, excludes free.
		// Specs match gpt-5.4 (272K context, 4 thinking levels).
		return "gpt-5.4", true
	case strings.Contains(compact, "codex"):
		return "gpt-5.3-codex", true
	case strings.Contains(compact, "gpt-5") || strings.Contains(compact, "gpt5"):
		return "gpt-5.4", true
	default:
		return "", false
	}
}

func modelRulePricing(model string) *ModelPricing {
	return pricingFromRules(model, modelPricingRules)
}

func pricingFromRules(model string, rules []modelPricingRule) *ModelPricing {
	bestIdx := -1
	bestLen := -1
	for i := range rules {
		rule := rules[i]
		if modelMatchesRule(model, rule.model) && len(rule.model) > bestLen {
			bestIdx = i
			bestLen = len(rule.model)
		}
	}
	if bestIdx == -1 {
		return nil
	}
	return &rules[bestIdx].pricing
}

func modelMatchesRule(model string, rule string) bool {
	if model == rule {
		return true
	}
	if !strings.HasPrefix(model, rule) {
		return false
	}
	if len(model) == len(rule) {
		return true
	}
	switch model[len(rule)] {
	case '-', '.', ':':
		return true
	default:
		return false
	}
}

func claudeFamilyPricing(model string) *ModelPricing {
	switch {
	case strings.Contains(model, "opus"):
		if strings.Contains(model, "4.7") || strings.Contains(model, "4-7") ||
			strings.Contains(model, "4.6") || strings.Contains(model, "4-6") ||
			strings.Contains(model, "4.5") || strings.Contains(model, "4-5") {
			return &ModelPricing{InputPricePerMToken: 5.0, OutputPricePerMToken: 25.0}
		}
		return &ModelPricing{InputPricePerMToken: 15.0, OutputPricePerMToken: 75.0}
	case strings.Contains(model, "sonnet"):
		return &ModelPricing{InputPricePerMToken: 3.0, OutputPricePerMToken: 15.0}
	case strings.Contains(model, "haiku"):
		if strings.Contains(model, "3-5") || strings.Contains(model, "3.5") {
			return &ModelPricing{InputPricePerMToken: 1.0, OutputPricePerMToken: 5.0}
		}
		return &ModelPricing{InputPricePerMToken: 0.25, OutputPricePerMToken: 1.25}
	case strings.Contains(model, "claude"):
		return &ModelPricing{InputPricePerMToken: 3.0, OutputPricePerMToken: 15.0}
	default:
		return nil
	}
}

func geminiFamilyPricing(model string) *ModelPricing {
	if strings.Contains(model, "gemini-3.1-pro") || strings.Contains(model, "gemini-3-1-pro") {
		return &ModelPricing{InputPricePerMToken: 2.0, OutputPricePerMToken: 12.0}
	}
	return nil
}

func usePriorityPricing(serviceTier string, pricing *ModelPricing) bool {
	tier := normalizeServiceTier(serviceTier)
	if tier != "priority" && tier != "fast" {
		return false
	}
	return pricing.InputPricePerMTokenPriority > 0 ||
		pricing.OutputPricePerMTokenPriority > 0 ||
		pricing.CacheReadPricePerMTokenPriority > 0
}

func serviceTierCostMultiplier(serviceTier string) float64 {
	switch normalizeServiceTier(serviceTier) {
	case "priority", "fast":
		return 2.0
	case "flex":
		return 0.5
	default:
		return 1.0
	}
}

func normalizeServiceTier(serviceTier string) string {
	return strings.ToLower(strings.TrimSpace(serviceTier))
}

// lowercase aliases for internal callers
func calculateCost(inputTokens, outputTokens, cachedTokens int, model string, serviceTier string) float64 {
	return CalculateCost(inputTokens, outputTokens, cachedTokens, model, serviceTier)
}

func calculateCostBreakdown(inputTokens, outputTokens, cachedTokens int, model string, serviceTier string) CostBreakdown {
	return CalculateCostBreakdown(inputTokens, outputTokens, cachedTokens, model, serviceTier)
}
