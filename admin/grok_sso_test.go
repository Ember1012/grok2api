package admin

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNormalizeGrokSSOTokens(t *testing.T) {
	t.Parallel()

	protocol, err := NewGrokSSOProtocol(nil, GrokSSOConfig{MaxTokenBytes: 8, MaxTokens: 2})
	if err != nil {
		t.Fatalf("NewGrokSSOProtocol: %v", err)
	}

	tests := []struct {
		name    string
		raw     string
		want    []GrokSSOCookie
		wantErr string
	}{
		{
			name: "normalize and dedupe",
			raw: strings.Join([]string{
				"",
				"sso=token-1; Path=/; Secure",
				"token-1",
				"sso=token-1",
				"sso-rw=token-2; HttpOnly",
				"",
			}, "\n"),
			want: []GrokSSOCookie{{Name: "sso", Value: "token-1"}, {Name: "sso-rw", Value: "token-2"}},
		},
		{
			name:    "reject overlong token",
			raw:     "sso=toolong!!",
			wantErr: "exceeds 8 bytes",
		},
		{
			name:    "reject too many tokens",
			raw:     "a\nb\nc",
			wantErr: "too many SSO tokens, max 2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := protocol.NormalizeGrokSSOTokens(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeGrokSSOTokens: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("len(got) = %d, want %d: %#v", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("cookie[%d] = %#v, want %#v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestValidateGrokSSOURLAllowlist(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		fn      func(string) error
		wantErr string
	}{
		{name: "base allowlisted", fn: func(raw string) error { _, err := validateGrokSSOBaseURL(raw); return err }, raw: "https://auth.x.ai/v1"},
		{name: "request allowlisted", fn: func(raw string) error { u, _ := url.Parse(raw); return validateGrokSSORequestURL(u) }, raw: "https://api.x.ai/oauth2/token"},
		{name: "reject base userinfo", fn: func(raw string) error { _, err := validateGrokSSOBaseURL(raw); return err }, raw: "https://user@auth.x.ai/v1", wantErr: "userinfo"},
		{name: "reject request userinfo", fn: func(raw string) error { u, _ := url.Parse(raw); return validateGrokSSORequestURL(u) }, raw: "https://user@api.x.ai/oauth2/token", wantErr: "userinfo"},
		{name: "reject non-xai base", fn: func(raw string) error { _, err := validateGrokSSOBaseURL(raw); return err }, raw: "https://auth.example.com/v1", wantErr: "allowlisted"},
		{name: "reject non-xai request", fn: func(raw string) error { u, _ := url.Parse(raw); return validateGrokSSORequestURL(u) }, raw: "https://api.example.com/oauth2/token", wantErr: "allowlisted"},
		{name: "reject plain http", fn: func(raw string) error { _, err := validateGrokSSOBaseURL(raw); return err }, raw: "http://auth.x.ai/v1", wantErr: "https"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fn(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validate = %v", err)
			}
		})
	}
}

func TestAllowedRedirectURLRejectsCrossDomain(t *testing.T) {
	t.Parallel()

	prev, err := url.Parse("https://auth.x.ai/oauth2/device/verify/complete")
	if err != nil {
		t.Fatalf("url.Parse prev: %v", err)
	}
	next, err := url.Parse("https://login.evil.example/finish")
	if err != nil {
		t.Fatalf("url.Parse next: %v", err)
	}
	if err := allowedRedirectURL(prev, next); err == nil || (!strings.Contains(err.Error(), "allowlisted") && !strings.Contains(err.Error(), "cross-domain redirect refused")) {
		t.Fatalf("allowedRedirectURL error = %v, want rejection for non-allowlisted redirect", err)
	}
}

func TestPollTokenMapsIDTokenWithoutExposingIt(t *testing.T) {
	t.Parallel()

	protocol, err := NewGrokSSOProtocol(&http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.URL.Host; got != "auth.x.ai" {
			t.Fatalf("host = %s, want auth.x.ai", got)
		}
		if got := r.URL.Path; got != "/oauth2/token" {
			t.Fatalf("path = %s, want /oauth2/token", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"x-request-id": []string{"req-123"}},
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"access-value","refresh_token":"refresh-value","token_type":"bearer","expires_in":3600,"scope":"scope-value","id_token":"id-value"}`)),
			Request:    r,
		}, nil
	})}, GrokSSOConfig{AuthBaseURL: "https://auth.x.ai", ClientID: "client-test", Scope: "scope-test", HTTPTimeout: 0})
	if err != nil {
		t.Fatalf("NewGrokSSOProtocol: %v", err)
	}

	cred, gotErr := protocol.pollToken(context.Background(), GrokSSOCookie{Name: "sso", Value: "cookie-value"}, &grokSSODeviceCodeResponse{DeviceCode: "device-value", Interval: 1, ExpiresIn: 10})
	if gotErr != nil {
		t.Fatalf("pollToken error = %v", gotErr)
	}
	if cred == nil {
		t.Fatal("pollToken credential = nil")
	}
	if cred.IDToken != "id-value" {
		t.Fatalf("IDToken mapped = %q, want %q", cred.IDToken, "id-value")
	}
	if cred.AccessToken != "access-value" || cred.RefreshToken != "refresh-value" || cred.TokenType != "bearer" {
		t.Fatalf("unexpected credential metadata: access=%q refresh=%q type=%q", cred.AccessToken, cred.RefreshToken, cred.TokenType)
	}
	if cred.RawRequestID != "" {
		t.Fatalf("RawRequestID = %q, want empty", cred.RawRequestID)
	}
	if cred.ClientID == "" || cred.Scope == "" || cred.ExpiresAt.IsZero() {
		t.Fatalf("credential missing required metadata: client_id=%q scope=%q expires_at_zero=%v", cred.ClientID, cred.Scope, cred.ExpiresAt.IsZero())
	}
}
