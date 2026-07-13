package grokquota

import "testing"

func TestIsXAISpendingLimit(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       bool
	}{
		{
			name:       "typical personal-team-blocked pending-limit",
			statusCode: 403,
			body:       `{"code":"personal-team-blocked:pending-limit","error":"You have run out of credits or need a Grok subscription. Add credits at https://grok.com/?_s=usage or upgrade at https://grok.com/supergrok"}`,
			want:       true,
		},
		{
			name:       "spending-limit code",
			statusCode: 403,
			body:       `{"code":"spending-limit","error":"quota exceeded"}`,
			want:       true,
		},
		{
			name:       "pending-limit case insensitive",
			statusCode: 403,
			body:       `{"code":"Personal-Team-Blocked:Pending-Limit"}`,
			want:       true,
		},
		{
			name:       "run out of credits only",
			statusCode: 403,
			body:       `{"error":"You have run out of credits"}`,
			want:       true,
		},
		{
			name:       "402 spending limit",
			statusCode: 402,
			body:       `{"code":"spending-limit"}`,
			want:       true,
		},
		{
			name:       "bad-credentials not spending limit",
			statusCode: 403,
			body:       `{"code":"unauthenticated:bad-credentials","error":"The OAuth2 access token could not be validated."}`,
			want:       false,
		},
		{
			name:       "ordinary forbidden",
			statusCode: 403,
			body:       `{"error":{"code":"forbidden","message":"insufficient_entitlement"}}`,
			want:       false,
		},
		{
			name:       "wrong status 401",
			statusCode: 401,
			body:       `{"code":"personal-team-blocked:pending-limit"}`,
			want:       false,
		},
		{
			name:       "200 success",
			statusCode: 200,
			body:       `{"code":"spending-limit"}`,
			want:       false,
		},
		{
			name:       "empty body",
			statusCode: 403,
			body:       ``,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsXAISpendingLimit(tt.statusCode, []byte(tt.body))
			if got != tt.want {
				t.Errorf("IsXAISpendingLimit() = %v, want %v", got, tt.want)
			}
		})
	}
}