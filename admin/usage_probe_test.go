package admin

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/cache"
	"github.com/codex2api/internal/grokquota"
	"github.com/gin-gonic/gin"
)

func TestShouldMarkUsageProbeAccountError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       []byte
		want       bool
	}{
		{
			name:       "payment required deactivated workspace",
			statusCode: http.StatusPaymentRequired,
			body:       []byte(`{"detail":{"code":"deactivated_workspace"}}`),
			want:       true,
		},
		{
			name:       "forbidden deactivated workspace",
			statusCode: http.StatusForbidden,
			body:       []byte(`{"error":{"code":"deactivated_workspace"}}`),
			want:       true,
		},
		{
			name:       "generic payment required is not account error",
			statusCode: http.StatusPaymentRequired,
			body:       []byte(`{"error":{"code":"billing_hard_limit_reached"}}`),
			want:       false,
		},
		{
			name:       "rate limit handled separately",
			statusCode: http.StatusTooManyRequests,
			body:       []byte(`{"detail":{"code":"deactivated_workspace"}}`),
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldMarkUsageProbeAccountError(tt.statusCode, tt.body); got != tt.want {
				t.Fatalf("shouldMarkUsageProbeAccountError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyGrokUsageSnapshotBillingProjectsWeeklyUsage(t *testing.T) {
	resetAt := "2026-07-08T03:40:00Z"
	pct := 63.0
	resp := &accountResponse{}

	applyGrokUsageSnapshotToQuotaFields(resp, &grokquota.UsageSnapshot{
		Source:         grokquota.SourceGrokBuildBilling,
		State:          grokquota.StateObserved,
		UsedPercent:    &pct,
		APIUsedPercent: &pct,
		Window:         &grokquota.TimeWindow{ResetAt: resetAt},
	})

	if resp.UsagePercent7d == nil || *resp.UsagePercent7d != 63 {
		t.Fatalf("UsagePercent7d = %v, want 63", resp.UsagePercent7d)
	}
	if resp.Reset7dAt != resetAt {
		t.Fatalf("Reset7dAt = %q, want %q", resp.Reset7dAt, resetAt)
	}
}

func TestApplyGrokUsageSnapshotBillingZeroPercentProjectsReset(t *testing.T) {
	resetAt := "2026-07-09T03:33:00Z"
	pct := 0.0
	resp := &accountResponse{}

	applyGrokUsageSnapshotToQuotaFields(resp, &grokquota.UsageSnapshot{
		Source:      grokquota.SourceGrokBuildBilling,
		State:       grokquota.StateObserved,
		UsedPercent: &pct,
		Window:      &grokquota.TimeWindow{ResetAt: resetAt},
	})

	if resp.UsagePercent7d == nil || *resp.UsagePercent7d != 0 {
		t.Fatalf("UsagePercent7d = %v, want 0", resp.UsagePercent7d)
	}
	if resp.Reset7dAt != resetAt {
		t.Fatalf("Reset7dAt = %q, want %q", resp.Reset7dAt, resetAt)
	}
}

func TestApplyGrokUsageSnapshotHeaderObserverDoesNotProjectWeeklyUsage(t *testing.T) {
	pct := 60.0
	resp := &accountResponse{}

	applyGrokUsageSnapshotToQuotaFields(resp, &grokquota.UsageSnapshot{
		Source:         grokquota.SourceHeaderObserver,
		State:          grokquota.StateObserved,
		APIUsedPercent: &pct,
		Headers: map[string]string{
			"x-ratelimit-limit-requests":     "100",
			"x-ratelimit-remaining-requests": "40",
		},
	})

	if resp.UsagePercent7d != nil {
		t.Fatalf("UsagePercent7d = %v, want nil for header observer", *resp.UsagePercent7d)
	}
	if resp.Reset7dAt != "" {
		t.Fatalf("Reset7dAt = %q, want empty", resp.Reset7dAt)
	}
}

func TestApplyGrokUsageSnapshotBillingWithoutPercentDoesNotProjectWeeklyUsage(t *testing.T) {
	resp := &accountResponse{}

	applyGrokUsageSnapshotToQuotaFields(resp, &grokquota.UsageSnapshot{
		Source: grokquota.SourceGrokBuildBilling,
		State:  grokquota.StateObserved,
		Window: &grokquota.TimeWindow{ResetAt: "2026-07-08T03:40:00Z"},
	})

	if resp.UsagePercent7d != nil {
		t.Fatalf("UsagePercent7d = %v, want nil without billing percent", *resp.UsagePercent7d)
	}
	if resp.Reset7dAt != "" || resp.Window7dKind != "" || resp.Window7dSeconds != nil {
		t.Fatalf("unexpected projected window: reset=%q kind=%q seconds=%v", resp.Reset7dAt, resp.Window7dKind, resp.Window7dSeconds)
	}
}

// billing capture: /grok_api_v2.GrokBuildBilling/GetGrokCreditsConfig 200 body
const billingCaptureBase64 = "AAAAAFIKUA0AAHRCEgAaACILCLi3kNIGENiUljQqCwi4rLXSBhDYlJY0OgcIARUAAHRCQhwIAhILCLi3kNIGENiUljQaCwi4rLXSBhDYlJY0WAFiAGgBgAAAAA9ncnBjLXN0YXR1czowDQo="

// TestProbeGrokQuotaUsageSpendingLimitDoesNotMarkError 验证 header/responses 返回
// pending-limit 403 时标 rate_limited，不把账号打成 error。
func TestProbeGrokQuotaUsageSpendingLimitDoesNotMarkError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const spendingBody = `{"code":"personal-team-blocked:pending-limit","error":"You have run out of credits or need a Grok subscription."}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/subscriptions":
			w.WriteHeader(http.StatusNotFound)
		case "/grok_api_v2.GrokBuildBilling/GetGrokCreditsConfig":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`billing down`))
		case "/v1/responses":
			w.Header().Set("x-request-id", "resp-spending")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(spendingBody))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	db := newTestAdminDB(t)
	store := auth.NewStore(db, cache.NewMemory(1), nil)
	id, err := db.InsertAccountWithCredentials(context.Background(), "probe-spending", map[string]interface{}{
		"access_token": "tok",
		"base_url":     server.URL,
	}, "")
	if err != nil {
		t.Fatalf("InsertAccountWithCredentials: %v", err)
	}
	if err := db.SetAccountPlatform(context.Background(), id, "grok", auth.UpstreamOpenAIResponses); err != nil {
		t.Fatalf("SetAccountPlatform: %v", err)
	}
	if err := store.LoadAccountByID(context.Background(), id); err != nil {
		t.Fatalf("LoadAccountByID: %v", err)
	}
	acc := store.FindByID(id)
	if acc == nil {
		t.Fatalf("runtime account %d not found", id)
	}
	// 模拟此前已是 rate_limited，探针不得把它打回 error
	store.MarkCooldownWithError(acc, 7*24*time.Hour, "rate_limited", "seed rate_limited")

	handler := &Handler{db: db, store: store}
	_ = handler.probeGrokQuotaUsage(context.Background(), acc)

	if got := acc.RuntimeStatus(); got == "error" {
		t.Fatalf("RuntimeStatus() = %q, want non-error (spending-limit must not MarkError); ErrorMsg=%q", got, acc.ErrorMsg)
	}
	if got := acc.RuntimeStatus(); got != "rate_limited" {
		t.Fatalf("RuntimeStatus() = %q, want rate_limited; ErrorMsg=%q", got, acc.ErrorMsg)
	}
	if acc.ErrorMsg == "" || !containsAny(acc.ErrorMsg, []string{"pending-limit", "额度用尽", "run out of credits"}) {
		t.Fatalf("ErrorMsg = %q, want spending-limit detail", acc.ErrorMsg)
	}
}

func TestProbeGrokQuotaUsageBillingOKResponses403KeepsError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	capture, err := base64.StdEncoding.DecodeString(billingCaptureBase64)
	if err != nil {
		t.Fatalf("decode billing capture: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/grok_api_v2.GrokBuildBilling/GetGrokCreditsConfig" {
			w.Header().Set("xai-request-id", "bill-1")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(capture)
			return
		}
		if r.URL.Path == "/v1/responses" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"permission-denied","error":"Access denied"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	db := newTestAdminDB(t)
	store := auth.NewStore(db, cache.NewMemory(1), nil)

	id, err := db.InsertAccountWithCredentials(context.Background(), "probe-grok", map[string]interface{}{
		"access_token": "tok",
		"base_url":     server.URL,
	}, "")
	if err != nil {
		t.Fatalf("InsertAccountWithCredentials: %v", err)
	}
	if err := db.SetAccountPlatform(context.Background(), id, "grok", auth.UpstreamOpenAIResponses); err != nil {
		t.Fatalf("SetAccountPlatform: %v", err)
	}
	if err := store.LoadAccountByID(context.Background(), id); err != nil {
		t.Fatalf("LoadAccountByID: %v", err)
	}

	acc := store.FindByID(id)
	if acc == nil {
		t.Fatalf("runtime account %d not found", id)
	}
	acc.Mu().RLock()
	platform := acc.Platform
	base := acc.BaseURL
	acc.Mu().RUnlock()
	if platform != "grok" {
		t.Fatalf("platform = %q, want grok", platform)
	}
	if base == "" {
		t.Fatalf("baseURL empty")
	}

	store.MarkError(acc, "seed")

	handler := &Handler{db: db, store: store}

	if err := handler.probeGrokQuotaUsage(context.Background(), acc); err != nil {
		// 403 是预期错误，返回值非 nil 也允许，但状态必须保留为 error
		_ = err
	}

	if got := acc.RuntimeStatus(); got != "error" {
		t.Fatalf("RuntimeStatus() = %q, want error (billing OK must not ClearError)", got)
	}
	if acc.ErrorMsg == "" || !containsAny(acc.ErrorMsg, []string{"permission", "无权限"}) {
		t.Fatalf("ErrorMsg = %q, want to contain permission or 无权限", acc.ErrorMsg)
	}
}

func TestProbeGrokQuotaUsageBillingOKResponsesOKClearsError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	capture, err := base64.StdEncoding.DecodeString(billingCaptureBase64)
	if err != nil {
		t.Fatalf("decode billing capture: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/grok_api_v2.GrokBuildBilling/GetGrokCreditsConfig" {
			w.Header().Set("xai-request-id", "bill-2")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(capture)
			return
		}
		if r.URL.Path == "/v1/responses" {
			w.Header().Set("x-request-id", "resp-2")
			w.Header().Set("x-ratelimit-limit-requests", "100")
			w.Header().Set("x-ratelimit-remaining-requests", "50")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"ok"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	db := newTestAdminDB(t)
	store := auth.NewStore(db, cache.NewMemory(1), nil)

	id, err := db.InsertAccountWithCredentials(context.Background(), "probe-grok-ok", map[string]interface{}{
		"access_token": "tok",
		"base_url":     server.URL,
	}, "")
	if err != nil {
		t.Fatalf("InsertAccountWithCredentials: %v", err)
	}
	if err := db.SetAccountPlatform(context.Background(), id, "grok", auth.UpstreamOpenAIResponses); err != nil {
		t.Fatalf("SetAccountPlatform: %v", err)
	}
	if err := store.LoadAccountByID(context.Background(), id); err != nil {
		t.Fatalf("LoadAccountByID: %v", err)
	}

	acc := store.FindByID(id)
	if acc == nil {
		t.Fatalf("runtime account %d not found", id)
	}

	store.MarkError(acc, "seed")

	handler := &Handler{db: db, store: store}

	if err := handler.probeGrokQuotaUsage(context.Background(), acc); err != nil {
		t.Fatalf("probeGrokQuotaUsage: %v", err)
	}

	if got := acc.RuntimeStatus(); got == "error" {
		t.Fatalf("RuntimeStatus() = %q, want non-error (responses Observed should clear)", got)
	}
}

// TestProbeGrokQuotaUsage402WithSubscriptionTierPersistsCredentials 验证 402 错误响应
// 只要带 xai-subscription-tier，仍应落库 subscription_tier / plan_type。
// （无 /rest/subscriptions 成功时，header 仍作降级套餐源。）
func TestProbeGrokQuotaUsage402WithSubscriptionTierPersistsCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/subscriptions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path == "/grok_api_v2.GrokBuildBilling/GetGrokCreditsConfig" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`billing down`))
			return
		}
		if r.URL.Path == "/v1/responses" {
			w.Header().Set("xai-subscription-tier", "supergrok")
			w.Header().Set("xai-entitlement-status", "active")
			w.Header().Set("x-request-id", "resp-402")
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":"payment required"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	db := newTestAdminDB(t)
	store := auth.NewStore(db, cache.NewMemory(1), nil)

	id, err := db.InsertAccountWithCredentials(context.Background(), "probe-grok-402-tier", map[string]interface{}{
		"access_token": "tok",
		"base_url":     server.URL,
	}, "")
	if err != nil {
		t.Fatalf("InsertAccountWithCredentials: %v", err)
	}
	if err := db.SetAccountPlatform(context.Background(), id, "grok", auth.UpstreamOpenAIResponses); err != nil {
		t.Fatalf("SetAccountPlatform: %v", err)
	}
	if err := store.LoadAccountByID(context.Background(), id); err != nil {
		t.Fatalf("LoadAccountByID: %v", err)
	}

	acc := store.FindByID(id)
	if acc == nil {
		t.Fatalf("runtime account %d not found", id)
	}

	handler := &Handler{db: db, store: store}
	// 402 预期返回 error，但套餐字段必须已写入
	_ = handler.probeGrokQuotaUsage(context.Background(), acc)

	row, err := db.GetAccountByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetAccountByID: %v", err)
	}
	if got := row.GetCredential("subscription_tier"); got != "supergrok" {
		t.Fatalf("subscription_tier = %q, want supergrok", got)
	}
	if got := row.GetCredential("entitlement_status"); got != "active" {
		t.Fatalf("entitlement_status = %q, want active", got)
	}
	if got := row.GetCredential("plan_type"); got != "super" {
		t.Fatalf("plan_type = %q, want super", got)
	}
	acc.Mu().RLock()
	plan := acc.PlanType
	acc.Mu().RUnlock()
	if plan != "super" {
		t.Fatalf("memory PlanType = %q, want super", plan)
	}
}

// TestProbeGrokQuotaUsageSubscriptionsAuthoritativeOverHeader402 验证：
// /rest/subscriptions 成功写入 plan 后，后续 /responses 402 不得清掉/覆盖 plan。
func TestProbeGrokQuotaUsageSubscriptionsAuthoritativeOverHeader402(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 真实 REST 形状（非 SSR 顶层 flag）
	const subBody = `{
  "subscriptions": [{
    "tier": "SUBSCRIPTION_TIER_GROK_PRO",
    "status": "SUBSCRIPTION_STATUS_ACTIVE",
    "billingPeriodEnd": "2026-07-19T15:42:24.000Z"
  }]
}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/subscriptions":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(subBody))
		case "/grok_api_v2.GrokBuildBilling/GetGrokCreditsConfig":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`billing down`))
		case "/v1/responses":
			// 故意给不同 header tier，且 402：不得覆盖 subscriptions 权威源
			w.Header().Set("xai-subscription-tier", "free")
			w.Header().Set("x-request-id", "resp-402")
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":"payment required"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	db := newTestAdminDB(t)
	store := auth.NewStore(db, cache.NewMemory(1), nil)
	id, err := db.InsertAccountWithCredentials(context.Background(), "probe-sub-auth", map[string]interface{}{
		"access_token": "tok",
		"base_url":     server.URL,
	}, "")
	if err != nil {
		t.Fatalf("InsertAccountWithCredentials: %v", err)
	}
	if err := db.SetAccountPlatform(context.Background(), id, "grok", auth.UpstreamOpenAIResponses); err != nil {
		t.Fatalf("SetAccountPlatform: %v", err)
	}
	if err := store.LoadAccountByID(context.Background(), id); err != nil {
		t.Fatalf("LoadAccountByID: %v", err)
	}
	acc := store.FindByID(id)
	handler := &Handler{db: db, store: store}
	_ = handler.probeGrokQuotaUsage(context.Background(), acc)

	row, err := db.GetAccountByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetAccountByID: %v", err)
	}
	if got := row.GetCredential("plan_type"); got != "super" {
		t.Fatalf("plan_type = %q, want super from subscriptions", got)
	}
	if got := row.GetCredential("subscription_tier"); got != "SUBSCRIPTION_TIER_GROK_PRO" {
		t.Fatalf("subscription_tier = %q, want SUBSCRIPTION_TIER_GROK_PRO", got)
	}
	if got := row.GetCredential("subscription_expires_at"); got != "2026-07-19T15:42:24Z" {
		t.Fatalf("subscription_expires_at = %q", got)
	}
	acc.Mu().RLock()
	plan := acc.PlanType
	acc.Mu().RUnlock()
	if plan != "super" {
		t.Fatalf("memory PlanType = %q, want super", plan)
	}
}

// TestProbeGrokQuotaUsageSubscriptions401DoesNotClearExistingPlan 验证订阅 401 不覆盖已有 plan。
func TestProbeGrokQuotaUsageSubscriptions401DoesNotClearExistingPlan(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/subscriptions":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`unauthorized`))
		case "/grok_api_v2.GrokBuildBilling/GetGrokCreditsConfig":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/v1/responses":
			// 无 tier header，确保不会降级写入
			w.Header().Set("x-request-id", "r1")
			w.Header().Set("x-ratelimit-limit-requests", "1")
			w.Header().Set("x-ratelimit-remaining-requests", "1")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	db := newTestAdminDB(t)
	store := auth.NewStore(db, cache.NewMemory(1), nil)
	id, err := db.InsertAccountWithCredentials(context.Background(), "probe-sub-401", map[string]interface{}{
		"access_token":      "tok",
		"base_url":          server.URL,
		"plan_type":         "super",
		"subscription_tier": "SUBSCRIPTION_TIER_GROK_PRO",
	}, "")
	if err != nil {
		t.Fatalf("InsertAccountWithCredentials: %v", err)
	}
	if err := db.SetAccountPlatform(context.Background(), id, "grok", auth.UpstreamOpenAIResponses); err != nil {
		t.Fatalf("SetAccountPlatform: %v", err)
	}
	if err := store.LoadAccountByID(context.Background(), id); err != nil {
		t.Fatalf("LoadAccountByID: %v", err)
	}
	acc := store.FindByID(id)
	// 同步内存 plan（Load 应已带上；保险再设）
	store.UpdateAccountPlanType(acc, "super")

	handler := &Handler{db: db, store: store}
	_ = handler.probeGrokQuotaUsage(context.Background(), acc)

	row, err := db.GetAccountByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetAccountByID: %v", err)
	}
	if got := row.GetCredential("plan_type"); got != "super" {
		t.Fatalf("plan_type = %q, want super preserved after subscriptions 401", got)
	}
	if got := row.GetCredential("subscription_tier"); got != "SUBSCRIPTION_TIER_GROK_PRO" {
		t.Fatalf("subscription_tier = %q, want preserved", got)
	}
}

// TestProbeGrokQuotaUsageSubscriptionsEmptyObjectDoesNotWriteFree 验证 {} 不写 free。
func TestProbeGrokQuotaUsageSubscriptionsEmptyObjectDoesNotWriteFree(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/subscriptions":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case "/grok_api_v2.GrokBuildBilling/GetGrokCreditsConfig":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/v1/responses":
			w.Header().Set("x-request-id", "r1")
			w.Header().Set("x-ratelimit-limit-requests", "1")
			w.Header().Set("x-ratelimit-remaining-requests", "1")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	db := newTestAdminDB(t)
	store := auth.NewStore(db, cache.NewMemory(1), nil)
	id, err := db.InsertAccountWithCredentials(context.Background(), "probe-sub-empty", map[string]interface{}{
		"access_token":      "tok",
		"base_url":          server.URL,
		"plan_type":         "super",
		"subscription_tier": "SUBSCRIPTION_TIER_GROK_PRO",
	}, "")
	if err != nil {
		t.Fatalf("InsertAccountWithCredentials: %v", err)
	}
	if err := db.SetAccountPlatform(context.Background(), id, "grok", auth.UpstreamOpenAIResponses); err != nil {
		t.Fatalf("SetAccountPlatform: %v", err)
	}
	if err := store.LoadAccountByID(context.Background(), id); err != nil {
		t.Fatalf("LoadAccountByID: %v", err)
	}
	acc := store.FindByID(id)
	store.UpdateAccountPlanType(acc, "super")

	handler := &Handler{db: db, store: store}
	_ = handler.probeGrokQuotaUsage(context.Background(), acc)

	row, err := db.GetAccountByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetAccountByID: %v", err)
	}
	if got := row.GetCredential("plan_type"); got != "super" {
		t.Fatalf("plan_type = %q, want super preserved ({} must not write free)", got)
	}
}

// TestProbeGrokQuotaUsageSubscriptionsRESTEmptyWritesFree 验证 REST 空列表写 free。
func TestProbeGrokQuotaUsageSubscriptionsRESTEmptyWritesFree(t *testing.T) {
	gin.SetMode(gin.TestMode)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/subscriptions":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"subscriptions":[]}`))
		case "/grok_api_v2.GrokBuildBilling/GetGrokCreditsConfig":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/v1/responses":
			w.Header().Set("x-request-id", "r1")
			w.Header().Set("x-ratelimit-limit-requests", "1")
			w.Header().Set("x-ratelimit-remaining-requests", "1")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	db := newTestAdminDB(t)
	store := auth.NewStore(db, cache.NewMemory(1), nil)
	id, err := db.InsertAccountWithCredentials(context.Background(), "probe-sub-free", map[string]interface{}{
		"access_token": "tok",
		"base_url":     server.URL,
	}, "")
	if err != nil {
		t.Fatalf("InsertAccountWithCredentials: %v", err)
	}
	if err := db.SetAccountPlatform(context.Background(), id, "grok", auth.UpstreamOpenAIResponses); err != nil {
		t.Fatalf("SetAccountPlatform: %v", err)
	}
	if err := store.LoadAccountByID(context.Background(), id); err != nil {
		t.Fatalf("LoadAccountByID: %v", err)
	}
	acc := store.FindByID(id)
	handler := &Handler{db: db, store: store}
	_ = handler.probeGrokQuotaUsage(context.Background(), acc)

	row, err := db.GetAccountByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetAccountByID: %v", err)
	}
	if got := row.GetCredential("plan_type"); got != "free" {
		t.Fatalf("plan_type = %q, want free from empty subscriptions", got)
	}
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
