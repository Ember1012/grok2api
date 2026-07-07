package grokquota

import (
	"encoding/json"
	"strings"
)

type AuthFailureKind string

const (
	AuthFailureNone           AuthFailureKind = ""
	AuthFailureBadCredentials AuthFailureKind = "bad_credentials"
)

// ClassifyXAIAuthFailure 精确识别 xAI OAuth Access Token 失效（bad-credentials）。
// 仅匹配：
//   - code == "unauthenticated:bad-credentials"
//   - 错误消息包含 "oauth2 access token could not be validated"
// 普通 403（entitlement、subscription、model permission 等）返回 None，不触发刷新。
func ClassifyXAIAuthFailure(statusCode int, body []byte) AuthFailureKind {
	if statusCode != 403 && statusCode != 401 {
		return AuthFailureNone
	}
	if len(body) == 0 {
		return AuthFailureNone
	}

	lower := strings.ToLower(string(body))
	if strings.Contains(lower, "unauthenticated:bad-credentials") ||
		strings.Contains(lower, "oauth2 access token could not be validated") {
		return AuthFailureBadCredentials
	}

	// 宽松 JSON 解析，支持顶层 code、嵌套 error/response/status_details 等
	var m map[string]any
	if json.Unmarshal(body, &m) == nil {
		if code := extractXAIAuthCode(m); code == "unauthenticated:bad-credentials" {
			return AuthFailureBadCredentials
		}
	}

	return AuthFailureNone
}

// IsXAIInvalidAccessToken 是 ClassifyXAIAuthFailure 的布尔快捷方式。
func IsXAIInvalidAccessToken(statusCode int, body []byte) bool {
	return ClassifyXAIAuthFailure(statusCode, body) == AuthFailureBadCredentials
}

func extractXAIAuthCode(m map[string]any) string {
	// 直接检查常见顶层字段
	for _, k := range []string{"code", "error_code", "type"} {
		if v := stringFromAny(m[k]); strings.EqualFold(v, "unauthenticated:bad-credentials") {
			return "unauthenticated:bad-credentials"
		}
	}

	// 递归检查常见错误包装结构
	for _, k := range []string{"error", "response", "status_details", "details", "failed"} {
		if sub := mapFromAny(m[k]); sub != nil {
			if c := extractXAIAuthCode(sub); c != "" {
				return c
			}
		}
	}

	// 顶层 error 是字符串的情况
	if s := stringFromAny(m["error"]); s != "" {
		if strings.Contains(strings.ToLower(s), "unauthenticated:bad-credentials") {
			return "unauthenticated:bad-credentials"
		}
	}
	if s := stringFromAny(m["message"]); s != "" {
		if strings.Contains(strings.ToLower(s), "oauth2 access token could not be validated") {
			return "unauthenticated:bad-credentials"
		}
	}

	return ""
}

func stringFromAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func mapFromAny(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}