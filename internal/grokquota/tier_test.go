package grokquota

import "testing"

func TestMapSubscriptionTierToPlanKey(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"supergrok", "super"},
		{"SuperGrok", "super"},
		{"  SUPER  ", "super"},
		{"heavygrok", "heavy"},
		{"heavy", "heavy"},
		{"HeavyGrok", "heavy"},
		{"free", "free"},
		{"FREE", "free"},
		{"  free  ", "free"},
		{"unknown-tier", "unknown-tier"},
		{"ProPlus", "proplus"},
		{"", ""},
		{"   ", ""},
		// grok.com /rest/subscriptions 枚举
		{"SUBSCRIPTION_TIER_GROK_PRO", "super"},
		{"SUBSCRIPTION_TIER_SUPER_GROK_PRO", "heavy"},
		{"SUBSCRIPTION_TIER_SUPER_GROK_LITE", "super_lite"},
		{"subscription_tier_free", "free"},
	}
	for _, tt := range tests {
		if got := MapSubscriptionTierToPlanKey(tt.raw); got != tt.want {
			t.Fatalf("MapSubscriptionTierToPlanKey(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestMapGrokWebSubscriptionToPlanKey(t *testing.T) {
	// HAR 样例：isSuperGrokUser + SUBSCRIPTION_TIER_GROK_PRO → super
	har := GrokWebSubscription{
		Observed:            true,
		IsSuperGrokLiteUser: false,
		IsSuperGrokUser:     true,
		IsSuperGrokProUser:  false,
		BestSubscription:    "SUBSCRIPTION_TIER_GROK_PRO",
		ActiveTier:          "SUBSCRIPTION_TIER_GROK_PRO",
		ActiveStatus:        "SUBSCRIPTION_STATUS_ACTIVE",
		BillingPeriodEnd:    "2026-07-19T15:42:24Z",
	}
	if got := MapGrokWebSubscriptionToPlanKey(har); got != "super" {
		t.Fatalf("HAR sample planKey = %q, want super", got)
	}
	if got := har.RawTier(); got != "SUBSCRIPTION_TIER_GROK_PRO" {
		t.Fatalf("RawTier = %q", got)
	}

	// ProUser 优先 heavy
	pro := GrokWebSubscription{Observed: true, IsSuperGrokProUser: true, BestSubscription: "SUBSCRIPTION_TIER_SUPER_GROK_PRO"}
	if got := MapGrokWebSubscriptionToPlanKey(pro); got != "heavy" {
		t.Fatalf("ProUser planKey = %q, want heavy", got)
	}

	// Lite flag
	lite := GrokWebSubscription{Observed: true, IsSuperGrokLiteUser: true}
	if got := MapGrokWebSubscriptionToPlanKey(lite); got != "super_lite" {
		t.Fatalf("Lite planKey = %q, want super_lite", got)
	}

	// !Observed 无 flag、无 tier → 未知，不写库
	if got := MapGrokWebSubscriptionToPlanKey(GrokWebSubscription{}); got != "" {
		t.Fatalf("empty planKey = %q, want \"\"", got)
	}

	// Observed 且无付费信号 → free（合法空列表）
	if got := MapGrokWebSubscriptionToPlanKey(GrokWebSubscription{Observed: true}); got != "free" {
		t.Fatalf("observed empty = %q, want free", got)
	}

	// 显式 free tier
	if got := MapGrokWebSubscriptionToPlanKey(GrokWebSubscription{Observed: true, BestSubscription: "subscription_tier_free"}); got != "free" {
		t.Fatalf("explicit free = %q, want free", got)
	}

	// 仅枚举 heavy
	if got := MapGrokWebSubscriptionToPlanKey(GrokWebSubscription{Observed: true, BestSubscription: "SUBSCRIPTION_TIER_SUPER_GROK_PRO"}); got != "heavy" {
		t.Fatalf("tier-only heavy = %q", got)
	}
}

func TestSubscriptionFieldsFromSnapshot(t *testing.T) {
	tier, ent := SubscriptionFieldsFromSnapshot(nil)
	if tier != "" || ent != "" {
		t.Fatalf("nil snapshot => %q/%q, want empty", tier, ent)
	}
	tier, ent = SubscriptionFieldsFromSnapshot(&UsageSnapshot{
		SubscriptionTier:  "  supergrok  ",
		EntitlementStatus: " active ",
	})
	if tier != "supergrok" || ent != "active" {
		t.Fatalf("got %q/%q", tier, ent)
	}
}
