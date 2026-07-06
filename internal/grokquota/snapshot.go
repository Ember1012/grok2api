package grokquota

import (
	"encoding/json"
	"time"
)

const (
	CredentialsKeyUsageSnapshot     = "grok_usage_snapshot"
	CredentialsKeyHeaderObservation = "grok_usage_header_observer"
	CredentialsKeyBillingDiagnostic = "grok_usage_billing_diagnostic"

	SourceGrokBuildBilling = "grok_build_billing"
	SourceHeaderObserver   = "header_observer"

	StateObserved           = "observed"
	StateUnavailable        = "unavailable"
	StateUnauthorized       = "unauthorized"
	StateForbidden          = "forbidden"
	StateRateLimited        = "rate_limited"
	StateDecodeFailed       = "decode_failed"
	StateBillingUnavailable = "billing_unavailable"
)

type TimeWindow struct {
	StartAt string `json:"start_at,omitempty"`
	ResetAt string `json:"reset_at,omitempty"`
}

type RateLimitWindow struct {
	Limit     *int64 `json:"limit,omitempty"`
	Remaining *int64 `json:"remaining,omitempty"`
	ResetAt   string `json:"reset_at,omitempty"`
}

type ProtobufFieldShape struct {
	Path   string `json:"path"`
	Number uint64 `json:"number"`
	Wire   uint64 `json:"wire"`
	Length int    `json:"length,omitempty"`
}

type GrpcWebFrameShape struct {
	Flag      byte `json:"flag"`
	Length    int  `json:"length"`
	IsTrailer bool `json:"is_trailer"`
}

type DecodeDiagnostic struct {
	Stage               string               `json:"stage,omitempty"`
	UsagePercentPath    string               `json:"usage_percent_path,omitempty"`
	MessageSize         int                  `json:"message_size,omitempty"`
	MessageSHA256Prefix string               `json:"message_sha256_prefix,omitempty"`
	GrpcWebFrames       []GrpcWebFrameShape  `json:"grpc_web_frames,omitempty"`
	ProtobufFields      []ProtobufFieldShape `json:"protobuf_fields,omitempty"`
}

type UsageSnapshot struct {
	Source             string                     `json:"source"`
	State              string                     `json:"state"`
	Period             string                     `json:"period,omitempty"`
	UsedPercent        *float64                   `json:"used_percent,omitempty"`
	APIUsedPercent     *float64                   `json:"api_used_percent,omitempty"`
	Window             *TimeWindow                `json:"window,omitempty"`
	SubscriptionTier   string                     `json:"subscription_tier,omitempty"`
	EntitlementStatus  string                     `json:"entitlement_status,omitempty"`
	RateLimits         map[string]RateLimitWindow `json:"rate_limits,omitempty"`
	Headers            map[string]string          `json:"headers,omitempty"`
	DecodeDiagnostic   *DecodeDiagnostic          `json:"decode_diagnostic,omitempty"`
	UpstreamStatusCode int                        `json:"upstream_status_code,omitempty"`
	UpstreamRequestID  string                     `json:"upstream_request_id,omitempty"`
	FetchedAt          string                     `json:"fetched_at"`
	ErrorCode          string                     `json:"error_code,omitempty"`
	Error              string                     `json:"error,omitempty"`
}

func NewObservedBillingSnapshot(percent float64, startAt, resetAt time.Time, statusCode int, requestID string) UsageSnapshot {
	now := time.Now().UTC().Format(time.RFC3339)
	snapshot := UsageSnapshot{
		Source:             SourceGrokBuildBilling,
		State:              StateObserved,
		Period:             "weekly",
		UsedPercent:        &percent,
		APIUsedPercent:     &percent,
		UpstreamStatusCode: statusCode,
		UpstreamRequestID:  requestID,
		FetchedAt:          now,
	}
	if !startAt.IsZero() || !resetAt.IsZero() {
		snapshot.Window = &TimeWindow{}
		if !startAt.IsZero() {
			snapshot.Window.StartAt = startAt.UTC().Format(time.RFC3339)
		}
		if !resetAt.IsZero() {
			snapshot.Window.ResetAt = resetAt.UTC().Format(time.RFC3339)
		}
	}
	return snapshot
}

func NewObservedHeaderSnapshot(headers map[string]string, rateLimits map[string]RateLimitWindow, statusCode int, requestID string) UsageSnapshot {
	snapshot := UsageSnapshot{
		Source:             SourceHeaderObserver,
		State:              StateObserved,
		Period:             "weekly",
		SubscriptionTier:   firstMapValue(headers, "xai-subscription-tier", "x-subscription-tier"),
		EntitlementStatus:  firstMapValue(headers, "xai-entitlement-status", "x-entitlement-status"),
		RateLimits:         rateLimits,
		Headers:            headers,
		UpstreamStatusCode: statusCode,
		UpstreamRequestID:  requestID,
		FetchedAt:          time.Now().UTC().Format(time.RFC3339),
	}
	return snapshot
}

func NewErrorSnapshot(state, code, message string, statusCode int, requestID string) UsageSnapshot {
	return NewSourceErrorSnapshot(SourceGrokBuildBilling, state, code, message, statusCode, requestID)
}

func NewSourceErrorSnapshot(source, state, code, message string, statusCode int, requestID string) UsageSnapshot {
	return UsageSnapshot{
		Source:             source,
		State:              state,
		UpstreamStatusCode: statusCode,
		UpstreamRequestID:  requestID,
		FetchedAt:          time.Now().UTC().Format(time.RFC3339),
		ErrorCode:          code,
		Error:              message,
	}
}

func firstMapValue(values map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := values[key]; value != "" {
			return value
		}
	}
	return ""
}

func FromCredentialValue(value any) (*UsageSnapshot, bool) {
	if value == nil {
		return nil, false
	}
	var snapshot UsageSnapshot
	switch typed := value.(type) {
	case UsageSnapshot:
		snapshot = typed
	case *UsageSnapshot:
		if typed == nil {
			return nil, false
		}
		snapshot = *typed
	case map[string]any:
		data, err := json.Marshal(typed)
		if err != nil || json.Unmarshal(data, &snapshot) != nil {
			return nil, false
		}
	default:
		data, err := json.Marshal(typed)
		if err != nil || json.Unmarshal(data, &snapshot) != nil {
			return nil, false
		}
	}
	if snapshot.Source == "" && snapshot.State == "" {
		return nil, false
	}
	return &snapshot, true
}
