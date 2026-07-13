package proxy

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/codex2api/api"
	"github.com/codex2api/database"
)

func newTestModelRegistryDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "codex2api.db"))
	if err != nil {
		t.Fatalf("New(sqlite) error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestParseOfficialGrokModelIDs(t *testing.T) {
	html := `
		<code>grok-4.3</code>
		<code>grok-build-0.1</code>
		<code>grok-4.20-0309-reasoning</code>
		<code>gpt-5.5</code>
	`
	models, skipped := ParseOfficialGrokModelIDs(html)
	for _, model := range []string{"grok-4.3", "grok-build-0.1", "grok-4.20-0309-reasoning"} {
		if !slices.Contains(models, model) {
			t.Fatalf("parsed models missing %q in %v", model, models)
		}
	}
	if slices.Contains(models, "gpt-5.5") {
		t.Fatalf("parsed models should not include legacy OpenAI model: %v", models)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped = %v, want empty", skipped)
	}
}

func TestApplyOfficialGrokModelSyncMergesWithBuiltinImageModel(t *testing.T) {
	db := newTestModelRegistryDB(t)
	ctx := context.Background()
	html := `grok-4.3 grok-build-0.1 grok-4.20-0309-reasoning grok-2-image gpt-5.5`

	result, err := ApplyOfficialGrokModelSync(ctx, db, html, time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ApplyOfficialGrokModelSync error: %v", err)
	}
	for _, model := range []string{"grok", "grok-latest", "grok-4.3", "grok-2-image"} {
		if !slices.Contains(result.Models, model) {
			t.Fatalf("sync should keep builtin Grok model %q, got %v", model, result.Models)
		}
	}
	if slices.Contains(result.Models, "gpt-5.5") {
		t.Fatalf("sync should not import legacy OpenAI models, got %v", result.Models)
	}

	var image *ModelInfo
	for i := range result.Items {
		if result.Items[i].ID == "grok-2-image" {
			image = &result.Items[i]
			break
		}
	}
	if image == nil || image.Category != ModelCategoryImage {
		t.Fatalf("image model should be marked image category, got %#v", image)
	}
}

func TestDynamicModelRegistryAffectsValidationImmediately(t *testing.T) {
	db := newTestModelRegistryDB(t)
	ctx := context.Background()
	err := db.UpsertModelRegistryRows(ctx, []database.ModelRegistryRow{
		{
			ID:                  "grok-custom-1",
			Enabled:             true,
			Category:            ModelCategoryText,
			Source:              ModelSourceOfficialGrokDocs,
			APIKeyAuthAvailable: true,
		},
	})
	if err != nil {
		t.Fatalf("UpsertModelRegistryRows error: %v", err)
	}

	handler := NewHandler(nil, db, nil, nil)
	models := handler.supportedModelIDs(ctx)
	if !slices.Contains(models, "grok-custom-1") {
		t.Fatalf("runtime supported models missing synced model: %v", models)
	}

	result := api.ValidateResponsesAPIRequest([]byte(`{"model":"grok-custom-1","input":"hello"}`), models)
	if !result.Valid {
		t.Fatalf("synced model should pass validation: %#v", result.Errors)
	}
}

func TestReasoningEffortModelsAreIncludedInCatalog(t *testing.T) {
	db := newTestModelRegistryDB(t)
	ctx := context.Background()
	settings, err := db.GetSystemSettings(ctx)
	if err != nil {
		t.Fatalf("GetSystemSettings error: %v", err)
	}
	if settings == nil {
		settings = &database.SystemSettings{
			SiteName:                         "GrokProxy",
			MaxConcurrency:                   2,
			TestModel:                        "grok-4.3",
			TestConcurrency:                  50,
			BackgroundRefreshIntervalMinutes: 2,
			UsageProbeMaxAgeMinutes:          10,
			UsageProbeConcurrency:            16,
			RecoveryProbeIntervalMinutes:     30,
			PgMaxConns:                       50,
			RedisPoolSize:                    30,
			MaxRetries:                       2,
			MaxRateLimitRetries:              1,
			ModelMapping:                     "{}",
			CodexModelMapping:                "{}",
			PromptFilterMode:                 "monitor",
			PromptFilterThreshold:            50,
			PromptFilterStrictThreshold:      90,
			PromptFilterLogMatches:           true,
			PromptFilterMaxTextLength:        81920,
			PromptFilterCustomPatterns:       "[]",
			PromptFilterDisabledPatterns:     "[]",
			ClientCompatMode:                 "preserve",
			CodexMinCLIVersion:               "0.118.0",
			UsageLogMode:                     "full",
			UsageLogBatchSize:                200,
			UsageLogFlushIntervalSeconds:     5,
			StreamFlushPolicy:                "immediate",
			StreamFlushIntervalMS:            20,
			BillingTierPolicy:                "actual",
			ImageStorageConfig:               "{}",
			SchedulerMode:                    "round_robin",
			AffinityMode:                     "bounded",
			BackgroundConfig:                 "{}",
		}
	}
	settings.ReasoningEffortModels = `[{"model":"grok-4.3","effort":"xhigh"}]`
	if err := db.UpdateSystemSettings(ctx, settings); err != nil {
		t.Fatalf("UpdateSystemSettings error: %v", err)
	}

	catalog, err := ListModelCatalog(ctx, db)
	if err != nil {
		t.Fatalf("ListModelCatalog error: %v", err)
	}
	if !slices.Contains(catalog.Models, "grok-4.3(xhigh)") {
		t.Fatalf("catalog models missing reasoning alias: %v", catalog.Models)
	}

	var aliasInfo *ModelInfo
	for i := range catalog.Items {
		if catalog.Items[i].ID == "grok-4.3(xhigh)" {
			aliasInfo = &catalog.Items[i]
			break
		}
	}
	if aliasInfo == nil {
		t.Fatalf("catalog items missing reasoning alias: %#v", catalog.Items)
	}
	if aliasInfo.Source != ModelSourceReasoningEffort {
		t.Fatalf("alias source = %q, want %q", aliasInfo.Source, ModelSourceReasoningEffort)
	}
	if aliasInfo.Category != ModelCategoryText {
		t.Fatalf("alias category = %q, want %q", aliasInfo.Category, ModelCategoryText)
	}
	if slices.Contains(TextTestModelIDs(ctx, db), "grok-4.3(xhigh)") {
		t.Fatalf("reasoning alias should not be used for direct connection tests")
	}
}

func TestAddAndRemoveManualModel(t *testing.T) {
	db := newTestModelRegistryDB(t)
	ctx := context.Background()

	catalog, err := AddManualModel(ctx, db, "  grok-manual-test  ", "")
	if err != nil {
		t.Fatalf("AddManualModel error: %v", err)
	}
	if !slices.Contains(catalog.Models, "grok-manual-test") {
		t.Fatalf("catalog missing manual model: %v", catalog.Models)
	}
	var manual *ModelInfo
	for i := range catalog.Items {
		if catalog.Items[i].ID == "grok-manual-test" {
			manual = &catalog.Items[i]
			break
		}
	}
	if manual == nil || manual.Source != ModelSourceManual || !manual.Enabled {
		t.Fatalf("manual model metadata unexpected: %#v", manual)
	}
	if manual.Category != ModelCategoryText {
		t.Fatalf("manual model category = %q, want text", manual.Category)
	}

	if _, err := AddManualModel(ctx, db, "gpt-4", ""); err == nil {
		t.Fatalf("AddManualModel should reject non-grok id")
	}
	if _, err := AddManualModel(ctx, db, "grok-manual-test", ""); err == nil {
		t.Fatalf("AddManualModel should reject duplicate id")
	}

	removed, err := RemoveManualModel(ctx, db, "grok-manual-test")
	if err != nil {
		t.Fatalf("RemoveManualModel error: %v", err)
	}
	if slices.Contains(removed.Models, "grok-manual-test") {
		t.Fatalf("manual model should be removed: %v", removed.Models)
	}

	if _, err := RemoveManualModel(ctx, db, DefaultGrokModelID); err == nil {
		t.Fatalf("RemoveManualModel should reject builtin model")
	}
}
