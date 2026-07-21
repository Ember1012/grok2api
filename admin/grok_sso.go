package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"strings"
	"sync"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/security"
	"github.com/gin-gonic/gin"
)

const (
	grokSSODefaultAuthBaseURL  = "https://auth.x.ai"
	grokSSODefaultSessionPath  = "/api/auth/session"
	grokSSODefaultDevicePath   = "/oauth2/device/code"
	grokSSODefaultVerifyPath   = "/oauth2/device/verify/complete"
	grokSSODefaultApprovePath  = "/oauth2/device/verify/approve"
	grokSSODefaultTokenPath    = "/oauth2/token"
	grokSSOMaxRedirects        = 8
	grokSSOMaxResponseBytes    = 2 << 20
	grokSSOMaxTokenBytes       = 16 << 10
	grokSSOMaxTokens           = 10000
	grokSSODefaultPollInterval = 5 * time.Second
)

var (
	ErrGrokSSOInvalid  = errors.New("grok sso authorization invalid")
	ErrGrokSSODenied   = errors.New("grok sso authorization denied")
	ErrGrokSSOExpired  = errors.New("grok sso authorization expired")
	ErrGrokSSOTemp     = errors.New("grok sso temporary upstream failure")
	ErrGrokSSOProtocol = errors.New("grok sso protocol error")
)

type GrokSSOErrorKind string

const (
	GrokSSOErrorKindInvalid   GrokSSOErrorKind = "invalid"
	GrokSSOErrorKindDenied    GrokSSOErrorKind = "denied"
	GrokSSOErrorKindExpired   GrokSSOErrorKind = "expired"
	GrokSSOErrorKindTemporary GrokSSOErrorKind = "temporary"
	GrokSSOErrorKindProtocol  GrokSSOErrorKind = "protocol"
)

type GrokSSOError struct {
	Kind       GrokSSOErrorKind `json:"kind"`
	Step       string           `json:"step,omitempty"`
	StatusCode int              `json:"status_code,omitempty"`
	RequestID  string           `json:"request_id,omitempty"`
	Message    string           `json:"message,omitempty"`
	Err        error            `json:"-"`
}

func (e *GrokSSOError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return string(e.Kind)
}

func (e *GrokSSOError) Unwrap() error { return e.Err }

func (e *GrokSSOError) Is(target error) bool {
	switch target {
	case ErrGrokSSOInvalid:
		return e != nil && e.Kind == GrokSSOErrorKindInvalid
	case ErrGrokSSODenied:
		return e != nil && e.Kind == GrokSSOErrorKindDenied
	case ErrGrokSSOExpired:
		return e != nil && e.Kind == GrokSSOErrorKindExpired
	case ErrGrokSSOTemp:
		return e != nil && e.Kind == GrokSSOErrorKindTemporary
	case ErrGrokSSOProtocol:
		return e != nil && e.Kind == GrokSSOErrorKindProtocol
	default:
		return false
	}
}

type GrokSSOCookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type GrokOAuthCredential struct {
	AccessToken         string    `json:"access_token"`
	RefreshToken        string    `json:"refresh_token,omitempty"`
	TokenType           string    `json:"token_type,omitempty"`
	ExpiresAt           time.Time `json:"expires_at,omitempty"`
	ClientID            string    `json:"client_id,omitempty"`
	Scope               string    `json:"scope,omitempty"`
	Email               string    `json:"email,omitempty"`
	SubscriptionTier    string    `json:"subscription_tier,omitempty"`
	EntitlementStatus   string    `json:"entitlement_status,omitempty"`
	RawRequestID        string    `json:"request_id,omitempty"`
	IDToken             string    `json:"-"`
	RawUpstreamResponse string    `json:"-"`
}

type GrokSSOFailure struct {
	Index      int              `json:"index"`
	CookieName string           `json:"cookie_name,omitempty"`
	Kind       GrokSSOErrorKind `json:"kind"`
	Step       string           `json:"step,omitempty"`
	RequestID  string           `json:"request_id,omitempty"`
	Message    string           `json:"message,omitempty"`
}

type GrokSSOExchangeResult struct {
	Accepted     int                   `json:"accepted"`
	Succeeded    int                   `json:"succeeded"`
	Deduplicated int                   `json:"deduplicated"`
	Credentials  []GrokOAuthCredential `json:"credentials,omitempty"`
	Failures     []GrokSSOFailure      `json:"failures,omitempty"`
}

type GrokSSOConfig struct {
	AuthBaseURL        string
	SessionCheckPath   string
	DeviceCodePath     string
	VerifyCompletePath string
	VerifyApprovePath  string
	TokenPath          string
	ClientID           string
	Scope              string
	MaxRedirects       int
	MaxResponseBytes   int64
	MaxTokens          int
	MaxTokenBytes      int
	PollInterval       time.Duration
	HTTPTimeout        time.Duration
}

type GrokSSOProtocol struct {
	client        *http.Client
	cfg           GrokSSOConfig
	defaultHeader map[string]string
	mu            sync.Mutex
}

type grokSSODeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type grokSSOTokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int    `json:"expires_in"`
	Scope            string `json:"scope"`
	IDToken          string `json:"id_token"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

type grokSSOJSONError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	Message          string `json:"message"`
	Detail           string `json:"detail"`
}

type grokHTTPResult struct {
	URL       *neturl.URL
	Status    int
	Header    http.Header
	Body      []byte
	RequestID string
}

func NewGrokSSOProtocol(client *http.Client, cfg GrokSSOConfig) (*GrokSSOProtocol, error) {
	baseURL := strings.TrimSpace(cfg.AuthBaseURL)
	if baseURL == "" {
		baseURL = grokSSODefaultAuthBaseURL
	}
	base, err := validateGrokSSOBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	cfg.AuthBaseURL = base.String()
	cfg.SessionCheckPath = defaultPath(cfg.SessionCheckPath, grokSSODefaultSessionPath)
	cfg.DeviceCodePath = defaultPath(cfg.DeviceCodePath, grokSSODefaultDevicePath)
	cfg.VerifyCompletePath = defaultPath(cfg.VerifyCompletePath, grokSSODefaultVerifyPath)
	cfg.VerifyApprovePath = defaultPath(cfg.VerifyApprovePath, grokSSODefaultApprovePath)
	cfg.TokenPath = defaultPath(cfg.TokenPath, grokSSODefaultTokenPath)
	cfg.ClientID = defaultPath(cfg.ClientID, oauthClientID)
	cfg.Scope = defaultPath(cfg.Scope, oauthDefaultScopes)
	cfg.MaxRedirects = defaultInt(cfg.MaxRedirects, grokSSOMaxRedirects)
	cfg.MaxResponseBytes = defaultInt64(cfg.MaxResponseBytes, grokSSOMaxResponseBytes)
	cfg.MaxTokens = defaultInt(cfg.MaxTokens, grokSSOMaxTokens)
	cfg.MaxTokenBytes = defaultInt(cfg.MaxTokenBytes, grokSSOMaxTokenBytes)
	cfg.PollInterval = defaultDuration(cfg.PollInterval, grokSSODefaultPollInterval)
	cfg.HTTPTimeout = defaultDuration(cfg.HTTPTimeout, 30*time.Second)
	if client == nil {
		client = &http.Client{Timeout: cfg.HTTPTimeout}
	}
	return &GrokSSOProtocol{client: client, cfg: cfg, defaultHeader: map[string]string{}}, nil
}

func defaultPath(v, fallback string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return fallback
	}
	if !strings.HasPrefix(v, "/") {
		return "/" + v
	}
	return v
}

func defaultInt(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

func defaultInt64(v, fallback int64) int64 {
	if v <= 0 {
		return fallback
	}
	return v
}

func defaultDuration(v, fallback time.Duration) time.Duration {
	if v <= 0 {
		return fallback
	}
	return v
}

func validateGrokSSOBaseURL(raw string) (*neturl.URL, error) {
	parsed, err := neturl.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid xAI base URL: %w", err)
	}
	if parsed.Scheme != "https" {
		return nil, fmt.Errorf("xAI base URL must use https")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("xAI base URL must not contain userinfo")
	}
	if !isAllowedXAIHost(parsed.Hostname()) {
		return nil, fmt.Errorf("xAI base URL host is not allowlisted")
	}
	parsed.Fragment = ""
	parsed.RawQuery = ""
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed, nil
}

func isAllowedXAIHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	return host == "x.ai" || strings.HasSuffix(host, ".x.ai")
}

func allowedRedirectURL(prev *neturl.URL, next *neturl.URL) error {
	if next == nil {
		return fmt.Errorf("empty redirect URL")
	}
	if next.Scheme != "https" {
		return fmt.Errorf("redirect must use https")
	}
	if next.User != nil {
		return fmt.Errorf("redirect must not contain userinfo")
	}
	if !isAllowedXAIHost(next.Hostname()) {
		return fmt.Errorf("redirect host is not allowlisted")
	}
	if prev != nil {
		prevHost := strings.ToLower(prev.Hostname())
		nextHost := strings.ToLower(next.Hostname())
		if prevHost != "" && prevHost != nextHost && !sameXAIParentDomain(prevHost, nextHost) {
			return fmt.Errorf("cross-domain redirect refused")
		}
	}
	return nil
}

func sameXAIParentDomain(a, b string) bool {
	if a == b {
		return true
	}
	return isAllowedXAIHost(a) && isAllowedXAIHost(b)
}

func NormalizeGrokSSOTokens(raw string) ([]GrokSSOCookie, error) {
	return defaultGrokSSOProtocol().NormalizeGrokSSOTokens(raw)
}

func defaultGrokSSOProtocol() *GrokSSOProtocol {
	p, _ := NewGrokSSOProtocol(nil, GrokSSOConfig{})
	return p
}

func (p *GrokSSOProtocol) NormalizeGrokSSOTokens(raw string) ([]GrokSSOCookie, error) {
	if p == nil {
		return nil, ErrGrokSSOProtocol
	}
	lines := strings.Split(raw, "\n")
	if len(lines) == 0 {
		return nil, nil
	}
	out := make([]GrokSSOCookie, 0, len(lines))
	dedup := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		cookie, ok, err := normalizeGrokSSOCookieLine(line)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if len(cookie.Value) > p.cfg.MaxTokenBytes {
			return nil, fmt.Errorf("SSO token exceeds %d bytes", p.cfg.MaxTokenBytes)
		}
		key := cookie.Name + "=" + cookie.Value
		if _, exists := dedup[key]; exists {
			continue
		}
		dedup[key] = struct{}{}
		out = append(out, cookie)
		if len(out) > p.cfg.MaxTokens {
			return nil, fmt.Errorf("too many SSO tokens, max %d", p.cfg.MaxTokens)
		}
	}
	return out, nil
}

func normalizeGrokSSOCookieLine(line string) (GrokSSOCookie, bool, error) {
	line = strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', '\x00':
			return -1
		default:
			return r
		}
	}, line)
	line = strings.TrimSpace(line)
	if line == "" {
		return GrokSSOCookie{}, false, nil
	}
	if strings.Contains(line, ";") {
		first := strings.TrimSpace(strings.SplitN(line, ";", 2)[0])
		if c, ok := parseGrokSSOCookiePair(first); ok {
			return c, true, nil
		}
		if c, ok := parseGrokSSOCookiePair(strings.TrimSpace(line)); ok {
			return c, true, nil
		}
	}
	if c, ok := parseGrokSSOCookiePair(line); ok {
		return c, true, nil
	}
	if strings.Contains(line, "=") {
		return GrokSSOCookie{}, false, nil
	}
	return GrokSSOCookie{Name: "sso", Value: line}, true, nil
}

func parseGrokSSOCookiePair(s string) (GrokSSOCookie, bool) {
	s = strings.TrimSpace(s)
	name, value, ok := strings.Cut(s, "=")
	if !ok {
		return GrokSSOCookie{}, false
	}
	name = strings.TrimSpace(name)
	value = strings.TrimSpace(value)
	if name != "sso" && name != "sso-rw" {
		return GrokSSOCookie{}, false
	}
	if value == "" {
		return GrokSSOCookie{}, false
	}
	return GrokSSOCookie{Name: name, Value: value}, true
}

func (p *GrokSSOProtocol) Exchange(ctx context.Context, raw string) (*GrokSSOExchangeResult, error) {
	cookies, err := p.NormalizeGrokSSOTokens(raw)
	if err != nil {
		return nil, err
	}
	result := &GrokSSOExchangeResult{
		Accepted: len(cookies),
	}
	if len(cookies) == 0 {
		return result, nil
	}
	for i, cookie := range cookies {
		cred, flowErr := p.ExchangeOne(ctx, cookie)
		if flowErr != nil {
			result.Failures = append(result.Failures, GrokSSOFailure{
				Index:      i,
				CookieName: cookie.Name,
				Kind:       flowErr.Kind,
				Step:       flowErr.Step,
				RequestID:  flowErr.RequestID,
				Message:    flowErr.Error(),
			})
			continue
		}
		result.Succeeded++
		result.Credentials = append(result.Credentials, *cred)
	}
	return result, nil
}

func (p *GrokSSOProtocol) ExchangeOne(ctx context.Context, cookie GrokSSOCookie) (*GrokOAuthCredential, *GrokSSOError) {
	if p == nil {
		return nil, &GrokSSOError{Kind: GrokSSOErrorKindProtocol, Message: "protocol not initialized", Err: ErrGrokSSOProtocol}
	}
	if err := p.checkSession(ctx, cookie); err != nil {
		return nil, err
	}
	dc, err := p.requestDeviceCode(ctx, cookie)
	if err != nil {
		return nil, err
	}
	if err := p.verifyDevice(ctx, cookie, dc); err != nil {
		return nil, err
	}
	if err := p.approveDevice(ctx, cookie, dc); err != nil {
		return nil, err
	}
	return p.pollToken(ctx, cookie, dc)
}

func (p *GrokSSOProtocol) checkSession(ctx context.Context, cookie GrokSSOCookie) *GrokSSOError {
	resp, err := p.doJSONRequest(ctx, http.MethodGet, p.joinAuthPath(p.cfg.SessionCheckPath), nil, cookie, nil)
	if err != nil {
		return err
	}
	if resp.Status >= 200 && resp.Status < 300 {
		var probe struct {
			Active        *bool `json:"active"`
			Authenticated *bool `json:"authenticated"`
			LoggedIn      *bool `json:"logged_in"`
		}
		if len(resp.Body) > 0 && json.Unmarshal(resp.Body, &probe) == nil {
			if probe.Active != nil && !*probe.Active {
				return p.newKindError(GrokSSOErrorKindInvalid, "session-check", resp.Status, resp.RequestID, "SSO session is not active", nil)
			}
			if probe.Authenticated != nil && !*probe.Authenticated {
				return p.newKindError(GrokSSOErrorKindInvalid, "session-check", resp.Status, resp.RequestID, "SSO session is not authenticated", nil)
			}
			if probe.LoggedIn != nil && !*probe.LoggedIn {
				return p.newKindError(GrokSSOErrorKindInvalid, "session-check", resp.Status, resp.RequestID, "SSO session is not logged in", nil)
			}
		}
		return nil
	}
	return classifyGrokSSOResponse("session-check", resp, nil)
}

func (p *GrokSSOProtocol) requestDeviceCode(ctx context.Context, cookie GrokSSOCookie) (*grokSSODeviceCodeResponse, *GrokSSOError) {
	form := neturl.Values{}
	form.Set("client_id", p.cfg.ClientID)
	form.Set("scope", p.cfg.Scope)
	resp, err := p.doJSONRequest(ctx, http.MethodPost, p.joinAuthPath(p.cfg.DeviceCodePath), strings.NewReader(form.Encode()), cookie, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if err != nil {
		return nil, err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return nil, classifyGrokSSOResponse("device-code", resp, nil)
	}
	var out grokSSODeviceCodeResponse
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, p.newKindError(GrokSSOErrorKindProtocol, "device-code", resp.Status, resp.RequestID, "failed to decode device code response", err)
	}
	if strings.TrimSpace(out.DeviceCode) == "" {
		return nil, p.newKindError(GrokSSOErrorKindProtocol, "device-code", resp.Status, resp.RequestID, "device code response missing device_code", nil)
	}
	if out.Interval <= 0 {
		out.Interval = int(p.cfg.PollInterval / time.Second)
		if out.Interval <= 0 {
			out.Interval = 5
		}
	}
	return &out, nil
}

func (p *GrokSSOProtocol) verifyDevice(ctx context.Context, cookie GrokSSOCookie, dc *grokSSODeviceCodeResponse) *GrokSSOError {
	form := neturl.Values{}
	form.Set("client_id", p.cfg.ClientID)
	form.Set("device_code", dc.DeviceCode)
	form.Set("user_code", dc.UserCode)
	form.Set("verification_uri", dc.VerificationURI)
	form.Set("verification_uri_complete", dc.VerificationURIComplete)
	resp, err := p.doJSONRequest(ctx, http.MethodPost, p.joinAuthPath(p.cfg.VerifyCompletePath), strings.NewReader(form.Encode()), cookie, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if err != nil {
		return err
	}
	if resp.Status >= 200 && resp.Status < 300 {
		return nil
	}
	return classifyGrokSSOResponse("verify-complete", resp, nil)
}

func (p *GrokSSOProtocol) approveDevice(ctx context.Context, cookie GrokSSOCookie, dc *grokSSODeviceCodeResponse) *GrokSSOError {
	form := neturl.Values{}
	form.Set("client_id", p.cfg.ClientID)
	form.Set("device_code", dc.DeviceCode)
	form.Set("user_code", dc.UserCode)
	resp, err := p.doJSONRequest(ctx, http.MethodPost, p.joinAuthPath(p.cfg.VerifyApprovePath), strings.NewReader(form.Encode()), cookie, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if err != nil {
		return err
	}
	if resp.Status >= 200 && resp.Status < 300 {
		return nil
	}
	return classifyGrokSSOResponse("verify-approve", resp, nil)
}

func (p *GrokSSOProtocol) pollToken(ctx context.Context, cookie GrokSSOCookie, dc *grokSSODeviceCodeResponse) (*GrokOAuthCredential, *GrokSSOError) {
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	if dc.ExpiresIn <= 0 {
		deadline = time.Now().Add(10 * time.Minute)
	}
	interval := time.Duration(dc.Interval) * time.Second
	if interval <= 0 {
		interval = p.cfg.PollInterval
	}
	for {
		if ctx.Err() != nil {
			return nil, p.newKindError(GrokSSOErrorKindTemporary, "token-poll", 0, "", "poll cancelled", ctx.Err())
		}
		if time.Now().After(deadline) {
			return nil, p.newKindError(GrokSSOErrorKindExpired, "token-poll", 0, "", "device flow expired", nil)
		}
		cred, err := p.pollOnce(ctx, cookie, dc)
		if err == nil {
			return cred, nil
		}
		switch err.Kind {
		case GrokSSOErrorKindTemporary:
			return nil, err
		case GrokSSOErrorKindDenied, GrokSSOErrorKindExpired, GrokSSOErrorKindInvalid, GrokSSOErrorKindProtocol:
			return nil, err
		}
		sleep := interval
		if err.Step == "token-poll-slowdown" {
			sleep += interval
		}
		select {
		case <-ctx.Done():
			return nil, p.newKindError(GrokSSOErrorKindTemporary, "token-poll", 0, "", "poll cancelled", ctx.Err())
		case <-time.After(sleep):
		}
	}
}

func (p *GrokSSOProtocol) pollOnce(ctx context.Context, cookie GrokSSOCookie, dc *grokSSODeviceCodeResponse) (*GrokOAuthCredential, *GrokSSOError) {
	form := neturl.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	form.Set("client_id", p.cfg.ClientID)
	form.Set("device_code", dc.DeviceCode)
	resp, err := p.doJSONRequest(ctx, http.MethodPost, p.joinAuthPath(p.cfg.TokenPath), strings.NewReader(form.Encode()), cookie, map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return nil, classifyGrokSSOResponse("token-poll", resp, nil)
	}
	var token grokSSOTokenResponse
	if err := json.Unmarshal(resp.Body, &token); err != nil {
		return nil, p.newKindError(GrokSSOErrorKindProtocol, "token-poll", resp.Status, resp.RequestID, "failed to decode token response", err)
	}
	if token.AccessToken != "" {
		cred := &GrokOAuthCredential{
			AccessToken:         token.AccessToken,
			RefreshToken:        token.RefreshToken,
			TokenType:           token.TokenType,
			ExpiresAt:           time.Now().Add(time.Duration(token.ExpiresIn) * time.Second),
			ClientID:            p.cfg.ClientID,
			Scope:               chooseString(token.Scope, p.cfg.Scope),
			RawRequestID:        resp.RequestID,
			IDToken:             chooseString(token.IDToken, ""),
			RawUpstreamResponse: string(resp.Body),
		}
		return cred, nil
	}
	if token.Error != "" {
		switch token.Error {
		case "authorization_pending":
			return nil, &GrokSSOError{Kind: GrokSSOErrorKindTemporary, Step: "token-poll", StatusCode: resp.Status, RequestID: resp.RequestID, Message: "authorization pending"}
		case "slow_down":
			return nil, &GrokSSOError{Kind: GrokSSOErrorKindTemporary, Step: "token-poll-slowdown", StatusCode: resp.Status, RequestID: resp.RequestID, Message: "slow down"}
		case "access_denied":
			return nil, &GrokSSOError{Kind: GrokSSOErrorKindDenied, Step: "token-poll", StatusCode: resp.Status, RequestID: resp.RequestID, Message: "authorization denied"}
		case "expired_token":
			return nil, &GrokSSOError{Kind: GrokSSOErrorKindExpired, Step: "token-poll", StatusCode: resp.Status, RequestID: resp.RequestID, Message: "authorization expired"}
		case "invalid_grant", "invalid_client":
			return nil, &GrokSSOError{Kind: GrokSSOErrorKindInvalid, Step: "token-poll", StatusCode: resp.Status, RequestID: resp.RequestID, Message: "authorization invalid"}
		default:
			return nil, &GrokSSOError{Kind: GrokSSOErrorKindProtocol, Step: "token-poll", StatusCode: resp.Status, RequestID: resp.RequestID, Message: "unexpected token error"}
		}
	}
	return nil, p.newKindError(GrokSSOErrorKindProtocol, "token-poll", resp.Status, resp.RequestID, "token response missing access_token", nil)
}

func (p *GrokSSOProtocol) doJSONRequest(ctx context.Context, method string, target *neturl.URL, body io.Reader, cookie GrokSSOCookie, headers map[string]string) (*grokHTTPResult, *GrokSSOError) {
	if p == nil {
		return nil, &GrokSSOError{Kind: GrokSSOErrorKindProtocol, Message: "protocol not initialized", Err: ErrGrokSSOProtocol}
	}
	if target == nil {
		return nil, p.newKindError(GrokSSOErrorKindProtocol, "request", 0, "", "missing request URL", nil)
	}
	if err := validateGrokSSORequestURL(target); err != nil {
		return nil, p.newKindError(GrokSSOErrorKindProtocol, "request", 0, "", err.Error(), err)
	}
	client := p.httpClient()
	var payload []byte
	if body != nil {
		buf, err := io.ReadAll(body)
		if err != nil {
			return nil, p.newKindError(GrokSSOErrorKindTemporary, "request", 0, "", "failed to read request body", err)
		}
		payload = buf
	}
	current := cloneNetURL(target)
	currentMethod := method
	currentPayload := payload
	var lastRequestID string
	for redirectCount := 0; redirectCount <= p.cfg.MaxRedirects; redirectCount++ {
		req, err := http.NewRequestWithContext(ctx, currentMethod, current.String(), bytes.NewReader(currentPayload))
		if err != nil {
			return nil, p.newKindError(GrokSSOErrorKindTemporary, "request", 0, "", "failed to create request", err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		for k, v := range p.defaultHeader {
			if req.Header.Get(k) == "" {
				req.Header.Set(k, v)
			}
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Cookie", buildCookieHeader(cookie))
		resp, err := client.Do(req)
		if err != nil {
			return nil, p.transportError("request", err)
		}
		bodyBytes, readErr := readLimitedBody(resp.Body, p.cfg.MaxResponseBytes)
		resp.Body.Close()
		if readErr != nil {
			return nil, p.newKindError(GrokSSOErrorKindTemporary, "request", resp.StatusCode, extractRequestID(resp.Header), "failed to read response body", readErr)
		}
		result := &grokHTTPResult{URL: cloneNetURL(resp.Request.URL), Status: resp.StatusCode, Header: resp.Header.Clone(), Body: bodyBytes, RequestID: extractRequestID(resp.Header)}
		if result.RequestID != "" {
			lastRequestID = result.RequestID
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			location := strings.TrimSpace(resp.Header.Get("Location"))
			if location == "" {
				return result, p.newKindError(GrokSSOErrorKindProtocol, "redirect", resp.StatusCode, result.RequestID, "redirect missing location", nil)
			}
			nextURL, err := current.Parse(location)
			if err != nil {
				return result, p.newKindError(GrokSSOErrorKindProtocol, "redirect", resp.StatusCode, result.RequestID, "invalid redirect location", err)
			}
			if err := allowedRedirectURL(current, nextURL); err != nil {
				return result, p.newKindError(GrokSSOErrorKindInvalid, "redirect", resp.StatusCode, result.RequestID, err.Error(), err)
			}
			if redirectCount >= p.cfg.MaxRedirects {
				return result, p.newKindError(GrokSSOErrorKindTemporary, "redirect", resp.StatusCode, result.RequestID, "too many redirects", nil)
			}
			if resp.StatusCode == http.StatusSeeOther || resp.StatusCode == http.StatusMovedPermanently || resp.StatusCode == http.StatusFound {
				currentMethod = http.MethodGet
				currentPayload = nil
			}
			current = nextURL
			continue
		}
		result.RequestID = chooseString(result.RequestID, lastRequestID)
		return result, nil
	}
	return nil, p.newKindError(GrokSSOErrorKindTemporary, "redirect", 0, "", "too many redirects", nil)
}

func validateGrokSSORequestURL(u *neturl.URL) error {
	if u == nil {
		return fmt.Errorf("missing request URL")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("request must use https")
	}
	if u.User != nil {
		return fmt.Errorf("request URL must not contain userinfo")
	}
	if !isAllowedXAIHost(u.Hostname()) {
		return fmt.Errorf("request host is not allowlisted")
	}
	return nil
}

func (p *GrokSSOProtocol) httpClient() *http.Client {
	if p.client == nil {
		return &http.Client{Timeout: p.cfg.HTTPTimeout}
	}
	clone := *p.client
	clone.Timeout = p.cfg.HTTPTimeout
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &clone
}

func (p *GrokSSOProtocol) joinAuthPath(path string) *neturl.URL {
	base, _ := neturl.Parse(p.cfg.AuthBaseURL)
	joined, _ := base.Parse(path)
	return joined
}

func (p *GrokSSOProtocol) transportError(step string, err error) *GrokSSOError {
	if err == nil {
		return nil
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return p.newKindError(GrokSSOErrorKindTemporary, step, 0, "", "upstream timeout", err)
	}
	return p.newKindError(GrokSSOErrorKindTemporary, step, 0, "", "upstream request failed", err)
}

func classifyGrokSSOResponse(step string, resp *grokHTTPResult, cause error) *GrokSSOError {
	if resp == nil {
		return &GrokSSOError{Kind: GrokSSOErrorKindTemporary, Step: step, Message: "missing upstream response", Err: cause}
	}
	kind := GrokSSOErrorKindTemporary
	switch resp.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		kind = GrokSSOErrorKindInvalid
	case http.StatusGone:
		kind = GrokSSOErrorKindExpired
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		kind = GrokSSOErrorKindTemporary
	}
	var payload grokSSOJSONError
	if len(resp.Body) > 0 {
		_ = json.Unmarshal(resp.Body, &payload)
	}
	msg := strings.TrimSpace(payload.ErrorDescription)
	if msg == "" {
		msg = strings.TrimSpace(payload.Message)
	}
	if msg == "" {
		msg = strings.TrimSpace(payload.Detail)
	}
	if msg == "" {
		msg = http.StatusText(resp.Status)
	}
	if payload.Error == "access_denied" {
		kind = GrokSSOErrorKindDenied
	}
	if payload.Error == "expired_token" {
		kind = GrokSSOErrorKindExpired
	}
	if payload.Error == "invalid_grant" || payload.Error == "invalid_client" {
		kind = GrokSSOErrorKindInvalid
	}
	if resp.Status == http.StatusTooManyRequests || resp.Status >= 500 {
		kind = GrokSSOErrorKindTemporary
	}
	return &GrokSSOError{Kind: kind, Step: step, StatusCode: resp.Status, RequestID: resp.RequestID, Message: msg, Err: cause}
}

func (p *GrokSSOProtocol) newKindError(kind GrokSSOErrorKind, step string, status int, requestID, message string, err error) *GrokSSOError {
	return &GrokSSOError{Kind: kind, Step: step, StatusCode: status, RequestID: requestID, Message: message, Err: err}
}

func extractRequestID(h http.Header) string {
	ids := make([]string, 0, 2)
	for _, key := range []string{"x-request-id", "xai-request-id"} {
		if v := strings.TrimSpace(h.Get(key)); v != "" {
			ids = append(ids, v)
		}
	}
	return strings.Join(uniqueStrings(ids), ",")
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func readLimitedBody(r io.ReadCloser, limit int64) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response body exceeds %d bytes", limit)
	}
	return data, nil
}

func buildCookieHeader(cookie GrokSSOCookie) string {
	cookie.Name = strings.TrimSpace(cookie.Name)
	cookie.Value = strings.TrimSpace(cookie.Value)
	if cookie.Name == "" {
		cookie.Name = "sso"
	}
	return cookie.Name + "=" + cookie.Value
}

func chooseString(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func cloneNetURL(u *neturl.URL) *neturl.URL {
	if u == nil {
		return nil
	}
	cpy := *u
	return &cpy
}

func (p *GrokSSOProtocol) joinBaseURL(path string) *neturl.URL {
	return p.joinAuthPath(path)
}

func normalizeSSOHeaderMap(raw map[string]string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		if strings.EqualFold(key, "authorization") {
			continue
		}
		val := strings.TrimSpace(v)
		if val == "" {
			continue
		}
		out[key] = val
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func splitSSOLines(raw string) []string {
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// collectSSOBatchTokens merges sso_tokens[] (preferred) and legacy multi-line token
// into per-line inputs. Callers must not log or echo the returned values.
func collectSSOBatchTokens(req ssoBatchInput) []string {
	capHint := len(req.SSOTokens)
	if req.Token != "" {
		capHint++
	}
	out := make([]string, 0, capHint)
	for _, item := range req.SSOTokens {
		out = append(out, splitSSOLines(item)...)
	}
	out = append(out, splitSSOLines(req.Token)...)
	return out
}

type ssoBatchInput struct {
	// SSOTokens is the primary frontend payload (HAR: sso_tokens string[]).
	SSOTokens []string `json:"sso_tokens"`
	// Token keeps multi-line text compatibility for older clients.
	Token          string            `json:"token"`
	ProxyURL       string            `json:"proxy_url"`
	CustomHeaders  map[string]string `json:"custom_headers"`
	AllowDuplicate bool              `json:"allow_duplicate"`
}

type ssoBatchResult struct {
	Index     int    `json:"index"`
	Status    string `json:"status"`
	ID        int64  `json:"id,omitempty"`
	Email     string `json:"email,omitempty"`
	PlanType  string `json:"plan_type,omitempty"`
	Updated   bool   `json:"updated,omitempty"`
	Message   string `json:"message,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

type ssoBatchSummary struct {
	Total      int `json:"total"`
	Succeeded  int `json:"succeeded"`
	Failed     int `json:"failed"`
	Duplicated int `json:"duplicated"`
}

func (h *Handler) AddSSOAccount(c *gin.Context) {
	var req ssoBatchInput
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "请求格式错误")
		return
	}

	proxyURL := strings.TrimSpace(req.ProxyURL)
	if proxyURL == "" && h != nil && h.store != nil {
		proxyURL = h.store.GetProxyURL()
	}
	if err := security.ValidateProxyURL(proxyURL); err != nil {
		writeError(c, http.StatusBadRequest, "代理URL无效")
		return
	}

	customHeaders, err := normalizeSSOHeaderMap(req.CustomHeaders)
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	_ = customHeaders
	_ = req.AllowDuplicate

	tokens := collectSSOBatchTokens(req)
	if len(tokens) == 0 {
		// Do not echo request token fields in the error body.
		writeError(c, http.StatusBadRequest, "未找到有效的 SSO token")
		return
	}
	if len(tokens) > 100 {
		writeError(c, http.StatusBadRequest, "单次最多添加100个账号")
		return
	}

	proto, err := NewGrokSSOProtocol(auth.BuildHTTPClient(proxyURL), GrokSSOConfig{})
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	if customHeaders != nil {
		proto.defaultHeader = customHeaders
	}

	stream := strings.EqualFold(c.Query("stream"), "true")
	if stream {
		setupSSE(c)
		sendImportEvent(c, importEvent{Type: "progress", Current: 0, Total: len(tokens), Success: 0, Duplicate: 0, Failed: 0})
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	summary := ssoBatchSummary{Total: len(tokens)}
	results := make([]ssoBatchResult, 0, len(tokens))
	for i, token := range tokens {
		cookie := GrokSSOCookie{Name: "sso", Value: token}
		if strings.Contains(token, "sso-rw=") {
			cookie.Name = "sso-rw"
			cookie.Value = strings.TrimSpace(strings.TrimPrefix(token, "sso-rw="))
		}

		item := ssoBatchResult{Index: i}
		cred, flowErr := proto.ExchangeOne(ctx, cookie)
		if flowErr != nil {
			item.Status = "failed"
			item.Message = flowErr.Error()
			item.RequestID = flowErr.RequestID
			summary.Failed++
			results = append(results, item)
			if stream {
				sendImportEvent(c, importEvent{Type: "progress", Current: i + 1, Total: len(tokens), Success: summary.Succeeded, Duplicate: summary.Duplicated, Failed: summary.Failed})
				sendSSEJSON(c, item)
			}
			continue
		}
		if cred == nil || strings.TrimSpace(cred.RefreshToken) == "" {
			item.Status = "failed"
			item.Message = "授权服务器未返回 refresh_token，请确认已开启 offline_access scope"
			summary.Failed++
			results = append(results, item)
			if stream {
				sendImportEvent(c, importEvent{Type: "progress", Current: i + 1, Total: len(tokens), Success: summary.Succeeded, Duplicate: summary.Duplicated, Failed: summary.Failed})
				sendSSEJSON(c, item)
			}
			continue
		}

		upsert, upsertErr := h.UpsertOAuthAccountFromTokenInfo(ctx, &OAuthTokenInfo{
			AccessToken:  cred.AccessToken,
			RefreshToken: cred.RefreshToken,
			IDToken:      cred.IDToken,
			ExpiresIn:    int64(time.Until(cred.ExpiresAt).Seconds()),
		}, "", proxyURL, "grok_sso", req.AllowDuplicate)
		if upsertErr != nil {
			item.Status = "failed"
			item.Message = upsertErr.Error()
			summary.Failed++
			results = append(results, item)
			if stream {
				sendImportEvent(c, importEvent{Type: "progress", Current: i + 1, Total: len(tokens), Success: summary.Succeeded, Duplicate: summary.Duplicated, Failed: summary.Failed})
				sendSSEJSON(c, item)
			}
			continue
		}

		item.Status = "succeeded"
		item.ID = upsert.ID
		item.Email = upsert.Email
		item.PlanType = upsert.PlanType
		item.Updated = upsert.Updated
		summary.Succeeded++
		if upsert.Updated {
			summary.Duplicated++
		}
		results = append(results, item)
		if stream {
			sendImportEvent(c, importEvent{Type: "progress", Current: i + 1, Total: len(tokens), Success: summary.Succeeded, Duplicate: summary.Duplicated, Failed: summary.Failed})
			sendSSEJSON(c, item)
		}
	}

	if stream {
		sendImportEvent(c, importEvent{Type: "complete", Current: len(tokens), Total: len(tokens), Success: summary.Succeeded, Duplicate: summary.Duplicated, Failed: summary.Failed})
		return
	}

	c.JSON(http.StatusOK, gin.H{"summary": summary, "results": results})
}

// Note: this file intentionally exposes only protocol primitives.
// Handlers can parse input, run the exchange, and decide whether to persist.
