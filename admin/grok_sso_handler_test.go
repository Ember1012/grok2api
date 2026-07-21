package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/codex2api/security"
	"github.com/gin-gonic/gin"
)

func TestAddSSOAccountRejectsInvalidJSONWithoutTokenEcho(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const secretToken = "sso-secret-token"
	body := `{"token":"` + secretToken + `","proxy_url":"` + strings.Repeat("x", security.MaxProxyURLLength+1) + `"}`

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/grok-sso", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")

	(&Handler{}).AddSSOAccount(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if strings.Contains(recorder.Body.String(), secretToken) {
		t.Fatalf("response leaked token: %s", recorder.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := payload["error"]; !ok {
		t.Fatalf("response missing error field: %v", payload)
	}
}

func TestAddSSOAccountStreamRejectsInvalidInputWithoutTokenEcho(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const secretToken = "stream-secret-token"
	body := `{"token":"` + secretToken + `","proxy_url":"` + strings.Repeat("x", security.MaxProxyURLLength+1) + `"}`

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/grok-sso?stream=true", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")

	(&Handler{}).AddSSOAccount(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if strings.Contains(recorder.Body.String(), secretToken) {
		t.Fatalf("response leaked token: %s", recorder.Body.String())
	}
	if ct := recorder.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("unexpected SSE response for rejected input: %q", ct)
	}
}

func TestSendSSEJSONImportEventDoesNotContainToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	sendSSEJSON(ctx, importEvent{Type: "progress", Current: 1, Total: 2, Success: 1, Duplicate: 0, Failed: 0})

	got := recorder.Body.String()
	if !strings.Contains(got, `data: {`) {
		t.Fatalf("sse payload = %q, want data frame", got)
	}
	if strings.Contains(got, "token") {
		t.Fatalf("sse payload leaked token field: %s", got)
	}
}

// HAR-shaped body uses sso_tokens[] without legacy token; collectSSOBatchTokens
// must yield entries so AddSSOAccount does not short-circuit with 未找到有效的 SSO token.
func TestCollectSSOBatchTokensAcceptsHARShapeWithoutTokenField(t *testing.T) {
	t.Parallel()

	const fake = "test-sso-not-a-real-credential"
	// Mirrors frontend HAR: {"sso_tokens":["..."],"proxy_url":"","allow_duplicate":false}
	raw := `{"sso_tokens":["` + fake + `"],"proxy_url":"","allow_duplicate":false}`
	var req ssoBatchInput
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal HAR-shaped body: %v", err)
	}
	if req.Token != "" {
		t.Fatalf("legacy token field should be empty, got %q", req.Token)
	}
	got := collectSSOBatchTokens(req)
	if len(got) == 0 {
		t.Fatal("HAR-shaped sso_tokens produced empty list; would return 未找到有效的 SSO token")
	}
	if got[0] != fake {
		t.Fatalf("token[0] = %q, want %q", got[0], fake)
	}
}

// DTO contract: sso_tokens[] binds and passes empty-token check into later validation
// (invalid proxy). Failure mode "未找到有效的 SSO token" would mean the array was ignored.
func TestAddSSOAccountSSOTokensArrayEntersValidationPath(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const fakeToken = "test-sso-array-not-a-real-credential"
	body := `{"sso_tokens":["` + fakeToken + `"],"proxy_url":"` + strings.Repeat("x", security.MaxProxyURLLength+1) + `","allow_duplicate":false}`

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/grok-sso", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")

	(&Handler{}).AddSSOAccount(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), fakeToken) {
		t.Fatalf("response leaked token: %s", recorder.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	errMsg, _ := payload["error"].(string)
	if errMsg == "未找到有效的 SSO token" {
		t.Fatalf("sso_tokens[] was not collected; still at empty-token gate: %q", errMsg)
	}
	if errMsg != "代理URL无效" {
		t.Fatalf("error = %q, want 代理URL无效 (token bound, next validation)", errMsg)
	}
}

// DTO contract: legacy multi-line token still binds and enters the same validation path.
func TestAddSSOAccountMultilineTokenStillCompatible(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const line1 = "test-sso-multiline-a-not-real"
	const line2 = "test-sso-multiline-b-not-real"
	// JSON string with real newlines (multi-line paste compatibility).
	body := `{"token":"` + line1 + `\n` + line2 + `","proxy_url":"` + strings.Repeat("x", security.MaxProxyURLLength+1) + `","allow_duplicate":false}`

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/grok-sso", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")

	(&Handler{}).AddSSOAccount(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	resp := recorder.Body.String()
	if strings.Contains(resp, line1) || strings.Contains(resp, line2) {
		t.Fatalf("response leaked multiline token: %s", resp)
	}

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	errMsg, _ := payload["error"].(string)
	if errMsg == "未找到有效的 SSO token" {
		t.Fatalf("legacy multiline token was not collected: %q", errMsg)
	}
	if errMsg != "代理URL无效" {
		t.Fatalf("error = %q, want 代理URL无效 (token bound, next validation)", errMsg)
	}

	// Unit-level: multi-line token splits into two batch entries.
	var req ssoBatchInput
	if err := json.Unmarshal([]byte(`{"token":"`+line1+`\n`+line2+`"}`), &req); err != nil {
		t.Fatalf("unmarshal multiline token body: %v", err)
	}
	got := collectSSOBatchTokens(req)
	if len(got) != 2 {
		t.Fatalf("collectSSOBatchTokens len = %d, want 2; got %#v", len(got), got)
	}
	if got[0] != line1 || got[1] != line2 {
		t.Fatalf("tokens = %#v, want [%q %q]", got, line1, line2)
	}
}

// DTO contract: both sso_tokens and token missing/empty → 400, fixed message, no token echo.
func TestAddSSOAccountRejectsEmptyTokensWithoutLeak(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const probe = "test-sso-must-not-appear-in-response"

	cases := []struct {
		name string
		body string
	}{
		{
			name: "empty_sso_tokens_array",
			body: `{"sso_tokens":[],"proxy_url":"","allow_duplicate":false}`,
		},
		{
			name: "both_fields_absent",
			body: `{"proxy_url":"","allow_duplicate":false}`,
		},
		{
			name: "empty_token_string_no_array",
			body: `{"token":"","proxy_url":"","allow_duplicate":false}`,
		},
		{
			name: "whitespace_only_token",
			// probe appears only as whitespace-surrounded content that must be stripped to empty.
			body: `{"token":"   \n\t  ","sso_tokens":[],"proxy_url":"","allow_duplicate":false}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/grok-sso", strings.NewReader(tc.body))
			ctx.Request.Header.Set("Content-Type", "application/json")

			(&Handler{}).AddSSOAccount(ctx)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}

			var payload map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			errMsg, _ := payload["error"].(string)
			if errMsg != "未找到有效的 SSO token" {
				t.Fatalf("error = %q, want %q", errMsg, "未找到有效的 SSO token")
			}
			// Desensitization: response must not dump request field names or probe secrets.
			resp := recorder.Body.String()
			if strings.Contains(resp, "sso_tokens") {
				t.Fatalf("response should not echo request fields: %s", resp)
			}
			if strings.Contains(resp, probe) {
				t.Fatalf("response leaked probe token: %s", resp)
			}
		})
	}
}

// Both fields missing while a non-token decoy value is present: still 400, never echo the decoy.
func TestAddSSOAccountMissingBothTokensDoesNotEchoDecoy(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const decoy = "test-sso-decoy-not-a-real-credential"
	// Decoy only in an unknown field — must not be treated as token and must not be echoed.
	body := `{"not_a_token":"` + decoy + `","proxy_url":"","allow_duplicate":false}`

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/grok-sso", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")

	(&Handler{}).AddSSOAccount(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), decoy) {
		t.Fatalf("response leaked decoy token: %s", recorder.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got, _ := payload["error"].(string); got != "未找到有效的 SSO token" {
		t.Fatalf("error = %q, want %q", got, "未找到有效的 SSO token")
	}
}
