package admin

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/internal/grokquota"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
)

// ProbeUsageSnapshot 主动刷新账号用量。
//
// 优先尝试 /backend-api/wham/usage（零额度成本的结构化端点）；
// 失败时（4xx/5xx/网络）回退到给 /backend-api/codex/responses 发一个最小请求
// （会真实计入用量但保证向下兼容）。
func (h *Handler) ProbeUsageSnapshot(ctx context.Context, account *auth.Account) error {
	if account == nil {
		return nil
	}

	account.Mu().RLock()
	hasToken := account.AccessToken != ""
	account.Mu().RUnlock()
	if !hasToken {
		return nil
	}

	if account.IsGrokPlatform() {
		return h.probeGrokQuotaUsage(ctx, account)
	}

	// 限流/冷却（429 或 premium 5h 限流）状态下只做 wham（零成本），
	// 失败也不回退 /responses，避免加重限流或额外消耗额度。
	limited := account.InLimitedState()
	whamOnly := limited || h.store.GetLazyMode() || !h.store.UsageProbeResponsesFallbackEnabled()

	// 1) 优先用 wham（零成本）
	if err := h.probeUsageViaWham(ctx, account, limited); err == nil {
		return nil
	} else {
		if whamOnly {
			log.Printf("[账号 %d] wham 用量探测失败，已按配置/限流状态跳过 /responses 探针: %v", account.DBID, err)
			return err
		}
		log.Printf("[账号 %d] wham 用量探测失败，回退到 /responses 探针: %v", account.DBID, err)
	}

	// 2) Fallback: 原有的 /responses 最小探针
	return h.probeUsageViaResponses(ctx, account)
}

func (h *Handler) probeGrokQuotaUsage(ctx context.Context, account *auth.Account) error {
	if h == nil || h.db == nil || account == nil {
		return nil
	}
	account.Mu().RLock()
	accessToken := account.AccessToken
	baseURL := account.BaseURL
	accountID := account.DBID
	account.Mu().RUnlock()
	client := grokquota.Client{
		HTTPClient: auth.BuildHTTPClient(h.store.ResolveProxyForAccount(account)),
		BaseURL:    baseURL,
		Token:      accessToken,
	}
	// 本地/测试 mock：BaseURL 为 loopback 时 billing/subscriptions 也走同一 origin，避免被改写到 grok.com
	if u, err := url.Parse(baseURL); err == nil {
		host := u.Hostname()
		if host == "127.0.0.1" || host == "localhost" || host == "::1" {
			client.BillingBaseURL = u.Scheme + "://" + u.Host
		}
	}

	// 1) 优先 grok.com /rest/subscriptions（账单页 SuperGrok 权威源）。
	// Bearer 与 billing 相同；401/403 不视为「无套餐」，不覆盖已有 plan_type。
	// 成功写入后即使后续 /responses 402 也不得清掉 plan。
	planFromSubscriptions := false
	subResult, subErr := client.FetchSubscriptions(ctx)
	if subErr != nil {
		if subResult != nil && subResult.AuthFailed {
			log.Printf("[账号 %d] Grok /rest/subscriptions 鉴权失败(status=%d)，保留已有 plan_type: %v",
				accountID, subResult.HTTPStatus, subErr)
		} else {
			log.Printf("[账号 %d] Grok /rest/subscriptions 拉取失败: %v", accountID, subErr)
		}
	} else if subResult != nil && subResult.PlanKey != "" {
		// PlanKey 非空才写库：含 free（合法空列表）；401/未观测 PlanKey 为空不写。
		credentials := map[string]interface{}{
			"plan_type": subResult.PlanKey,
		}
		rawTier := subResult.Subscription.RawTier()
		if rawTier != "" {
			credentials["subscription_tier"] = rawTier
		}
		if end := strings.TrimSpace(subResult.Subscription.BillingPeriodEnd); end != "" {
			credentials["subscription_expires_at"] = end
		}
		if updateErr := h.db.UpdateCredentials(ctx, accountID, credentials); updateErr != nil {
			log.Printf("[账号 %d] 保存 Grok 订阅套餐失败: %v", accountID, updateErr)
		} else {
			planFromSubscriptions = true
			h.store.UpdateAccountPlanType(account, subResult.PlanKey)
			log.Printf("[账号 %d] Grok 订阅套餐 plan=%s raw=%s", accountID, subResult.PlanKey, rawTier)
		}
	}

	billingResult, billingErr := client.FetchCreditsConfig(ctx)
	if billingResult != nil {
		credentials := map[string]interface{}{
			grokquota.CredentialsKeyBillingDiagnostic: billingResult.Snapshot,
		}
		if billingResult.Snapshot.Source == grokquota.SourceGrokBuildBilling && billingResult.Snapshot.State == grokquota.StateObserved {
			credentials[grokquota.CredentialsKeyUsageSnapshot] = billingResult.Snapshot
		}
		if updateErr := h.db.UpdateCredentials(ctx, accountID, credentials); updateErr != nil {
			log.Printf("[账号 %d] 保存 Grok billing 用量状态失败: %v", accountID, updateErr)
		} else if billingResult.Snapshot.Source == grokquota.SourceGrokBuildBilling && billingResult.Snapshot.State == grokquota.StateObserved {
			// 同步内存 7d 快照/重置点（仅 weekly；不写 5h）。
			var pct *float64
			if billingResult.Snapshot.UsedPercent != nil {
				pct = billingResult.Snapshot.UsedPercent
			} else if billingResult.Snapshot.APIUsedPercent != nil {
				pct = billingResult.Snapshot.APIUsedPercent
			}
			if pct != nil {
				fetchedAt := time.Now().UTC()
				if ts := strings.TrimSpace(billingResult.Snapshot.FetchedAt); ts != "" {
					if t, err := time.Parse(time.RFC3339, ts); err == nil {
						fetchedAt = t
					}
				}
				account.SetUsageSnapshot(*pct, fetchedAt)
				if billingResult.Snapshot.Window != nil {
					if resetRaw := strings.TrimSpace(billingResult.Snapshot.Window.ResetAt); resetRaw != "" {
						if resetAt, err := time.Parse(time.RFC3339, resetRaw); err == nil {
							account.SetReset7dAt(resetAt)
						}
					}
				}
			}
		}
	}
	if billingErr == nil && billingResult != nil && billingResult.Snapshot.State == grokquota.StateObserved {
		// billing 成功只记录健康分样本；不 ClearError / ClearCooldown，也不短路。
		// 必须继续执行 /responses 探针来校验可调度性。
		h.store.ReportRequestSuccess(account, 0)
		// 继续走 headerResult / FetchQuotaSnapshot
	}
	if billingResult != nil {
		switch billingResult.Snapshot.State {
		case grokquota.StateUnauthorized:
			h.store.ReportRequestFailure(account, "client", 0)
			h.store.MarkCooldownWithError(account, 24*time.Hour, "unauthorized", "Grok billing 用量接口返回未授权")
			return billingErr
		case grokquota.StateForbidden:
			status := billingResult.Snapshot.UpstreamStatusCode
			if status == 0 {
				status = 403
			}
			if grokquota.IsXAISpendingLimit(status, billingResult.RawBody) {
				msg := fmt.Sprintf("Grok billing 用量接口额度用尽: %s", truncate(string(billingResult.RawBody), 300))
				h.store.ReportRequestFailure(account, "client", 0)
				h.store.MarkCooldownWithError(account, 7*24*time.Hour, "rate_limited", msg)
				return billingErr
			}
			h.store.MarkError(account, "Grok billing 用量接口返回无权限")
			return billingErr
		case grokquota.StateRateLimited:
			h.store.ReportRequestFailure(account, "client", 0)
			return billingErr
		}
	}

	headerResult, headerErr := client.FetchQuotaSnapshot(ctx)
	if headerResult != nil {
		// header 观测仍落库；套餐仅在订阅未成功时作降级写入（不覆盖 subscriptions 权威源）。
		credentials := map[string]interface{}{
			grokquota.CredentialsKeyHeaderObservation: headerResult.Snapshot,
		}
		tier, entitlement := grokquota.SubscriptionFieldsFromSnapshot(&headerResult.Snapshot)
		if !planFromSubscriptions && tier != "" {
			credentials["subscription_tier"] = tier
			if planKey := grokquota.MapSubscriptionTierToPlanKey(tier); planKey != "" {
				credentials["plan_type"] = planKey
			}
		}
		if entitlement != "" {
			credentials["entitlement_status"] = entitlement
		}
		if updateErr := h.db.UpdateCredentials(ctx, accountID, credentials); updateErr != nil {
			log.Printf("[账号 %d] 保存 Grok header 观测状态失败: %v", accountID, updateErr)
		}
		if !planFromSubscriptions && tier != "" {
			if planKey := grokquota.MapSubscriptionTierToPlanKey(tier); planKey != "" {
				// 同步内存 PlanType + 调度索引；DB plan_type 已在上方写入，重复写仅在变更时发生
				h.store.UpdateAccountPlanType(account, planKey)
			}
		}
	}
	if headerErr != nil {
		if headerResult != nil {
			switch headerResult.Snapshot.State {
			case grokquota.StateUnauthorized:
				h.store.ReportRequestFailure(account, "client", 0)
				h.store.MarkCooldownWithError(account, 24*time.Hour, "unauthorized", "Grok quota header 探针返回未授权")
			case grokquota.StateForbidden:
				// 精确识别 bad-credentials，优先尝试安全刷新一次（最多一次，由 Store 锁保证）
				body := headerResult.RawBody
				status := headerResult.Snapshot.UpstreamStatusCode
				if status == 0 {
					status = 403
				}
				if grokquota.IsXAIInvalidAccessToken(status, body) {
					if recErr := h.store.RefreshAccountAfterAuthFailure(ctx, account, accessToken); recErr != nil {
						if recErr == auth.ErrRefreshTokenMissing {
							h.store.MarkError(account, "Grok quota header 探针返回无权限（缺少 refresh_token，需要重新授权）")
							return recErr
						}
						// 刷新失败（RT 失效等），保留原错误，不无限重试
						h.store.MarkError(account, "Grok quota header 探针返回无权限（刷新失败）")
						return recErr
					}
					// 刷新成功，清除可能的 error 状态，由下一次 probe 或手动测试验证
					h.store.ClearCooldown(account)
					log.Printf("[账号 %d] Grok quota header 探针检测到 bad-credentials，已成功刷新 Access Token", accountID)
					return nil
				}
				// spending/pending-limit 是额度耗尽，标 rate_limited，勿 MarkError
				if grokquota.IsXAISpendingLimit(status, body) {
					msg := fmt.Sprintf("Grok quota header 探针额度用尽: %s", truncate(string(body), 300))
					h.store.ReportRequestFailure(account, "client", 0)
					h.store.MarkCooldownWithError(account, 7*24*time.Hour, "rate_limited", msg)
					return headerErr
				}
				h.store.MarkError(account, fmt.Sprintf("Grok quota header 探针返回无权限: %s", truncate(string(body), 300)))
			case grokquota.StateRateLimited:
				h.store.ReportRequestFailure(account, "client", 0)
			case grokquota.StateUnavailable:
				if headerResult.Snapshot.UpstreamStatusCode >= 500 || headerResult.Snapshot.UpstreamStatusCode == 0 {
					h.store.ReportRequestFailure(account, "server", 0)
				}
			}
		}
		if billingErr != nil {
			return billingErr
		}
		return headerErr
	}
	if headerResult != nil && headerResult.Snapshot.State == grokquota.StateObserved {
		h.store.ReportRequestSuccess(account, 0)
		h.store.ClearCooldown(account)
	}
	if billingErr != nil {
		log.Printf("[账号 %d] Grok billing weekly 用量暂不可用，已保留旧周额度并保存 header 诊断: %v", accountID, billingErr)
	}
	return nil
}

// probeUsageViaWham 通过 /backend-api/wham/usage 拉取用量，
// 不消耗任何 token 额度。
//
// limited=true 表示账号正处于 429 冷却 / premium 5h 限流状态：本次仅为零成本刷新
// 「主动重置次数」与用量快照，不上报成功、也不清除冷却（冷却解除交给恢复探针/到期判断），
// 避免把一次额度查询误判为账号已恢复。
func (h *Handler) probeUsageViaWham(ctx context.Context, account *auth.Account, limited bool) error {
	usage, resp, err := proxy.QueryWhamUsage(ctx, account, h.store.ResolveProxyForAccount(account))
	if resp != nil {
		// QueryWhamUsage 在非 200 时不会读 body；这里读取一小段用于账号错误详情。
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			h.store.ReportRequestFailure(account, "client", 0)
			h.store.MarkCooldownWithError(account, 24*time.Hour, "unauthorized", fmt.Sprintf("用量探针 wham 上游返回 %d: %s", resp.StatusCode, truncate(string(body), 300)))
		case http.StatusTooManyRequests:
			h.store.ReportRequestFailure(account, "client", 0)
		}
	}
	if err != nil {
		return err
	}
	if usage == nil {
		return fmt.Errorf("wham returned empty body")
	}

	state := proxy.ApplyWhamUsage(h.store, account, usage)
	if limited {
		// 限流/冷却态下，用 wham 返回的权威用量窗口重新判定：
		// 若上游已重置窗口、不再限流（例如官方提前重置了 5h/7d 用量），
		// 则主动解除限流冷却，无需等待冷却到期或用户手动测试连接。
		// 仍不调用 ReportRequestSuccess，避免把一次零成本额度查询计入健康成功样本。
		if !applyUsageLimitedAccountState(h.store, account, state) {
			h.store.ClearCooldown(account)
			log.Printf("[账号 %d] wham 显示限流窗口已重置，自动解除限流冷却", account.DBID)
		}
		return nil
	}
	h.store.ReportRequestSuccess(account, 0)
	// 用量未耗尽时重置冷却
	if !applyUsageLimitedAccountState(h.store, account, state) {
		h.store.ClearCooldown(account)
	}
	return nil
}

// probeUsageViaResponses 原有探针：发送最小 /responses 请求，
// 通过响应头同步 Codex 用量状态。会真实消耗少量 token。
func (h *Handler) probeUsageViaResponses(ctx context.Context, account *auth.Account) error {
	payload := buildConnectionTestPayload(h.store, h.store.GetTestModel())
	resp, err := proxy.ExecuteRequest(ctx, account, payload, "", h.store.ResolveProxyForAccount(account), "", nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	usageState := proxy.SyncCodexUsageState(h.store, account, resp)

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	switch resp.StatusCode {
	case http.StatusOK:
		h.store.ReportRequestSuccess(account, 0)
		// 只有用量未耗尽时才重置状态
		if !applyUsageLimitedAccountState(h.store, account, usageState) {
			h.store.ClearCooldown(account)
		}
		return nil
	case http.StatusUnauthorized:
		h.store.ReportRequestFailure(account, "client", 0)
		h.store.MarkCooldownWithError(account, 24*time.Hour, "unauthorized", fmt.Sprintf("用量探针上游返回 %d: %s", resp.StatusCode, truncate(string(body), 300)))
		return nil
	case http.StatusTooManyRequests:
		h.store.ReportRequestFailure(account, "client", 0)
		proxy.Apply429Cooldown(h.store, account, body, resp, h.store.GetTestModel())
		return nil
	default:
		if proxy.IsUsageLimitReachedError(body) {
			h.store.ReportRequestFailure(account, "client", 0)
			proxy.Apply429Cooldown(h.store, account, body, resp, h.store.GetTestModel())
			return nil
		}
		if grokquota.IsXAISpendingLimit(resp.StatusCode, body) {
			msg := fmt.Sprintf("用量探针上游返回 %d: %s", resp.StatusCode, truncate(string(body), 300))
			h.store.ReportRequestFailure(account, "client", 0)
			h.store.MarkCooldownWithError(account, 7*24*time.Hour, "rate_limited", msg)
			return nil
		}
		if shouldMarkUsageProbeAccountError(resp.StatusCode, body) {
			h.store.MarkError(account, fmt.Sprintf("用量探针上游返回 %d: %s", resp.StatusCode, truncate(string(body), 300)))
			return nil
		}
		if resp.StatusCode >= 500 {
			h.store.ReportRequestFailure(account, "server", 0)
		} else if resp.StatusCode >= 400 {
			h.store.ReportRequestFailure(account, "client", 0)
		}
		return fmt.Errorf("探针返回状态 %d", resp.StatusCode)
	}
}

func shouldMarkUsageProbeAccountError(statusCode int, body []byte) bool {
	switch statusCode {
	case http.StatusPaymentRequired, http.StatusForbidden:
		return proxy.IsDeactivatedWorkspaceError(body)
	default:
		return false
	}
}

// ForceUsageProbe 主动触发一次"忽略缓存阈值"的全量用量探针，并立即返回。
// 真正的探针在后台并发执行（受 usage_probe_concurrency 限制）。
func (h *Handler) ForceUsageProbe(c *gin.Context) {
	h.store.TriggerUsageProbeForceAsync()
	payload := gin.H{
		"triggered":   true,
		"concurrency": h.store.GetUsageProbeConcurrency(),
	}
	if h.store.GetLazyMode() || !h.store.UsageProbeResponsesFallbackEnabled() {
		payload["mode"] = "wham_only"
	}
	c.JSON(http.StatusOK, payload)
}
