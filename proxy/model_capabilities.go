package proxy

import (
	"strings"

	"github.com/tidwall/sjson"
)

const (
	ModelCapabilityText            = "text"
	ModelCapabilityReasoning       = "reasoning"
	ModelCapabilityImageGeneration = "image_generation"
)

func capabilitiesForModelID(model string) []string {
	model = strings.ToLower(strings.TrimSpace(model))
	if resolved, ok := resolveGrokPublicModelAlias(model); ok {
		model = resolved
	}

	switch model {
	case DefaultGrokModelID, "grok-4.20-0309-reasoning", "grok-4.20-multi-agent-0309":
		return []string{ModelCapabilityText, ModelCapabilityReasoning}
	case "grok-2-image":
		return []string{ModelCapabilityImageGeneration}
	default:
		return []string{ModelCapabilityText}
	}
}

func modelSupportsCapability(model string, capability string) bool {
	for _, current := range capabilitiesForModelID(model) {
		if current == capability {
			return true
		}
	}
	return false
}

// patchGrokRequestForModel enforces model capabilities after model mapping and
// before authorization is injected by the upstream forwarding layer.
func patchGrokRequestForModel(body []byte, model string) []byte {
	if modelSupportsCapability(model, ModelCapabilityReasoning) {
		return body
	}

	patched := body
	for _, path := range []string{"reasoning", "reasoning_effort", "reasoningEffort"} {
		updated, err := sjson.DeleteBytes(patched, path)
		if err == nil {
			patched = updated
		}
	}
	return patched
}