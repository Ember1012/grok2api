package grokquota

import "strings"

// SubscriptionFieldsFromSnapshot 从用量快照取出上游原始套餐字段（原样，仅 trim）。
func SubscriptionFieldsFromSnapshot(s *UsageSnapshot) (subscriptionTier, entitlementStatus string) {
	if s == nil {
		return "", ""
	}
	return strings.TrimSpace(s.SubscriptionTier), strings.TrimSpace(s.EntitlementStatus)
}

// MapSubscriptionTierToPlanKey 将 xAI / grok.com subscription_tier 映射为现有 UI 用的 plan_type 键。
// 兼容 header 短名（supergrok/heavy）与 web 枚举（SUBSCRIPTION_TIER_*）。
// 未知值返回 lower(trim(raw))，不猜测。
func MapSubscriptionTierToPlanKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	// 顺序：heavy/pro 先于 super，lite 先于 super，避免 SUPER_GROK_PRO 误映射为 super。
	switch {
	case lower == "free" || strings.Contains(lower, "subscription_tier_free") || strings.HasSuffix(lower, "_free"):
		return "free"
	case strings.Contains(lower, "super_grok_pro") || strings.Contains(lower, "heavy"):
		return "heavy"
	case strings.Contains(lower, "lite"):
		return "super_lite"
	case strings.Contains(lower, "grok_pro") || strings.Contains(lower, "super"):
		return "super"
	default:
		return lower
	}
}

// GrokWebSubscription 是 grok.com /rest/subscriptions 解析后的最小套餐视图（不含 payment 细节）。
type GrokWebSubscription struct {
	// Observed 表示 200 且 body 可识别为 REST subscriptions 列表或 SSR 套餐字段（含合法空列表）。
	// 未观测（如 {}）时不得写 plan_type。
	Observed            bool
	IsSuperGrokLiteUser bool
	IsSuperGrokUser     bool
	IsSuperGrokProUser  bool
	BestSubscription    string
	ActiveTier          string
	ActiveStatus        string
	BillingPeriodEnd    string // RFC3339，空表示未知
}

// RawTier 用于 credentials.subscription_tier：优先 bestSubscription，否则 active tier。
func (s GrokWebSubscription) RawTier() string {
	if t := strings.TrimSpace(s.BestSubscription); t != "" {
		return t
	}
	return strings.TrimSpace(s.ActiveTier)
}

// MapGrokWebSubscriptionToPlanKey 账单页 SuperGrok 权威映射（flags + tier 枚举）。
// flag 优先 heavy > super > super_lite；再 raw tier；Observed 且无付费信号 → free；!Observed → "" 不写。
func MapGrokWebSubscriptionToPlanKey(s GrokWebSubscription) string {
	if s.IsSuperGrokProUser {
		return "heavy"
	}
	if s.IsSuperGrokUser {
		return "super"
	}
	if s.IsSuperGrokLiteUser {
		return "super_lite"
	}
	raw := s.RawTier()
	if raw != "" {
		return MapSubscriptionTierToPlanKey(raw)
	}
	if s.Observed {
		// 合法空列表 / SSR free：无付费 flag 与 raw tier
		return "free"
	}
	// 未观测（如 {}）：未知，勿写库
	return ""
}
