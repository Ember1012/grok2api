package grokquota

import "testing"

func TestClassifyXAIAuthFailure_BadCredentials(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       AuthFailureKind
	}{
		{
			name:       "exact code top level",
			statusCode: 403,
			body:       `{"code":"unauthenticated:bad-credentials","error":"The OAuth2 access token could not be validated."}`,
			want:       AuthFailureBadCredentials,
		},
		{
			name:       "nested error code",
			statusCode: 403,
			body:       `{"error":{"code":"unauthenticated:bad-credentials","message":"The OAuth2 access token could not be validated."}}`,
			want:       AuthFailureBadCredentials,
		},
		{
			name:       "message contains",
			statusCode: 403,
			body:       `{"error":"The OAuth2 access token could not be validated. Please re-authenticate."}`,
			want:       AuthFailureBadCredentials,
		},
		{
			name:       "status details nested",
			statusCode: 403,
			body:       `{"status_details":{"error":{"code":"unauthenticated:bad-credentials"}}}`,
			want:       AuthFailureBadCredentials,
		},
		{
			name:       "ordinary 403 entitlement",
			statusCode: 403,
			body:       `{"error":{"code":"forbidden","message":"insufficient_entitlement"}}`,
			want:       AuthFailureNone,
		},
		{
			name:       "401 unauthorized different",
			statusCode: 401,
			body:       `{"error":"invalid_token"}`,
			want:       AuthFailureNone,
		},
		{
			name:       "200 success",
			statusCode: 200,
			body:       `{"ok":true}`,
			want:       AuthFailureNone,
		},
		{
			name:       "empty body",
			statusCode: 403,
			body:       ``,
			want:       AuthFailureNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyXAIAuthFailure(tt.statusCode, []byte(tt.body))
			if got != tt.want {
				t.Errorf("ClassifyXAIAuthFailure() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsXAIInvalidAccessToken(t *testing.T) {
	if !IsXAIInvalidAccessToken(403, []byte(`{"code":"unauthenticated:bad-credentials"}`)) {
		t.Error("expected true for bad-credentials")
	}
	if IsXAIInvalidAccessToken(403, []byte(`{"code":"forbidden"}`)) {
		t.Error("expected false for ordinary forbidden")
	}
}