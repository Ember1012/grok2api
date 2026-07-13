package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
)

func TestQueryWhamUsage_ParsesPlusAccountResponse(t *testing.T) {
	body := `{
		"user_id": "user-abc",
		"account_id": "user-abc",
		"email": "rundown_consist_3o@icloud.com",
		"plan_type": "plus",
		"subscription_expires_at": "2026-07-17T09:30:23Z",
		"rate_limit": {
			"allowed": true,
			"limit_reached": false,
			"primary_window": {
				"used_percent": 83,
				"limit_window_seconds": 18000,
				"reset_after_seconds": 10778,
				"reset_at": 1779708117
			},
			"secondary_window": {
				"used_percent": 30,
				"limit_window_seconds": 604800,
				"reset_after_seconds": 474764,
				"reset_at": 1780172103
			}
		},
		"credits": {
			"has_credits": false,
			"unlimited": false,
			"overage_limit_reached": false,
			"balance": "0",
			"approx_local_messages": [0, 0],
			"approx_cloud_messages": [0, 0]
		},
		"spend_control": {"reached": false, "individual_limit": null}
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("Authorization header missing or malformed: %q", got)
		}
		if r.Header.Get("chatgpt-account-id") != "acc-1" {
			t.Errorf("chatgpt-account-id = %q, want acc-1", r.Header.Get("chatgpt-account-id"))
		}
		if r.Header.Get("Originator") != Originator {
			t.Errorf("Originator = %q, want %q", r.Header.Get("Originator"), Originator)
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "codex-tui/") {
			t.Errorf("User-Agent = %q, want codex-tui prefix", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	// 重写 URL 到测试服务器：通过临时变量替换
	oldURL := whamURLForTest
	whamURLForTest = server.URL
	defer func() { whamURLForTest = oldURL }()

	account := &auth.Account{DBID: 1, AccessToken: "at-123", AccountID: "acc-1"}
	usage, _, err := queryWhamUsageWithURL(context.Background(), account, "", whamURLForTest)
	if err != nil {
		t.Fatalf("QueryWhamUsage error: %v", err)
	}
	if usage.PlanType != "plus" {
		t.Errorf("PlanType = %q, want plus", usage.PlanType)
	}
	wantSubscriptionExpiresAt := time.Date(2026, 7, 17, 9, 30, 23, 0, time.UTC)
	if got := usage.SubscriptionExpiresAt(); !got.Equal(wantSubscriptionExpiresAt) {
		t.Errorf("SubscriptionExpiresAt() = %s, want %s", got.Format(time.RFC3339), wantSubscriptionExpiresAt.Format(time.RFC3339))
	}
	if usage.RateLimit.PrimaryWindow == nil || usage.RateLimit.PrimaryWindow.UsedPercent != 83 {
		t.Errorf("primary used_percent = %+v, want 83", usage.RateLimit.PrimaryWindow)
	}
	if usage.RateLimit.SecondaryWindow == nil || usage.RateLimit.SecondaryWindow.UsedPercent != 30 {
		t.Errorf("secondary used_percent = %+v, want 30", usage.RateLimit.SecondaryWindow)
	}
}

func TestQueryWhamUsage_UsesCustomHeaderAccountIDOverride(t *testing.T) {
	var gotAccountID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccountID = r.Header.Get("chatgpt-account-id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"plan_type": "plus", "rate_limit": {}}`))
	}))
	defer server.Close()

	account := &auth.Account{
		DBID:          1,
		AccessToken:   "at-123",
		AccountID:     "acc-1",
		CustomHeaders: map[string]string{"Chatgpt-Account-Id": "acc-override"},
	}
	if _, _, err := queryWhamUsageWithURL(context.Background(), account, "", server.URL); err != nil {
		t.Fatalf("queryWhamUsageWithURL error: %v", err)
	}
	if gotAccountID != "acc-override" {
		t.Errorf("chatgpt-account-id = %q, want acc-override", gotAccountID)
	}
}

func TestApplyWhamUsage_SkipsIdentityWriteBackWhenAccountIDOverridden(t *testing.T) {
	account := &auth.Account{
		DBID:          1,
		AccessToken:   "at",
		AccountID:     "acc-real",
		CustomHeaders: map[string]string{"Chatgpt-Account-Id": "acc-override"},
	}
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 1, TestConcurrency: 1, TestModel: "gpt-5.4"})

	usage := &WhamUsage{PlanType: "plus", AccountID: "acc-override", Email: "a@b.c"}
	ApplyWhamUsage(store, account, usage)

	if got := account.AccountID; got != "acc-real" {
		t.Errorf("AccountID = %q, want acc-real (identity must not be overwritten by overridden workspace)", got)
	}
}

func TestApplyWhamUsage_PersistsPlanAnd5h7d(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := database.New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	defer db.Close()

	id, err := db.InsertAccountWithCredentials(ctx, "wham-test", map[string]interface{}{"plan_type": "free"}, "")
	if err != nil {
		t.Fatalf("InsertAccountWithCredentials: %v", err)
	}

	store := auth.NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	account := &auth.Account{DBID: id, AccessToken: "at", PlanType: "free", AccountID: "acc"}

	now := time.Now()
	reset5h := now.Add(3 * time.Hour).Unix()
	reset7d := now.Add(5 * 24 * time.Hour).Unix()
	usage := &WhamUsage{PlanType: "plus"}
	usage.SubscriptionExpiresAtRaw = whamTimeRaw("2026-07-17T09:30:23Z")
	usage.RateLimit.PrimaryWindow = &WhamUsageWindow{UsedPercent: 83, LimitWindowSeconds: 18000, ResetAt: reset5h}
	usage.RateLimit.SecondaryWindow = &WhamUsageWindow{UsedPercent: 30, LimitWindowSeconds: 604800, ResetAt: reset7d}

	result := ApplyWhamUsage(store, account, usage)

	if got := account.GetPlanType(); got != "plus" {
		t.Errorf("plan_type = %q, want plus (synced from wham)", got)
	}
	if !result.HasUsage5h || result.UsagePct5h != 83 {
		t.Errorf("5h result = %+v, want HasUsage5h && UsagePct5h=83", result)
	}
	if !result.HasUsage7d || result.UsagePct7d != 30 {
		t.Errorf("7d result = %+v, want HasUsage7d && UsagePct7d=30", result)
	}
	if result.Premium5hRateLimited {
		t.Error("expected NOT premium 5h rate limited (used_percent=83 < 100)")
	}

	row, err := db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatalf("GetAccountByID: %v", err)
	}
	if got := row.GetCredential("plan_type"); got != "plus" {
		t.Errorf("persisted plan_type = %q, want plus", got)
	}
	wantSubscriptionExpiresAt := time.Date(2026, 7, 17, 9, 30, 23, 0, time.UTC)
	if !account.SubscriptionExpiresAt.Equal(wantSubscriptionExpiresAt) {
		t.Errorf("account SubscriptionExpiresAt = %s, want %s", account.SubscriptionExpiresAt.Format(time.RFC3339), wantSubscriptionExpiresAt.Format(time.RFC3339))
	}
	if got := row.GetCredential("subscription_expires_at"); got != wantSubscriptionExpiresAt.Format(time.RFC3339) {
		t.Errorf("persisted subscription_expires_at = %q, want %q", got, wantSubscriptionExpiresAt.Format(time.RFC3339))
	}
}

func TestApplyWhamUsage_PersistsIdentity(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := database.New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	defer db.Close()

	id, err := db.InsertAccountWithCredentials(ctx, "at-only", map[string]interface{}{"access_token": "at"}, "")
	if err != nil {
		t.Fatalf("InsertAccountWithCredentials: %v", err)
	}

	store := auth.NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	account := &auth.Account{DBID: id, AccessToken: "at"}
	usage := &WhamUsage{
		UserID:    "user-from-wham",
		AccountID: "account-from-wham",
		Email:     "wham@example.com",
		PlanType:  "team",
	}

	ApplyWhamUsage(store, account, usage)

	if account.Email != "wham@example.com" {
		t.Fatalf("account.Email = %q, want wham@example.com", account.Email)
	}
	if account.AccountID != "account-from-wham" {
		t.Fatalf("account.AccountID = %q, want account-from-wham", account.AccountID)
	}

	row, err := db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatalf("GetAccountByID: %v", err)
	}
	if got := row.GetCredential("email"); got != "wham@example.com" {
		t.Fatalf("credentials.email = %q, want wham@example.com", got)
	}
	if got := row.GetCredential("account_id"); got != "account-from-wham" {
		t.Fatalf("credentials.account_id = %q, want account-from-wham", got)
	}
}

func TestApplyWhamUsage_PersistsSubscriptionExpiresAtWhenMemoryAlreadyMatches(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := database.New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	defer db.Close()

	id, err := db.InsertAccountWithCredentials(ctx, "wham-subscription-retry", map[string]interface{}{"plan_type": "plus"}, "")
	if err != nil {
		t.Fatalf("InsertAccountWithCredentials: %v", err)
	}

	store := auth.NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	expiresAt := time.Date(2026, 7, 17, 9, 30, 23, 0, time.UTC)
	account := &auth.Account{DBID: id, AccessToken: "at", PlanType: "plus", AccountID: "acc", SubscriptionExpiresAt: expiresAt}
	usage := &WhamUsage{PlanType: "plus"}
	usage.SubscriptionExpiresAtRaw = whamTimeRaw(expiresAt.Format(time.RFC3339))

	ApplyWhamUsage(store, account, usage)

	row, err := db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatalf("GetAccountByID: %v", err)
	}
	if got := row.GetCredential("subscription_expires_at"); got != expiresAt.Format(time.RFC3339) {
		t.Fatalf("persisted subscription_expires_at = %q, want %q", got, expiresAt.Format(time.RFC3339))
	}
}

func TestWhamUsageSubscriptionExpiresAtFallbacks(t *testing.T) {
	cases := []struct {
		name string
		body string
		want time.Time
	}{
		{
			name: "subscription_active_until",
			body: `{"subscription_active_until":"2026-07-17T09:30:23.123Z"}`,
			want: time.Date(2026, 7, 17, 9, 30, 23, 123000000, time.UTC),
		},
		{
			name: "chatgpt_subscription_active_until",
			body: `{"chatgpt_subscription_active_until":"2026-07-17T09:30:23Z"}`,
			want: time.Date(2026, 7, 17, 9, 30, 23, 0, time.UTC),
		},
		{
			name: "unix_seconds",
			body: `{"subscription_expires_at":1784280623}`,
			want: time.Unix(1784280623, 0),
		},
		{
			name: "unix_milliseconds",
			body: `{"subscription_expires_at":1784280623000}`,
			want: time.UnixMilli(1784280623000),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var usage WhamUsage
			if err := json.Unmarshal([]byte(tc.body), &usage); err != nil {
				t.Fatalf("json.Unmarshal: %v", err)
			}
			if got := usage.SubscriptionExpiresAt(); !got.Equal(tc.want) {
				t.Fatalf("SubscriptionExpiresAt() = %s, want %s", got.Format(time.RFC3339Nano), tc.want.Format(time.RFC3339Nano))
			}
		})
	}
}

func TestApplyWhamUsage5hOnlyDoesNotRefreshStale7dProbeFreshness(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	account := &auth.Account{
		DBID:                2,
		AccessToken:         "at",
		PlanType:            "plus",
		Status:              auth.StatusReady,
		UsagePercent7d:      40,
		UsagePercent7dValid: true,
		UsageUpdatedAt:      time.Now().Add(-20 * time.Minute),
	}

	now := time.Now()
	usage := &WhamUsage{PlanType: "plus"}
	usage.RateLimit.PrimaryWindow = &WhamUsageWindow{UsedPercent: 83, LimitWindowSeconds: 18000, ResetAt: now.Add(2 * time.Hour).Unix()}

	result := ApplyWhamUsage(store, account, usage)

	if !result.Used5hHeaders || !result.HasUsage5h {
		t.Fatalf("ApplyWhamUsage result = %+v, want 5h-only usage snapshot", result)
	}
	if result.HasUsage7d {
		t.Fatalf("ApplyWhamUsage result = %+v, want no 7d snapshot from 5h-only usage", result)
	}
	if !result.Persisted5hOnly {
		t.Fatalf("ApplyWhamUsage result = %+v, want 5h-only persistence path", result)
	}
	if !account.NeedsUsageProbe(10 * time.Minute) {
		t.Fatal("NeedsUsageProbe() = false, want true because 5h-only WHAM sync must not refresh stale 7d freshness")
	}
}

// 复现 issue #168：free 账号的 wham 响应里 primary_window 实际承载的是 7d 数据
// （limit_window_seconds=604800），secondary_window=null。代码必须按
// limit_window_seconds 而不是字段位置来分类，否则 7d 数据会被错误写入 5h 槽位。
func TestApplyWhamUsage_FreeAccountPrimaryIs7d(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := database.New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	defer db.Close()

	id, err := db.InsertAccountWithCredentials(ctx, "wham-free", map[string]interface{}{"plan_type": "free"}, "")
	if err != nil {
		t.Fatalf("InsertAccountWithCredentials: %v", err)
	}

	store := auth.NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	account := &auth.Account{DBID: id, AccessToken: "at", PlanType: "free", AccountID: "acc"}

	reset7d := time.Now().Add(7 * 24 * time.Hour).Unix()
	usage := &WhamUsage{PlanType: "free"}
	usage.RateLimit.PrimaryWindow = &WhamUsageWindow{UsedPercent: 3, LimitWindowSeconds: 604800, ResetAfterSeconds: 604800, ResetAt: reset7d}
	usage.RateLimit.SecondaryWindow = nil

	result := ApplyWhamUsage(store, account, usage)

	if result.HasUsage5h {
		t.Errorf("expected HasUsage5h=false for free account (only 7d window), got result=%+v", result)
	}
	if result.Used5hHeaders {
		t.Errorf("expected Used5hHeaders=false (no 5h window in response), got result=%+v", result)
	}
	if !result.HasUsage7d || result.UsagePct7d != 3 {
		t.Errorf("7d result = %+v, want HasUsage7d && UsagePct7d=3", result)
	}
	if result.Persisted5hOnly {
		t.Error("expected Persisted5hOnly=false; should persist via 7d snapshot path")
	}

	if pct, ok := account.GetUsagePercent7d(); !ok || pct != 3 {
		t.Errorf("account 7d in-memory snapshot = (%v, %v), want (3, true)", pct, ok)
	}
	if _, ok := account.GetUsagePercent5h(); ok {
		t.Error("account 5h snapshot should remain unset for free account")
	}

	row, err := db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatalf("GetAccountByID: %v", err)
	}
	if got := row.GetCredential("codex_7d_used_percent"); got != "3" {
		t.Errorf("persisted codex_7d_used_percent = %q, want %q", got, "3")
	}
}

// 防御性测试：即使后端把 5h/7d 字段顺序对调，分类也必须按 limit_window_seconds 走。
func TestApplyWhamUsage_ClassifiesByWindowSeconds(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	account := &auth.Account{DBID: 1, AccessToken: "at", PlanType: "plus"}

	now := time.Now()
	usage := &WhamUsage{PlanType: "plus"}
	// 故意颠倒：把 7d (604800) 放 primary、5h (18000) 放 secondary
	usage.RateLimit.PrimaryWindow = &WhamUsageWindow{UsedPercent: 30, LimitWindowSeconds: 604800, ResetAt: now.Add(5 * 24 * time.Hour).Unix()}
	usage.RateLimit.SecondaryWindow = &WhamUsageWindow{UsedPercent: 83, LimitWindowSeconds: 18000, ResetAt: now.Add(3 * time.Hour).Unix()}

	result := ApplyWhamUsage(store, account, usage)

	if !result.HasUsage5h || result.UsagePct5h != 83 {
		t.Errorf("5h result = %+v, want UsagePct5h=83 (classified by 18000s window)", result)
	}
	if !result.HasUsage7d || result.UsagePct7d != 30 {
		t.Errorf("7d result = %+v, want UsagePct7d=30 (classified by 604800s window)", result)
	}
}

// 当 limit_window_seconds 缺失或为未知值时，按字段位置兜底分类
// （与 CPA-Manager pickClassifiedWindows 的 allowOrderFallback 行为一致）。
func TestPickClassifiedWhamWindows_FallsBackToPositionForUnknownSeconds(t *testing.T) {
	primary := &WhamUsageWindow{UsedPercent: 50, LimitWindowSeconds: 0} // 未知/缺失
	secondary := &WhamUsageWindow{UsedPercent: 20, LimitWindowSeconds: 0}

	w5h, w7d := pickClassifiedWhamWindows(primary, secondary, "plus", time.Now())
	if w5h != primary {
		t.Errorf("expected primary→5h via position fallback, got %v", w5h)
	}
	if w7d != secondary {
		t.Errorf("expected secondary→7d via position fallback, got %v", w7d)
	}
}

func TestPickClassifiedWhamWindows_FreeUnknownPrimaryFallsBackTo7d(t *testing.T) {
	primary := &WhamUsageWindow{UsedPercent: 100, LimitWindowSeconds: 0}

	w5h, w7d := pickClassifiedWhamWindows(primary, nil, "free", time.Now())
	if w5h != nil {
		t.Fatalf("expected no 5h window for free unknown primary, got %v", w5h)
	}
	if w7d != primary {
		t.Fatalf("expected primary→7d for free unknown primary, got %v", w7d)
	}
}

func TestPickClassifiedWhamWindows_LongResetPrimaryFallsBackTo7d(t *testing.T) {
	primary := &WhamUsageWindow{
		UsedPercent:        100,
		LimitWindowSeconds: 0,
		ResetAfterSeconds:  6 * 60 * 60,
	}

	w5h, w7d := pickClassifiedWhamWindows(primary, nil, "", time.Now())
	if w5h != nil {
		t.Fatalf("expected no 5h window for long reset primary, got %v", w5h)
	}
	if w7d != primary {
		t.Fatalf("expected primary→7d for long reset primary, got %v", w7d)
	}
}

// TestPickClassifiedWhamWindows_TeamMonthlyWindowRoutesTo7dSlot 验证 team plan 的
// 月窗(约 30 天 = 2592000s)被归入长窗口(7d)槽，而非漏掉或误进 5h。
func TestPickClassifiedWhamWindows_TeamMonthlyWindowRoutesTo7dSlot(t *testing.T) {
	now := time.Now()
	primary := &WhamUsageWindow{UsedPercent: 40, LimitWindowSeconds: 18000, ResetAt: now.Add(2 * time.Hour).Unix()}
	monthly := &WhamUsageWindow{UsedPercent: 12, LimitWindowSeconds: 2_592_000, ResetAt: now.Add(20 * 24 * time.Hour).Unix()}

	w5h, w7d := pickClassifiedWhamWindows(primary, monthly, "team", now)
	if w5h != primary {
		t.Fatalf("expected primary→5h, got %v", w5h)
	}
	if w7d != monthly {
		t.Fatalf("expected monthly(2592000s)→7d slot, got %v", w7d)
	}

	// 28–31 天容差：29 天窗口也应识别为月窗。
	tolMonthly := &WhamUsageWindow{UsedPercent: 5, LimitWindowSeconds: 29 * 24 * 60 * 60, ResetAt: now.Add(29 * 24 * time.Hour).Unix()}
	if _, w7dTol := pickClassifiedWhamWindows(primary, tolMonthly, "team", now); w7dTol != tolMonthly {
		t.Fatalf("expected 29d window→7d slot via tolerance, got %v", w7dTol)
	}
}

// TestApplyWhamUsage_CapturesMonthlyWindowLength 验证 team 月窗的真实周期秒数被记入账号，
// 供智能配速按真实周期(而非固定 7 天)计算。
func TestApplyWhamUsage_CapturesMonthlyWindowLength(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	account := &auth.Account{DBID: 1, AccessToken: "at", PlanType: "team"}

	now := time.Now()
	usage := &WhamUsage{PlanType: "team"}
	usage.RateLimit.PrimaryWindow = &WhamUsageWindow{UsedPercent: 40, LimitWindowSeconds: 18000, ResetAt: now.Add(2 * time.Hour).Unix()}
	usage.RateLimit.SecondaryWindow = &WhamUsageWindow{UsedPercent: 12, LimitWindowSeconds: 2_592_000, ResetAt: now.Add(20 * 24 * time.Hour).Unix()}

	ApplyWhamUsage(store, account, usage)

	if got := account.GetWindow7dSeconds(); got != 2_592_000 {
		t.Fatalf("Window7dSeconds=%d, want 2592000", got)
	}
	if kind := account.Window7dKind(); kind != "monthly" {
		t.Fatalf("Window7dKind=%q, want monthly", kind)
	}
}

func TestApplyWhamUsage_MarksPremium5hLimitedAt100Percent(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	account := &auth.Account{DBID: 1, AccessToken: "at", PlanType: "plus"}

	now := time.Now()
	usage := &WhamUsage{PlanType: "plus"}
	usage.RateLimit.PrimaryWindow = &WhamUsageWindow{UsedPercent: 100, LimitWindowSeconds: 18000, ResetAt: now.Add(2 * time.Hour).Unix()}

	result := ApplyWhamUsage(store, account, usage)
	if !result.Premium5hRateLimited {
		t.Errorf("expected Premium5hRateLimited=true for plus plan at 100%%, result=%+v", result)
	}
	if !account.IsPremium5hRateLimited() {
		t.Error("account should be in premium 5h rate-limited state after ApplyWhamUsage")
	}
}

func TestApplyWhamUsage_CreditAccountSkipsPremium5hWindowLimit(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.5"})
	account := &auth.Account{
		DBID:                  1,
		AccessToken:           "at",
		PlanType:              "plus",
		Status:                auth.StatusReady,
		CreditEnabled:         true,
		CreditSkipUsageWindow: true,
	}

	now := time.Now()
	usage := &WhamUsage{PlanType: "plus"}
	usage.RateLimit.PrimaryWindow = &WhamUsageWindow{UsedPercent: 100, LimitWindowSeconds: 18000, ResetAt: now.Add(2 * time.Hour).Unix()}

	result := ApplyWhamUsage(store, account, usage)
	if result.Premium5hRateLimited {
		t.Fatalf("Premium5hRateLimited = true, want false for credit account")
	}
	if account.IsPremium5hRateLimited() {
		t.Fatal("credit account should not be in premium 5h rate-limited state after ApplyWhamUsage")
	}
	pct5h, _, ok := account.GetUsageSnapshot5h()
	if !ok || pct5h != 100 {
		t.Fatalf("5h snapshot = (%v, %v), want 100 with valid snapshot", pct5h, ok)
	}
}

func TestApplyWhamUsage_Marks7dLimitedAt100Percent(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.4"})
	account := &auth.Account{DBID: 1, AccessToken: "at", PlanType: "team", Status: auth.StatusReady, HealthTier: auth.HealthTierHealthy}

	now := time.Now()
	usage := &WhamUsage{PlanType: "team"}
	usage.RateLimit.PrimaryWindow = &WhamUsageWindow{UsedPercent: 20, LimitWindowSeconds: 18000, ResetAt: now.Add(2 * time.Hour).Unix()}
	usage.RateLimit.SecondaryWindow = &WhamUsageWindow{UsedPercent: 100, LimitWindowSeconds: 604800, ResetAt: now.Add(5 * 24 * time.Hour).Unix()}

	result := ApplyWhamUsage(store, account, usage)
	if !result.Usage7dRateLimited {
		t.Fatalf("Usage7dRateLimited = false, result=%+v", result)
	}
	if got := account.RuntimeStatus(); got != "rate_limited" {
		t.Fatalf("RuntimeStatus() = %q, want rate_limited", got)
	}
}

func TestApplyWhamUsage_CreditAccountSkips7dWindowLimit(t *testing.T) {
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, TestConcurrency: 1, TestModel: "gpt-5.5"})
	account := &auth.Account{
		DBID:                  1,
		AccessToken:           "at",
		PlanType:              "team",
		Status:                auth.StatusReady,
		HealthTier:            auth.HealthTierHealthy,
		CreditEnabled:         true,
		CreditSkipUsageWindow: true,
	}

	now := time.Now()
	usage := &WhamUsage{PlanType: "team"}
	usage.RateLimit.PrimaryWindow = &WhamUsageWindow{UsedPercent: 20, LimitWindowSeconds: 18000, ResetAt: now.Add(2 * time.Hour).Unix()}
	usage.RateLimit.SecondaryWindow = &WhamUsageWindow{UsedPercent: 100, LimitWindowSeconds: 604800, ResetAt: now.Add(5 * 24 * time.Hour).Unix()}

	result := ApplyWhamUsage(store, account, usage)
	if result.Usage7dRateLimited {
		t.Fatalf("Usage7dRateLimited = true, want false for credit account")
	}
	if got := account.RuntimeStatus(); got != "active" {
		t.Fatalf("RuntimeStatus() = %q, want active for credit account", got)
	}
	pct7d, ok := account.GetUsagePercent7d()
	if !ok || pct7d != 100 {
		t.Fatalf("7d snapshot = (%v, %v), want 100 with valid snapshot", pct7d, ok)
	}
}

func TestWhamUsageJSON_RoundTrip(t *testing.T) {
	in := WhamUsage{PlanType: "plus"}
	in.RateLimit.Allowed = true
	in.RateLimit.PrimaryWindow = &WhamUsageWindow{UsedPercent: 50}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out WhamUsage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.RateLimit.PrimaryWindow == nil || out.RateLimit.PrimaryWindow.UsedPercent != 50 {
		t.Errorf("roundtrip lost primary window")
	}
}

