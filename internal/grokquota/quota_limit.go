package grokquota

import "strings"

// IsXAISpendingLimit 识别 xAI/Grok 额度用尽（spending/pending limit）。
// 仅对 403（兼容 402）生效；bad-credentials 与普通 forbidden 返回 false。
func IsXAISpendingLimit(statusCode int, body []byte) bool {
	if statusCode != 403 && statusCode != 402 {
		return false
	}
	if len(body) == 0 {
		return false
	}

	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "pending-limit") ||
		strings.Contains(lower, "spending-limit") ||
		strings.Contains(lower, "run out of credits") ||
		strings.Contains(lower, "personal-team-blocked")
}