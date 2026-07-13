package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/codex2api/auth"
)

// WhamUsageURL 是 ChatGPT 后端用量查询端点。
// 该端点返回结构化 JSON（不消耗任何额度），可用于零成本获取账号 5h/7d 用量。
const WhamUsageURL = "https://chatgpt.com/backend-api/wham/usage"

// whamURLForTest 允许测试替换默认 URL。生产代码不要赋值。
var whamURLForTest = ""

// WhamUsage 是 /backend-api/wham/usage 的响应结构。
type WhamUsage struct {
	UserID    string `json:"user_id"`
	AccountID string `json:"account_id"`
	Email     string `json:"email"`
	PlanType  string `json:"plan_type"`

	// OpenAI 续费后 JWT 里的 chatgpt_subscription_active_until 不一定会立即随授权刷新，
	// wham/usage 返回的服务端当前订阅到期时间用于同步管理端展示与调度权重。
	SubscriptionExpiresAtRaw          whamTimeRaw `json:"subscription_expires_at,omitempty"`
	SubscriptionActiveUntilRaw        whamTimeRaw `json:"subscription_active_until,omitempty"`
	ChatGPTSubscriptionActiveUntilRaw whamTimeRaw `json:"chatgpt_subscription_active_until,omitempty"`

	RateLimit struct {
		Allowed         bool             `json:"allowed"`
		LimitReached    bool             `json:"limit_reached"`
		PrimaryWindow   *WhamUsageWindow `json:"primary_window"`
		SecondaryWindow *WhamUsageWindow `json:"secondary_window"`
	} `json:"rate_limit"`

	Credits *struct {
		HasCredits          bool   `json:"has_credits"`
		Unlimited           bool   `json:"unlimited"`
		OverageLimitReached bool   `json:"overage_limit_reached"`
		Balance             string `json:"balance"`
		ApproxLocalMessages []int  `json:"approx_local_messages"`
		ApproxCloudMessages []int  `json:"approx_cloud_messages"`
	} `json:"credits,omitempty"`

	SpendControl *struct {
		Reached         bool        `json:"reached"`
		IndividualLimit interface{} `json:"individual_limit"`
	} `json:"spend_control,omitempty"`
}

type whamTimeRaw string

func (w *whamTimeRaw) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*w = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err == nil {
		*w = whamTimeRaw(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(trimmed, &n); err == nil {
		*w = whamTimeRaw(n.String())
		return nil
	}
	return nil
}

func (w *WhamUsage) SubscriptionExpiresAt() time.Time {
	if w == nil {
		return time.Time{}
	}
	for _, raw := range []whamTimeRaw{
		w.SubscriptionExpiresAtRaw,
		w.SubscriptionActiveUntilRaw,
		w.ChatGPTSubscriptionActiveUntilRaw,
	} {
		if t := parseWhamTime(string(raw)); !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

func parseWhamTime(raw string) time.Time {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if unix, err := strconv.ParseInt(s, 10, 64); err == nil && unix > 0 {
		// 兼容秒级与毫秒级 unix 时间戳。
		if unix > 1_000_000_000_000 {
			return time.UnixMilli(unix)
		}
		return time.Unix(unix, 0)
	}
	return time.Time{}
}

// WhamUsageWindow 是单个限流窗口（primary=5h，secondary=7d）。
type WhamUsageWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

// QueryWhamUsage 调用 /backend-api/wham/usage 获取账号当前用量。
// 该调用不消耗任何 token 额度——比发送最小 /responses 请求更便宜。
func QueryWhamUsage(ctx context.Context, account *auth.Account, proxyURL string) (*WhamUsage, *http.Response, error) {
	url := WhamUsageURL
	if whamURLForTest != "" {
		url = whamURLForTest
	}
	return queryWhamUsageWithURL(ctx, account, proxyURL, url)
}

func queryWhamUsageWithURL(ctx context.Context, account *auth.Account, proxyURL, url string) (*WhamUsage, *http.Response, error) {
	if account == nil {
		return nil, nil, fmt.Errorf("account is nil")
	}
	accessToken := account.GetAccessToken()
	if accessToken == "" {
		return nil, nil, fmt.Errorf("account has no access token")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("build wham request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", defaultCodexCLIUserAgent)
	req.Header.Set("Originator", Originator)
	// 用 EffectiveAccountID:自定义头覆盖了工作区 ID 时,额度必须查覆盖后的空间,
	// 否则进度条/自动暂停/智能配速统计的是与实际流量不同的空间。
	if accountID := account.EffectiveAccountID(); accountID != "" {
		req.Header.Set("chatgpt-account-id", accountID)
	}

	// 复用网关同款 transport（支持 uTLS Chrome 指纹），而非裸标准 transport，
	// 让 wham 查询与 /responses 走一致的 TLS 指纹，降低被 Cloudflare 拦截的概率。
	client := &http.Client{Transport: newCodexTransport(proxyURL)}

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("wham request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// 调用方需要根据状态码触发刷新 / 冷却；返回 resp 让上层处理 body。
		return nil, resp, fmt.Errorf("wham returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if err != nil {
		return nil, resp, fmt.Errorf("read wham response: %w", err)
	}

	var usage WhamUsage
	if err := json.Unmarshal(body, &usage); err != nil {
		return nil, resp, fmt.Errorf("parse wham response: %w", err)
	}
	return &usage, resp, nil
}

// ApplyWhamUsage 将 /wham/usage 返回的数据写入账号 state + 持久化。
// 行为与 SyncCodexUsageState（处理 /responses 响应头时）保持一致：
//   - plan_type 同步到内存 + DB
//   - 窗口按 limit_window_seconds 精确分类（18000s→5h，604800s→7d）；
//     未精确匹配时优先按 free plan / 长 reset 识别 7d，再按字段位置兜底。
//   - 5h 窗口写入 SetUsageSnapshot5h
//   - 7d 窗口走 PersistUsageSnapshot
//   - premium 5h 用尽时走 MarkPremium5hRateLimited
func ApplyWhamUsage(store *auth.Store, account *auth.Account, usage *WhamUsage) CodexUsageSyncResult {
	result := CodexUsageSyncResult{}
	if account == nil || usage == nil {
		return result
	}

	if store != nil && usage.PlanType != "" {
		store.UpdateAccountPlanType(account, usage.PlanType)
	}
	if store != nil {
		// 只回填真正的工作区 ID。此前 account_id 缺失时用 user_id 兜底写入
		// account_id 字段，会污染 OAuth 身份去重键（JWT 解出的工作区 ID 与
		// 库里存的 user-... 永远对不上），导致同一账号重复导入(串号池)。
		// 自定义头覆盖了工作区 ID 时,wham 返回的是覆盖后空间的 ID,
		// 不能回写进 OAuth 身份字段(会覆盖真实身份并与覆盖值反复打架)。
		identityAccountID := strings.TrimSpace(usage.AccountID)
		if account.AccountIDOverridden() {
			identityAccountID = ""
		}
		store.UpdateAccountIdentity(account, usage.Email, identityAccountID)
		store.UpdateAccountSubscriptionExpiresAt(account, usage.SubscriptionExpiresAt())
	}

	now := time.Now()

	w5h, w7d := pickClassifiedWhamWindows(usage.RateLimit.PrimaryWindow, usage.RateLimit.SecondaryWindow, usage.PlanType, now)

	if w5h != nil {
		resetAt := whamWindowResetAt(w5h, now)
		account.SetUsageSnapshot5hAt(w5h.UsedPercent, resetAt, now)
		result.UsagePct5h = w5h.UsedPercent
		result.Reset5hAt = resetAt
		result.HasUsage5h = true
		result.Used5hHeaders = true
		if store != nil {
			// 5h 窗口重置时刻武装「到点即探」，窗口翻新即刷新进度条。
			store.WakeBoundaryProbe(resetAt)
		}
	}

	if w7d != nil {
		resetAt := whamWindowResetAt(w7d, now)
		account.SetReset7dAt(resetAt)
		account.SetWindow7dSeconds(w7d.LimitWindowSeconds)
		result.UsagePct7d = w7d.UsedPercent
		result.HasUsage7d = true
		if store != nil {
			store.WakeBoundaryProbe(resetAt)
			store.PersistUsageSnapshot(account, w7d.UsedPercent)
			if result.UsagePct7d >= 100 {
				result.Usage7dRateLimited = store.MarkUsage7dRateLimited(account)
			}
		}
	} else if result.Used5hHeaders && store != nil {
		// 只有 5h 数据时，单独持久化 5h 快照
		store.PersistUsageSnapshot5hOnly(account)
		result.Persisted5hOnly = true
	}

	// premium 5h 限流标记
	if result.Used5hHeaders && account.IsPremium5hPlan() && result.HasUsage5h && result.UsagePct5h >= 100 && !account.SkipsUsageWindowLimits() {
		if store != nil {
			store.MarkPremium5hRateLimited(account, result.Reset5hAt)
		}
		result.Premium5hRateLimited = true
	}

	return result
}

// 已知窗口长度（秒）。和 CPA-Manager src/utils/quota/codexQuota.ts 保持一致。
const (
	whamWindow5hSeconds int64 = 18_000
	whamWindow7dSeconds int64 = 604_800
)

// pickClassifiedWhamWindows 把 primary/secondary 两个窗口归类到 5h/7d 槽位。
//
// 策略对齐 CPA-Manager 的 pickClassifiedWindows：
//  1. 第一遍：按 limit_window_seconds 精确匹配（18000→5h，604800→7d）
//  2. 第二遍：把 free plan 或 reset 明显超过 5h 的未知窗口归到 7d
//  3. 最后按字段位置兜底（primary→5h、secondary→7d），只填补空槽位
//
// 这样可同时正确处理：
//   - plus：primary=18000(5h) + secondary=604800(7d)
//   - free：primary=604800(7d) + secondary=null（issue #168 报告的场景）
//   - 字段位置颠倒
//   - 未来出现的未知窗口长度（先防止 free/长周期误进 5h，再避免数据完全丢失）
func pickClassifiedWhamWindows(primary, secondary *WhamUsageWindow, planType string, now time.Time) (w5h, w7d *WhamUsageWindow) {
	for _, w := range []*WhamUsageWindow{primary, secondary} {
		if w == nil {
			continue
		}
		switch {
		case w.LimitWindowSeconds == whamWindow5hSeconds:
			if w5h == nil {
				w5h = w
			}
		// 长窗口槽(7d)：plus/pro 为周窗(604800)，team plan 为月窗(约 2592000，28–31 天容差)。
		case w.LimitWindowSeconds == whamWindow7dSeconds || auth.IsMonthlyWindowSeconds(w.LimitWindowSeconds):
			if w7d == nil {
				w7d = w
			}
		}
	}

	if w5h == nil && primary != nil && primary != w7d {
		if shouldTreatUnknownWhamWindowAs7d(primary, planType, now) && w7d == nil {
			w7d = primary
		} else {
			w5h = primary
		}
	}
	if w7d == nil && secondary != nil && secondary != w5h {
		if shouldTreatUnknownWhamWindowAs7d(secondary, planType, now) {
			w7d = secondary
		} else if w5h == nil {
			w5h = secondary
		} else {
			w7d = secondary
		}
	}
	return w5h, w7d
}

func shouldTreatUnknownWhamWindowAs7d(w *WhamUsageWindow, planType string, now time.Time) bool {
	if w == nil {
		return false
	}
	if auth.NormalizePlanType(planType) == "free" {
		return true
	}
	if w.ResetAfterSeconds > whamWindow5hSeconds {
		return true
	}
	if w.ResetAt > 0 && !now.IsZero() && time.Unix(w.ResetAt, 0).After(now.Add(time.Duration(whamWindow5hSeconds)*time.Second)) {
		return true
	}
	return false
}

func whamWindowResetAt(w *WhamUsageWindow, now time.Time) time.Time {
	if w == nil {
		return time.Time{}
	}
	// reset_at 是 unix 时间戳（秒），优先使用；缺失时 fallback 到 reset_after_seconds
	if w.ResetAt > 0 {
		return time.Unix(w.ResetAt, 0)
	}
	if w.ResetAfterSeconds > 0 {
		return now.Add(time.Duration(w.ResetAfterSeconds) * time.Second)
	}
	return time.Time{}
}
