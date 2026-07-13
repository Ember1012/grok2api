package grokquota

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	billingPath        = "/grok_api_v2.GrokBuildBilling/GetGrokCreditsConfig"
	subscriptionsPath  = "/rest/subscriptions"
	responsesProbePath = "/responses"
	defaultAPIBaseURL  = "https://api.x.ai/v1"
	defaultProbeModel  = "grok-4.3"
	grpcStatusOK       = "0"
)

var ErrEmptyGrpcWebMessage = errors.New("empty grpc-web message")

var emptyGrpcWebRequest = []byte{0, 0, 0, 0, 0}

type Client struct {
	HTTPClient     *http.Client
	BaseURL        string
	BillingBaseURL string
	Token          string
}

type ProbeResult struct {
	Snapshot UsageSnapshot
	RawBody  []byte
}

// SubscriptionResult 是 grok.com /rest/subscriptions 的解析结果。
// 鉴权使用 xAI OAuth access_token 的 Bearer 路径（与 billing 一致）。
// 测试仅 mock HTTP；运行时若 401 可能需后续 session cookie 路径，本层不引入 cookie。
type SubscriptionResult struct {
	Subscription GrokWebSubscription
	PlanKey      string
	HTTPStatus   int
	// AuthFailed 表示 401/403：不是「无套餐」，调用方不得据此清空 plan_type。
	AuthFailed bool
	RawBody    []byte
}

func (c Client) FetchQuotaSnapshot(ctx context.Context) (*ProbeResult, error) {
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	payload, err := json.Marshal(map[string]any{
		"model":             defaultProbeModel,
		"input":             ".",
		"max_output_tokens": 1,
		"store":             false,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, responsesProbeURL(c.BaseURL), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	applyResponsesProbeHeaders(req, c.Token)

	resp, err := client.Do(req)
	if err != nil {
		snapshot := NewSourceErrorSnapshot(SourceHeaderObserver, StateUnavailable, "request_failed", err.Error(), 0, "")
		return &ProbeResult{Snapshot: snapshot}, err
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	requestID := firstHeader(resp.Header, "x-request-id", "xai-request-id")
	if readErr != nil {
		snapshot := NewSourceErrorSnapshot(SourceHeaderObserver, StateUnavailable, "read_failed", readErr.Error(), resp.StatusCode, requestID)
		return &ProbeResult{Snapshot: snapshot, RawBody: body}, readErr
	}

	headers, rateLimits := observeQuotaHeaders(resp.Header)
	if resp.StatusCode == http.StatusOK {
		if len(headers) == 0 {
			snapshot := NewSourceErrorSnapshot(SourceHeaderObserver, StateUnavailable, "no_quota_headers", "No xAI quota headers observed on the latest Grok probe", resp.StatusCode, requestID)
			return &ProbeResult{Snapshot: snapshot, RawBody: body}, nil
		}
		snapshot := NewObservedHeaderSnapshot(headers, rateLimits, resp.StatusCode, requestID)
		return &ProbeResult{Snapshot: snapshot, RawBody: body}, nil
	}

	state := StateUnavailable
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		state = StateUnauthorized
	case http.StatusForbidden:
		state = StateForbidden
	case http.StatusTooManyRequests:
		state = StateRateLimited
	}
	msg := truncateBody(body, 240)
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	snapshot := NewSourceErrorSnapshot(SourceHeaderObserver, state, "upstream_status", msg, resp.StatusCode, requestID)
	snapshot.Headers = headers
	snapshot.RateLimits = rateLimits
	snapshot.SubscriptionTier = firstMapValue(headers, "xai-subscription-tier", "x-subscription-tier")
	snapshot.EntitlementStatus = firstMapValue(headers, "xai-entitlement-status", "x-entitlement-status")
	return &ProbeResult{Snapshot: snapshot, RawBody: body}, fmt.Errorf("grok quota probe returned status %d", resp.StatusCode)
}

func responsesProbeURL(raw string) string {
	return normalizeResponsesBaseURL(raw) + responsesProbePath
}

func normalizeResponsesBaseURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return defaultAPIBaseURL
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return defaultAPIBaseURL
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	if path == "" {
		parsed.Path = "/v1"
		parsed.RawPath = ""
		return strings.TrimRight(parsed.String(), "/")
	}
	if strings.HasSuffix(path, "/v1") {
		parsed.RawPath = ""
		return strings.TrimRight(parsed.String(), "/")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1"
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/")
}

func applyResponsesProbeHeaders(req *http.Request, token string) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "grok2api-grok-quota-probe/1.0")
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
}

func observeQuotaHeaders(headers http.Header) (map[string]string, map[string]RateLimitWindow) {
	observed := map[string]string{}
	for _, key := range []string{
		"x-ratelimit-limit-requests",
		"x-ratelimit-remaining-requests",
		"x-ratelimit-reset-requests",
		"x-ratelimit-limit-tokens",
		"x-ratelimit-remaining-tokens",
		"x-ratelimit-reset-tokens",
		"retry-after",
		"xai-subscription-tier",
		"x-subscription-tier",
		"xai-entitlement-status",
		"x-entitlement-status",
	} {
		if value := strings.TrimSpace(headers.Get(key)); value != "" {
			observed[key] = value
		}
	}
	return observed, parseRateLimitWindows(observed)
}

func parseRateLimitWindows(headers map[string]string) map[string]RateLimitWindow {
	windows := map[string]RateLimitWindow{}
	for _, dimension := range []string{"requests", "tokens"} {
		window := RateLimitWindow{
			Limit:     parseInt64Ptr(headers["x-ratelimit-limit-"+dimension]),
			Remaining: parseInt64Ptr(headers["x-ratelimit-remaining-"+dimension]),
		}
		if reset := parseResetHeader(headers["x-ratelimit-reset-"+dimension]); reset != "" {
			window.ResetAt = reset
		}
		if window.Limit != nil || window.Remaining != nil || window.ResetAt != "" {
			windows[dimension] = window
		}
	}
	if len(windows) == 0 {
		return nil
	}
	return windows
}

func parseInt64Ptr(raw string) *int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil
	}
	return &value
}

func parseResetHeader(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if seconds > 0 {
			return time.Unix(seconds, 0).UTC().Format(time.RFC3339)
		}
		return ""
	}
	if ts, err := time.Parse(time.RFC3339, raw); err == nil {
		return ts.UTC().Format(time.RFC3339)
	}
	return ""
}

func truncateBody(body []byte, limit int) string {
	msg := strings.TrimSpace(string(body))
	if len(msg) > limit {
		msg = msg[:limit]
	}
	return msg
}

func grpcWebFrameShapes(raw []byte) []GrpcWebFrameShape {
	frames := make([]GrpcWebFrameShape, 0)
	for offset := 0; offset < len(raw); {
		if len(raw)-offset < 5 {
			break
		}
		flag := raw[offset]
		length := int(binary.BigEndian.Uint32(raw[offset+1 : offset+5]))
		offset += 5
		if length < 0 || offset+length > len(raw) {
			break
		}
		frames = append(frames, GrpcWebFrameShape{Flag: flag, Length: length, IsTrailer: flag&0x80 != 0})
		offset += length
	}
	return frames
}

func protobufDiagnostic(message []byte, stage string) *DecodeDiagnostic {
	hash := sha256.Sum256(message)
	return &DecodeDiagnostic{
		Stage:               stage,
		MessageSize:         len(message),
		MessageSHA256Prefix: hex.EncodeToString(hash[:])[:16],
		ProtobufFields:      protobufFieldShapes(message, "", 0),
	}
}

func protobufDecodeStage(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "missing usage percent field"):
		return "percent_field_missing"
	case strings.Contains(msg, "ambiguous usage percent fields"):
		return "percent_field_ambiguous"
	case strings.Contains(msg, "invalid usage percent candidate"):
		return "percent_field_invalid"
	case strings.Contains(msg, "missing credits config field"):
		return "credits_config_missing"
	default:
		return "protobuf_decode_failed"
	}
}

func protobufFieldShapes(data []byte, prefix string, depth int) []ProtobufFieldShape {
	if len(data) == 0 || depth > 2 {
		return nil
	}
	fields, err := parseProtoFields(data)
	if err != nil {
		return nil
	}
	shapes := make([]ProtobufFieldShape, 0, len(fields))
	for _, field := range fields {
		path := fmt.Sprintf("%d", field.Number)
		if prefix != "" {
			path = prefix + "." + path
		}
		shape := ProtobufFieldShape{Path: path, Number: field.Number, Wire: field.Wire}
		if len(field.Bytes) > 0 {
			shape.Length = len(field.Bytes)
		}
		shapes = append(shapes, shape)
		if field.Wire == 2 && len(field.Bytes) > 0 {
			shapes = append(shapes, protobufFieldShapes(field.Bytes, path, depth+1)...)
		}
	}
	return shapes
}

func (c Client) FetchCreditsConfig(ctx context.Context) (*ProbeResult, error) {
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	billingBase := strings.TrimRight(strings.TrimSpace(c.BillingBaseURL), "/")
	if billingBase == "" {
		billingBase = billingBaseURL(c.BaseURL)
	}
	url := billingBase + billingPath

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(emptyGrpcWebRequest))
	if err != nil {
		return nil, err
	}
	applyBillingHeaders(req, c.Token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	requestID := firstHeader(resp.Header, "x-trace-id", "x-request-id", "xai-request-id")
	if readErr != nil {
		snapshot := NewErrorSnapshot(StateUnavailable, "read_failed", readErr.Error(), resp.StatusCode, requestID)
		return &ProbeResult{Snapshot: snapshot, RawBody: body}, readErr
	}
	if resp.StatusCode != http.StatusOK {
		state := StateBillingUnavailable
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			state = StateUnauthorized
		case http.StatusForbidden:
			state = StateForbidden
		case http.StatusTooManyRequests:
			state = StateRateLimited
		}
		msg := strings.TrimSpace(string(body))
		if len(msg) > 240 {
			msg = msg[:240]
		}
		snapshot := NewErrorSnapshot(state, "upstream_status", msg, resp.StatusCode, requestID)
		return &ProbeResult{Snapshot: snapshot, RawBody: body}, fmt.Errorf("grok billing returned status %d", resp.StatusCode)
	}

	message, trailers, err := decodeUnaryGrpcWeb(body)
	frames := grpcWebFrameShapes(body)
	if status := trailers["grpc-status"]; status != "" && status != grpcStatusOK {
		msg := trailers["grpc-message"]
		snapshot := NewErrorSnapshot(StateBillingUnavailable, "grpc_status_"+status, msg, resp.StatusCode, requestID)
		snapshot.DecodeDiagnostic = &DecodeDiagnostic{Stage: "grpc_status", GrpcWebFrames: frames}
		return &ProbeResult{Snapshot: snapshot, RawBody: body}, fmt.Errorf("grok billing grpc status %s: %s", status, msg)
	}
	if err != nil {
		state := StateDecodeFailed
		code := "grpc_web_decode_failed"
		if errors.Is(err, ErrEmptyGrpcWebMessage) {
			state = StateBillingUnavailable
			code = "empty_grpc_web_message"
		}
		snapshot := NewErrorSnapshot(state, code, err.Error(), resp.StatusCode, requestID)
		snapshot.DecodeDiagnostic = &DecodeDiagnostic{Stage: code, GrpcWebFrames: frames}
		return &ProbeResult{Snapshot: snapshot, RawBody: body}, err
	}

	decoded, err := decodeCreditsConfigMessage(message)
	if err != nil {
		snapshot := NewErrorSnapshot(StateDecodeFailed, "protobuf_decode_failed", err.Error(), resp.StatusCode, requestID)
		snapshot.DecodeDiagnostic = protobufDiagnostic(message, protobufDecodeStage(err))
		snapshot.DecodeDiagnostic.GrpcWebFrames = frames
		return &ProbeResult{Snapshot: snapshot, RawBody: body}, err
	}
	snapshot := NewObservedBillingSnapshot(decoded.Percent, decoded.StartAt, decoded.ResetAt, resp.StatusCode, requestID)
	stage := "observed"
	if decoded.PercentPath == "default_zero" {
		stage = "percent_default_zero"
	}
	snapshot.DecodeDiagnostic = protobufDiagnostic(message, stage)
	snapshot.DecodeDiagnostic.GrpcWebFrames = frames
	snapshot.DecodeDiagnostic.UsagePercentPath = decoded.PercentPath
	return &ProbeResult{Snapshot: snapshot, RawBody: body}, nil
}

// FetchSubscriptions 拉取 grok.com 当前账号订阅（账单页 SuperGrok 权威源）。
// GET {billingBaseURL}/rest/subscriptions，Bearer 与 billing 相同。
// 不解析/不返回 purchaseToken 等支付细节。
func (c Client) FetchSubscriptions(ctx context.Context) (*SubscriptionResult, error) {
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	billingBase := strings.TrimRight(strings.TrimSpace(c.BillingBaseURL), "/")
	if billingBase == "" {
		billingBase = billingBaseURL(c.BaseURL)
	}
	url := billingBase + subscriptionsPath

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	applySubscriptionsHeaders(req, c.Token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return &SubscriptionResult{HTTPStatus: resp.StatusCode, RawBody: body}, readErr
	}
	if resp.StatusCode != http.StatusOK {
		authFailed := resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden
		msg := truncateBody(body, 240)
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return &SubscriptionResult{
			HTTPStatus: resp.StatusCode,
			AuthFailed: authFailed,
			RawBody:    body,
		}, fmt.Errorf("grok subscriptions returned status %d: %s", resp.StatusCode, msg)
	}

	sub, err := parseGrokWebSubscriptionsJSON(body)
	if err != nil {
		return &SubscriptionResult{HTTPStatus: resp.StatusCode, RawBody: body}, err
	}
	return &SubscriptionResult{
		Subscription: sub,
		PlanKey:      MapGrokWebSubscriptionToPlanKey(sub),
		HTTPStatus:   resp.StatusCode,
		RawBody:      body,
	}, nil
}

func billingBaseURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "https://grok.com"
	}
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "grok.com") {
		return raw
	}
	return "https://grok.com"
}

func applyBillingHeaders(req *http.Request, token string) {
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Content-Type", "application/grpc-web+proto")
	req.Header.Set("Origin", "https://grok.com")
	req.Header.Set("Referer", "https://grok.com/?_s=usage")
	req.Header.Set("X-Grpc-Web", "1")
	req.Header.Set("X-User-Agent", "connect-es/2.1.1")
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
}

// applySubscriptionsHeaders：JSON GET，Bearer 与 billing 一致（grok.com 用 OAuth AT）。
// 若生产 401，可能需 session cookie 路径——此处仅 Bearer，不引入 cookie。
func applySubscriptionsHeaders(req *http.Request, token string) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "https://grok.com")
	req.Header.Set("Referer", "https://grok.com/")
	req.Header.Set("User-Agent", "grok2api-grok-quota-probe/1.0")
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
}

// parseGrokWebSubscriptionsJSON 只取套餐判定字段，忽略 purchaseToken/payment 等。
// 兼容两种形状：
// A) REST: { "subscriptions": [ { tier, status, billingPeriodEnd } ] }（含空数组）
// B) SSR: { isSuperGrokUser, bestSubscription, activeSubscriptions, ... }
// {} 无 subscriptions 且无 SSR 字段 → Observed=false。
func parseGrokWebSubscriptionsJSON(body []byte) (GrokWebSubscription, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return GrokWebSubscription{}, fmt.Errorf("decode grok subscriptions: %w", err)
	}

	// A) REST：存在 subscriptions 数组字段（含空数组）即为已观测。
	if rawList, ok := top["subscriptions"]; ok {
		var items []struct {
			Tier             string `json:"tier"`
			Status           string `json:"status"`
			BillingPeriodEnd string `json:"billingPeriodEnd"`
		}
		if err := json.Unmarshal(rawList, &items); err != nil {
			return GrokWebSubscription{}, fmt.Errorf("decode grok subscriptions list: %w", err)
		}
		return parseRESTSubscriptionsList(items), nil
	}

	// B) SSR：顶层 flag / bestSubscription / activeSubscriptions 任一字段存在。
	ssrKeys := []string{
		"isSuperGrokUser", "isSuperGrokProUser", "isSuperGrokLiteUser",
		"bestSubscription", "activeSubscriptions",
	}
	hasSSR := false
	for _, k := range ssrKeys {
		if _, ok := top[k]; ok {
			hasSSR = true
			break
		}
	}
	if !hasSSR {
		return GrokWebSubscription{Observed: false}, nil
	}

	var raw struct {
		IsSuperGrokLiteUser bool   `json:"isSuperGrokLiteUser"`
		IsSuperGrokUser     bool   `json:"isSuperGrokUser"`
		IsSuperGrokProUser  bool   `json:"isSuperGrokProUser"`
		BestSubscription    string `json:"bestSubscription"`
		ActiveSubscriptions []struct {
			Tier             string `json:"tier"`
			Status           string `json:"status"`
			BillingPeriodEnd string `json:"billingPeriodEnd"`
		} `json:"activeSubscriptions"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return GrokWebSubscription{}, fmt.Errorf("decode grok subscriptions: %w", err)
	}
	out := GrokWebSubscription{
		Observed:            true,
		IsSuperGrokLiteUser: raw.IsSuperGrokLiteUser,
		IsSuperGrokUser:     raw.IsSuperGrokUser,
		IsSuperGrokProUser:  raw.IsSuperGrokProUser,
		BestSubscription:    strings.TrimSpace(raw.BestSubscription),
	}
	if len(raw.ActiveSubscriptions) > 0 {
		a := raw.ActiveSubscriptions[0]
		out.ActiveTier = strings.TrimSpace(a.Tier)
		out.ActiveStatus = strings.TrimSpace(a.Status)
		out.BillingPeriodEnd = normalizeBillingPeriodEnd(a.BillingPeriodEnd)
	}
	return out, nil
}

func parseRESTSubscriptionsList(items []struct {
	Tier             string `json:"tier"`
	Status           string `json:"status"`
	BillingPeriodEnd string `json:"billingPeriodEnd"`
}) GrokWebSubscription {
	out := GrokWebSubscription{Observed: true}
	bestRank := -1
	for _, it := range items {
		tier := strings.TrimSpace(it.Tier)
		status := strings.TrimSpace(it.Status)
		if tier == "" || !subscriptionStatusActive(status) {
			continue
		}
		applyActiveRESTTierFlags(&out, tier)
		rank := subscriptionTierRank(tier)
		if rank < bestRank {
			continue
		}
		// 同档保留先出现的；更高档覆盖
		if rank > bestRank {
			bestRank = rank
			out.BestSubscription = tier
			out.ActiveTier = tier
			out.ActiveStatus = status
			out.BillingPeriodEnd = normalizeBillingPeriodEnd(it.BillingPeriodEnd)
		}
	}
	return out
}

func subscriptionStatusActive(status string) bool {
	return strings.Contains(strings.ToLower(status), "active")
}

// subscriptionTierRank：heavy > super > lite > 其它；与前端 getSubscriptionsQueryData 对齐。
func subscriptionTierRank(tier string) int {
	lower := strings.ToLower(strings.TrimSpace(tier))
	switch {
	case strings.Contains(lower, "super_grok_pro") || strings.Contains(lower, "heavy"):
		return 3
	case strings.Contains(lower, "lite"):
		return 1
	case strings.Contains(lower, "grok_pro") || strings.Contains(lower, "super"):
		return 2
	default:
		return 0
	}
}

// applyActiveRESTTierFlags：ACTIVE 条目按 tier 设 flag（SUPER_GROK_PRO 先于 GROK_PRO）。
func applyActiveRESTTierFlags(out *GrokWebSubscription, tier string) {
	lower := strings.ToLower(strings.TrimSpace(tier))
	switch {
	case strings.Contains(lower, "super_grok_pro"):
		out.IsSuperGrokProUser = true
	case strings.Contains(lower, "lite"):
		out.IsSuperGrokLiteUser = true
	case strings.Contains(lower, "grok_pro"):
		out.IsSuperGrokUser = true
	}
}

func normalizeBillingPeriodEnd(raw string) string {
	end := strings.TrimSpace(raw)
	if end == "" {
		return ""
	}
	if ts, err := time.Parse(time.RFC3339, end); err == nil {
		return ts.UTC().Format(time.RFC3339)
	}
	if ts, err := time.Parse(time.RFC3339Nano, end); err == nil {
		return ts.UTC().Format(time.RFC3339)
	}
	// 非标准时间戳原样保留（调用方仍可展示）
	return end
}

func decodeUnaryGrpcWeb(raw []byte) ([]byte, map[string]string, error) {
	trailers := map[string]string{}
	var message []byte
	for offset := 0; offset < len(raw); {
		if len(raw)-offset < 5 {
			return nil, nil, fmt.Errorf("truncated grpc-web frame header at %d", offset)
		}
		flag := raw[offset]
		length := int(binary.BigEndian.Uint32(raw[offset+1 : offset+5]))
		offset += 5
		if length < 0 || offset+length > len(raw) {
			return nil, nil, fmt.Errorf("invalid grpc-web frame length %d", length)
		}
		payload := raw[offset : offset+length]
		offset += length
		if flag&0x80 != 0 {
			for key, value := range parseTrailerBlock(payload) {
				trailers[key] = value
			}
			continue
		}
		if flag != 0 {
			return nil, nil, fmt.Errorf("unsupported compressed grpc-web frame flag 0x%x", flag)
		}
		message = append(message, payload...)
	}
	if len(message) == 0 {
		return nil, trailers, ErrEmptyGrpcWebMessage
	}
	return message, trailers, nil
}

func parseTrailerBlock(raw []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\r\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	return out
}

type decodedCreditsConfig struct {
	Percent     float64
	PercentPath string
	StartAt     time.Time
	ResetAt     time.Time
}

func decodeCreditsConfigMessage(message []byte) (decodedCreditsConfig, error) {
	fields, err := parseProtoFields(message)
	if err != nil {
		return decodedCreditsConfig{}, err
	}
	root := firstField(fields, 1)
	if root == nil || len(root.Bytes) == 0 {
		return decodedCreditsConfig{}, errors.New("missing credits config field")
	}
	inner, err := parseProtoFields(root.Bytes)
	if err != nil {
		return decodedCreditsConfig{}, err
	}
	startAt := decodeTimestampField(firstField(inner, 4))
	resetAt := decodeTimestampField(firstField(inner, 5))
	percent, path, err := decodeUsagePercentCandidate(inner, "1")
	if err != nil {
		if err.Error() == "missing usage percent field" && (!startAt.IsZero() || !resetAt.IsZero()) {
			return decodedCreditsConfig{Percent: 0, PercentPath: "default_zero", StartAt: startAt, ResetAt: resetAt}, nil
		}
		return decodedCreditsConfig{}, err
	}

	return decodedCreditsConfig{Percent: percent, PercentPath: path, StartAt: startAt, ResetAt: resetAt}, nil
}

func decodeUsagePercentCandidate(fields []protoField, prefix string) (float64, string, error) {
	if field := firstField(fields, 1); field != nil && field.Wire == 5 && len(field.Bytes) == 4 {
		if value, ok := fixed32Percent(field); ok {
			return value, prefix + ".1", nil
		}
		return 0, "", fmt.Errorf("invalid usage percent candidate at %s.1", prefix)
	}
	candidates := make([]struct {
		path  string
		value float64
	}, 0)
	for _, field := range fields {
		if field.Number == 1 || field.Number == 4 || field.Number == 5 {
			continue
		}
		if field.Wire != 5 || len(field.Bytes) != 4 {
			continue
		}
		if value, ok := fixed32Percent(&field); ok {
			candidates = append(candidates, struct {
				path  string
				value float64
			}{path: fmt.Sprintf("%s.%d", prefix, field.Number), value: value})
		}
	}
	switch len(candidates) {
	case 0:
		return 0, "", errors.New("missing usage percent field")
	case 1:
		return candidates[0].value, candidates[0].path, nil
	default:
		paths := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			paths = append(paths, candidate.path)
		}
		return 0, "", fmt.Errorf("ambiguous usage percent fields: %s", strings.Join(paths, ","))
	}
}

func fixed32Percent(field *protoField) (float64, bool) {
	if field == nil || field.Wire != 5 || len(field.Bytes) != 4 {
		return 0, false
	}
	value := float64(math.Float32frombits(binary.LittleEndian.Uint32(field.Bytes)))
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 {
		return 0, false
	}
	return value, true
}

func decodeTimestampField(field *protoField) time.Time {
	if field == nil || len(field.Bytes) == 0 {
		return time.Time{}
	}
	fields, err := parseProtoFields(field.Bytes)
	if err != nil {
		return time.Time{}
	}
	seconds := firstField(fields, 1)
	if seconds == nil || seconds.Varint <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(seconds.Varint), 0).UTC()
}

func firstHeader(headers http.Header, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			return value
		}
	}
	return ""
}
