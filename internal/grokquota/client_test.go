package grokquota

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeCreditsConfigFromGrpcWebCapture(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString("AAAAAFIKUA0AAHRCEgAaACILCLi3kNIGENiUljQqCwi4rLXSBhDYlJY0OgcIARUAAHRCQhwIAhILCLi3kNIGENiUljQaCwi4rLXSBhDYlJY0WAFiAGgBgAAAAA9ncnBjLXN0YXR1czowDQo=")
	if err != nil {
		t.Fatalf("decode capture: %v", err)
	}
	message, trailers, err := decodeUnaryGrpcWeb(raw)
	if err != nil {
		t.Fatalf("decode grpc-web: %v", err)
	}
	if trailers["grpc-status"] != "0" {
		t.Fatalf("grpc-status = %q, want 0", trailers["grpc-status"])
	}
	decoded, err := decodeCreditsConfigMessage(message)
	if err != nil {
		t.Fatalf("decode credits config: %v", err)
	}
	if decoded.Percent != 61.0 {
		t.Fatalf("percent = %v, want 61", decoded.Percent)
	}
	if got := decoded.StartAt.Format("2006-01-02T15:04:05Z"); got != "2026-06-30T19:40:40Z" {
		t.Fatalf("start = %s", got)
	}
	if got := decoded.ResetAt.Format("2006-01-02T15:04:05Z"); got != "2026-07-07T19:40:40Z" {
		t.Fatalf("reset = %s", got)
	}
}

func TestDecodeUnaryGrpcWebRejectsEmptyMessage(t *testing.T) {
	_, _, err := decodeUnaryGrpcWeb([]byte{0x80, 0, 0, 0, 15, 'g', 'r', 'p', 'c', '-', 's', 't', 'a', 't', 'u', 's', ':', '0', '\r', '\n'})
	if err == nil {
		t.Fatal("expected empty message error")
	}
}

func TestFetchCreditsConfigProjectsBillingWeeklyUsage(t *testing.T) {
	capture, err := base64.StdEncoding.DecodeString("AAAAAFIKUA0AAHRCEgAaACILCLi3kNIGENiUljQqCwi4rLXSBhDYlJY0OgcIARUAAHRCQhwIAhILCLi3kNIGENiUljQaCwi4rLXSBhDYlJY0WAFiAGgBgAAAAA9ncnBjLXN0YXR1czowDQo=")
	if err != nil {
		t.Fatalf("decode capture: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != billingPath {
			t.Fatalf("path = %q, want %s", r.URL.Path, billingPath)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if string(body) != string(emptyGrpcWebRequest) {
			t.Fatalf("body = %v, want empty grpc-web request", body)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("authorization = %q", got)
		}
		w.Header().Set("xai-request-id", "billing-req")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(capture)
	}))
	defer server.Close()

	result, err := (Client{HTTPClient: server.Client(), BillingBaseURL: server.URL, Token: "test-token"}).FetchCreditsConfig(context.Background())
	if err != nil {
		t.Fatalf("FetchCreditsConfig: %v", err)
	}
	if result.Snapshot.Source != SourceGrokBuildBilling || result.Snapshot.State != StateObserved {
		t.Fatalf("snapshot = %#v", result.Snapshot)
	}
	if result.Snapshot.UsedPercent == nil || *result.Snapshot.UsedPercent != 61 {
		t.Fatalf("used percent = %v, want 61", result.Snapshot.UsedPercent)
	}
	if result.Snapshot.APIUsedPercent == nil || *result.Snapshot.APIUsedPercent != 61 {
		t.Fatalf("api used percent = %v, want 61", result.Snapshot.APIUsedPercent)
	}
}

func TestFetchCreditsConfigEmptyMessageIsBillingUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte{0x80, 0, 0, 0, 15, 'g', 'r', 'p', 'c', '-', 's', 't', 'a', 't', 'u', 's', ':', '0', '\r', '\n'})
	}))
	defer server.Close()

	result, err := (Client{HTTPClient: server.Client(), BillingBaseURL: server.URL, Token: "test-token"}).FetchCreditsConfig(context.Background())
	if err == nil {
		t.Fatal("expected empty message error")
	}
	if result.Snapshot.State != StateBillingUnavailable || result.Snapshot.ErrorCode != "empty_grpc_web_message" {
		t.Fatalf("snapshot = %#v", result.Snapshot)
	}
	if result.Snapshot.UsedPercent != nil || result.Snapshot.APIUsedPercent != nil {
		t.Fatalf("empty billing response must not project usage: %#v", result.Snapshot)
	}
}

func TestDecodeCreditsConfigMessageRejectsMissingUsagePercentField(t *testing.T) {
	message := protoMessage(1, protoMessage(4, protoMessage(1, []byte{1})))
	_, err := decodeCreditsConfigMessage(message)
	if err == nil || err.Error() != "missing usage percent field" {
		t.Fatalf("err = %v, want missing usage percent field", err)
	}
}

func TestDecodeCreditsConfigMessageFindsAllowedAlternatePercentField(t *testing.T) {
	message := protoMessage(1, protoFixed32(2, 63))
	decoded, err := decodeCreditsConfigMessage(message)
	if err != nil {
		t.Fatalf("decodeCreditsConfigMessage: %v", err)
	}
	if decoded.Percent != 63 || decoded.PercentPath != "1.2" {
		t.Fatalf("decoded = %#v, want percent 63 at 1.2", decoded)
	}
}

func TestDecodeCreditsConfigMessageRejectsAmbiguousPercentFields(t *testing.T) {
	message := protoMessage(1, append(protoFixed32(2, 63), protoFixed32(3, 64)...))
	_, err := decodeCreditsConfigMessage(message)
	if err == nil || !strings.Contains(err.Error(), "ambiguous usage percent fields") {
		t.Fatalf("err = %v, want ambiguous usage percent fields", err)
	}
}

func TestDecodeCreditsConfigMessageDoesNotTreatTimestampOrVarintAsPercent(t *testing.T) {
	message := protoMessage(1, protoVarint(2, 63))
	_, err := decodeCreditsConfigMessage(message)
	if err == nil || err.Error() != "missing usage percent field" {
		t.Fatalf("err = %v, want missing usage percent field", err)
	}
}

func TestDecodeCreditsConfigMessageTreatsMissingPercentWithResetAsZero(t *testing.T) {
	resetSeconds := uint64(1893456000)
	message := protoMessage(1, protoMessage(5, protoVarint(1, resetSeconds)))
	decoded, err := decodeCreditsConfigMessage(message)
	if err != nil {
		t.Fatalf("decodeCreditsConfigMessage: %v", err)
	}
	if decoded.Percent != 0 || decoded.PercentPath != "default_zero" {
		t.Fatalf("decoded = %#v, want default zero percent", decoded)
	}
	if got := decoded.ResetAt.Unix(); got != int64(resetSeconds) {
		t.Fatalf("reset = %d, want %d", got, resetSeconds)
	}
}

func TestFetchCreditsConfigMissingPercentWithResetProjectsZeroUsage(t *testing.T) {
	resetSeconds := uint64(1893456000)
	message := protoMessage(1, protoMessage(5, protoVarint(1, resetSeconds)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("xai-request-id", "zero-usage")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(grpcWebUnary(message))
	}))
	defer server.Close()

	result, err := (Client{HTTPClient: server.Client(), BillingBaseURL: server.URL, Token: "test-token"}).FetchCreditsConfig(context.Background())
	if err != nil {
		t.Fatalf("FetchCreditsConfig: %v", err)
	}
	if result.Snapshot.State != StateObserved || result.Snapshot.Source != SourceGrokBuildBilling {
		t.Fatalf("snapshot = %#v", result.Snapshot)
	}
	if result.Snapshot.UsedPercent == nil || *result.Snapshot.UsedPercent != 0 {
		t.Fatalf("used percent = %v, want 0", result.Snapshot.UsedPercent)
	}
	if result.Snapshot.Window == nil || result.Snapshot.Window.ResetAt == "" {
		t.Fatalf("missing reset window: %#v", result.Snapshot.Window)
	}
	if result.Snapshot.DecodeDiagnostic == nil || result.Snapshot.DecodeDiagnostic.Stage != "percent_default_zero" {
		t.Fatalf("diagnostic = %#v, want percent_default_zero", result.Snapshot.DecodeDiagnostic)
	}
}

func TestFetchCreditsConfigMissingUsagePercentIncludesShapeDiagnostic(t *testing.T) {
	message := protoMessage(1, protoVarint(2, 63))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("xai-request-id", "shape-req")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(grpcWebUnary(message))
	}))
	defer server.Close()

	result, err := (Client{HTTPClient: server.Client(), BillingBaseURL: server.URL, Token: "test-token"}).FetchCreditsConfig(context.Background())
	if err == nil {
		t.Fatal("expected missing usage percent error")
	}
	if result.Snapshot.State != StateDecodeFailed || result.Snapshot.ErrorCode != "protobuf_decode_failed" {
		t.Fatalf("snapshot = %#v", result.Snapshot)
	}
	if result.Snapshot.DecodeDiagnostic == nil || result.Snapshot.DecodeDiagnostic.MessageSize == 0 || len(result.Snapshot.DecodeDiagnostic.ProtobufFields) == 0 {
		t.Fatalf("missing decode diagnostic: %#v", result.Snapshot.DecodeDiagnostic)
	}
	if result.Snapshot.UsedPercent != nil || result.Snapshot.APIUsedPercent != nil {
		t.Fatalf("decode failed response must not project usage: %#v", result.Snapshot)
	}
}

func TestBillingBaseURLIgnoresAPIBaseURL(t *testing.T) {
	if got := billingBaseURL("http://api.x.ai/v1"); got != "https://grok.com" {
		t.Fatalf("billingBaseURL(api.x.ai) = %q, want https://grok.com", got)
	}
	if got := billingBaseURL("https://grok.com"); got != "https://grok.com" {
		t.Fatalf("billingBaseURL(grok.com) = %q, want https://grok.com", got)
	}
}

func TestFetchQuotaSnapshotUsesResponsesProbeAndHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %q, want /v1/responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("authorization = %q", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload["input"] != "." || payload["store"] != false || payload["max_output_tokens"].(float64) != 1 {
			t.Fatalf("unexpected payload: %#v", payload)
		}
		w.Header().Set("x-request-id", "req-1")
		w.Header().Set("x-ratelimit-limit-requests", "100")
		w.Header().Set("x-ratelimit-remaining-requests", "25")
		w.Header().Set("x-ratelimit-reset-requests", "1893456000")
		w.Header().Set("xai-subscription-tier", "supergrok")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"resp"}`))
	}))
	defer server.Close()

	result, err := (Client{HTTPClient: server.Client(), BaseURL: server.URL, Token: "test-token"}).FetchQuotaSnapshot(context.Background())
	if err != nil {
		t.Fatalf("FetchQuotaSnapshot: %v", err)
	}
	if result.Snapshot.Source != SourceHeaderObserver || result.Snapshot.State != StateObserved {
		t.Fatalf("snapshot = %#v", result.Snapshot)
	}
	if result.Snapshot.UpstreamRequestID != "req-1" {
		t.Fatalf("request id = %q", result.Snapshot.UpstreamRequestID)
	}
	if result.Snapshot.SubscriptionTier != "supergrok" {
		t.Fatalf("subscription tier = %q", result.Snapshot.SubscriptionTier)
	}
	if result.Snapshot.APIUsedPercent != nil || result.Snapshot.UsedPercent != nil {
		t.Fatalf("header observer must not project weekly usage: %#v", result.Snapshot)
	}
	if _, ok := result.Snapshot.Headers["authorization"]; ok {
		t.Fatal("authorization header must not be persisted")
	}
}

func TestFetchQuotaSnapshotNoHeadersDoesNotFailRefresh(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"resp"}`))
	}))
	defer server.Close()

	result, err := (Client{HTTPClient: server.Client(), BaseURL: server.URL, Token: "test-token"}).FetchQuotaSnapshot(context.Background())
	if err != nil {
		t.Fatalf("FetchQuotaSnapshot no headers should not return error: %v", err)
	}
	if result.Snapshot.State != StateUnavailable || result.Snapshot.ErrorCode != "no_quota_headers" {
		t.Fatalf("snapshot = %#v", result.Snapshot)
	}
}

func TestFetchQuotaSnapshotClassifiesUpstreamStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("xai-request-id", "xai-429")
		w.Header().Set("retry-after", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`rate limited`))
	}))
	defer server.Close()

	result, err := (Client{HTTPClient: server.Client(), BaseURL: server.URL, Token: "test-token"}).FetchQuotaSnapshot(context.Background())
	if err == nil {
		t.Fatal("expected upstream status error")
	}
	if result.Snapshot.State != StateRateLimited {
		t.Fatalf("state = %q", result.Snapshot.State)
	}
	if result.Snapshot.UpstreamRequestID != "xai-429" {
		t.Fatalf("request id = %q", result.Snapshot.UpstreamRequestID)
	}
	if result.Snapshot.Headers["retry-after"] != "30" {
		t.Fatalf("retry-after = %q", result.Snapshot.Headers["retry-after"])
	}
}

func grpcWebUnary(message []byte) []byte {
	out := make([]byte, 5+len(message))
	binary.BigEndian.PutUint32(out[1:5], uint32(len(message)))
	copy(out[5:], message)
	trailers := []byte("grpc-status:0\r\n")
	frame := make([]byte, 5+len(trailers))
	frame[0] = 0x80
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(trailers)))
	copy(frame[5:], trailers)
	return append(out, frame...)
}

func protoMessage(number uint64, payload []byte) []byte {
	out := protoVarintValue(number<<3 | 2)
	out = append(out, protoVarintValue(uint64(len(payload)))...)
	out = append(out, payload...)
	return out
}

func protoFixed32(number uint64, value float32) []byte {
	out := protoVarintValue(number<<3 | 5)
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, math.Float32bits(value))
	return append(out, buf...)
}

func protoVarint(number uint64, value uint64) []byte {
	out := protoVarintValue(number << 3)
	return append(out, protoVarintValue(value)...)
}

func protoVarintValue(value uint64) []byte {
	out := make([]byte, 0)
	for value >= 0x80 {
		out = append(out, byte(value)|0x80)
		value >>= 7
	}
	return append(out, byte(value))
}
