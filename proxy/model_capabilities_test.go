package proxy

import (
	"slices"
	"testing"

	"github.com/tidwall/gjson"
)

func TestGrokModelCapabilities(t *testing.T) {
	if !modelSupportsCapability("grok-4.3", ModelCapabilityReasoning) {
		t.Fatal("grok-4.3 should support reasoning")
	}
	if !modelSupportsCapability("grok-latest", ModelCapabilityReasoning) {
		t.Fatal("grok-latest should inherit default model reasoning capability")
	}
	if modelSupportsCapability("grok-build-0.1", ModelCapabilityReasoning) {
		t.Fatal("grok-build-0.1 should not support reasoning")
	}
	if !slices.Contains(capabilitiesForModelID("grok-2-image"), ModelCapabilityImageGeneration) {
		t.Fatal("grok-2-image should expose image generation capability")
	}
}

func TestPatchGrokRequestRemovesReasoningForBuildModel(t *testing.T) {
	body := []byte(`{
		"model":"grok-build-0.1",
		"input":"hello",
		"reasoning":{"effort":"medium","summary":"auto"},
		"reasoning_effort":"medium",
		"reasoningEffort":"medium"
	}`)

	patched := patchGrokRequestForModel(body, "grok-build-0.1")
	for _, path := range []string{"reasoning", "reasoning_effort", "reasoningEffort"} {
		if gjson.GetBytes(patched, path).Exists() {
			t.Fatalf("unsupported field %q was not removed: %s", path, patched)
		}
	}
	if got := gjson.GetBytes(patched, "model").String(); got != "grok-build-0.1" {
		t.Fatalf("model = %q, want grok-build-0.1", got)
	}
}

func TestPatchGrokRequestPreservesReasoningForGrok43(t *testing.T) {
	body := []byte(`{
		"model":"grok-4.3",
		"input":"hello",
		"reasoning":{"effort":"medium","summary":"auto"}
	}`)

	patched := patchGrokRequestForModel(body, "grok-4.3")
	if got := gjson.GetBytes(patched, "reasoning.effort").String(); got != "medium" {
		t.Fatalf("reasoning effort = %q, want medium; body=%s", got, patched)
	}
}