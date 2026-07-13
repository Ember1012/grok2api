package proxy

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/codex2api/database"
)

const (
	OfficialGrokModelsURL = "https://docs.x.ai/docs/models"
	DefaultGrokModelID    = "grok-4.3"

	ModelCategoryText  = "text"
	ModelCategoryImage = "image"

	ModelSourceBuiltin          = "builtin"
	ModelSourceOfficialGrokDocs = "official_grok_docs"
	ModelSourceReasoningEffort  = "reasoning_effort"
	ModelSourceManual           = "manual"
)

// ModelInfo describes one model exposed by this proxy.
type ModelInfo struct {
	ID                   string     `json:"id"`
	Enabled              bool       `json:"enabled"`
	Category             string     `json:"category"`
	Source               string     `json:"source"`
	Capabilities         []string   `json:"capabilities"`
	ProOnly              bool       `json:"pro_only"`
	APIKeyAuthAvailable  bool       `json:"api_key_auth_available"`
	LastSeenAt           *time.Time `json:"last_seen_at,omitempty"`
	UpdatedAt            *time.Time `json:"updated_at,omitempty"`
}

// ModelCatalog is the admin-facing model list plus registry metadata.
type ModelCatalog struct {
	Models       []string    `json:"models"`
	Items        []ModelInfo `json:"items"`
	LastSyncedAt *time.Time  `json:"last_synced_at,omitempty"`
	SourceURL    string      `json:"source_url"`
	Warning      string      `json:"warning,omitempty"`
}

// ModelSyncResult is returned after a manual upstream sync.
type ModelSyncResult struct {
	Added        int         `json:"added"`
	Updated      int         `json:"updated"`
	Unchanged    int         `json:"unchanged"`
	Skipped      []string    `json:"skipped"`
	Models       []string    `json:"models"`
	Items        []ModelInfo `json:"items"`
	LastSyncedAt time.Time   `json:"last_synced_at"`
	SourceURL    string      `json:"source_url"`
}

var builtinModelInfos = []ModelInfo{
	modelInfoForID("grok", ModelSourceBuiltin),
	modelInfoForID("grok-latest", ModelSourceBuiltin),
	modelInfoForID(DefaultGrokModelID, ModelSourceBuiltin),
	modelInfoForID("grok-build-0.1", ModelSourceBuiltin),
	modelInfoForID("grok-4.20-0309-reasoning", ModelSourceBuiltin),
	modelInfoForID("grok-4.20-0309-non-reasoning", ModelSourceBuiltin),
	modelInfoForID("grok-4.20-multi-agent-0309", ModelSourceBuiltin),
	modelInfoForID("grok-2-image", ModelSourceBuiltin),
}

// SupportedModels is the static built-in fallback list. Runtime handlers use
// SupportedModelIDs so synced registry entries can take effect immediately.
var SupportedModels = BuiltinModelIDs()

func BuiltinModelIDs() []string {
	ids := make([]string, 0, len(builtinModelInfos))
	for _, model := range builtinModelInfos {
		ids = append(ids, model.ID)
	}
	return ids
}

func modelInfoForID(id string, source string) ModelInfo {
	id = strings.TrimSpace(id)
	if source == "" {
		source = ModelSourceBuiltin
	}
	info := ModelInfo{
		ID:                  id,
		Enabled:             true,
		Category:            ModelCategoryText,
		Source:              source,
		Capabilities:        capabilitiesForModelID(id),
		APIKeyAuthAvailable: true,
	}
	if strings.Contains(strings.ToLower(id), "image") {
		info.Category = ModelCategoryImage
	}
	return info
}

func modelInfoFromRow(row database.ModelRegistryRow) ModelInfo {
	var lastSeenAt *time.Time
	if row.LastSeenAt.Valid {
		t := row.LastSeenAt.Time.UTC()
		lastSeenAt = &t
	}
	var updatedAt *time.Time
	if !row.UpdatedAt.IsZero() {
		t := row.UpdatedAt.UTC()
		updatedAt = &t
	}
	return ModelInfo{
		ID:                  row.ID,
		Enabled:             row.Enabled,
		Category:            valueOrDefault(row.Category, ModelCategoryText),
		Source:              valueOrDefault(row.Source, ModelSourceManual),
		Capabilities:        capabilitiesForModelID(row.ID),
		ProOnly:             row.ProOnly,
		APIKeyAuthAvailable: row.APIKeyAuthAvailable,
		LastSeenAt:          lastSeenAt,
		UpdatedAt:           updatedAt,
	}
}

func modelInfoToRow(info ModelInfo, lastSeenAt time.Time) database.ModelRegistryRow {
	return database.ModelRegistryRow{
		ID:                  info.ID,
		Enabled:             info.Enabled,
		Category:            valueOrDefault(info.Category, ModelCategoryText),
		Source:              valueOrDefault(info.Source, ModelSourceManual),
		ProOnly:             info.ProOnly,
		APIKeyAuthAvailable: info.APIKeyAuthAvailable,
		LastSeenAt:          sql.NullTime{Time: lastSeenAt.UTC(), Valid: !lastSeenAt.IsZero()},
	}
}

func valueOrDefault(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

// ListModelCatalog returns enabled model IDs plus metadata. It falls back to
// built-ins if the registry cannot be read.
func ListModelCatalog(ctx context.Context, db *database.DB) (ModelCatalog, error) {
	catalog := builtinCatalog()
	if db == nil {
		return catalog, nil
	}

	rows, err := db.ListModelRegistry(ctx)
	if err != nil {
		catalog.Warning = err.Error()
		return catalog, err
	}

	merged := mergeModelInfos(rows)
	if settings, settingsErr := db.GetSystemSettings(ctx); settingsErr == nil && settings != nil {
		merged = appendReasoningEffortModelInfos(merged, settings.ReasoningEffortModels)
	} else if settingsErr != nil && catalog.Warning == "" {
		catalog.Warning = settingsErr.Error()
	}
	catalog.Items = merged
	catalog.Models = enabledModelIDs(merged, false)
	if len(catalog.Models) == 0 {
		catalog.Models = BuiltinModelIDs()
	}

	state, err := db.GetModelRegistrySyncState(ctx)
	if err != nil {
		catalog.Warning = err.Error()
		return catalog, err
	}
	if state != nil {
		catalog.SourceURL = valueOrDefault(state.SourceURL, OfficialGrokModelsURL)
		if state.LastSyncedAt.Valid {
			t := state.LastSyncedAt.Time.UTC()
			catalog.LastSyncedAt = &t
		}
	}
	return catalog, nil
}

func builtinCatalog() ModelCatalog {
	items := append([]ModelInfo(nil), builtinModelInfos...)
	return ModelCatalog{
		Models:    enabledModelIDs(items, false),
		Items:     items,
		SourceURL: OfficialGrokModelsURL,
	}
}

func isGrokModelID(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	return id == "grok" || id == "grok-latest" || strings.HasPrefix(id, "grok-")
}

func mergeModelInfos(rows []database.ModelRegistryRow) []ModelInfo {
	byID := make(map[string]ModelInfo, len(builtinModelInfos)+len(rows))
	for _, info := range builtinModelInfos {
		byID[info.ID] = info
	}
	for _, row := range rows {
		info := modelInfoFromRow(row)
		if info.ID == "" || !isGrokModelID(info.ID) {
			continue
		}
		byID[info.ID] = info
	}

	result := make([]ModelInfo, 0, len(byID))
	for _, info := range builtinModelInfos {
		if merged, ok := byID[info.ID]; ok {
			result = append(result, merged)
			delete(byID, info.ID)
		}
	}
	extras := make([]ModelInfo, 0, len(byID))
	for _, info := range byID {
		extras = append(extras, info)
	}
	sort.Slice(extras, func(i, j int) bool {
		return extras[i].ID < extras[j].ID
	})
	result = append(result, extras...)
	return result
}

func appendReasoningEffortModelInfos(items []ModelInfo, settingsJSON string) []ModelInfo {
	entries, _ := parseReasoningEffortModelEntries(settingsJSON, enabledModelIDs(items, false), false)
	if len(entries) == 0 {
		return items
	}

	result := append([]ModelInfo(nil), items...)
	byID := make(map[string]ModelInfo, len(result)+len(entries)*2)
	for _, item := range result {
		byID[strings.ToLower(strings.TrimSpace(item.ID))] = item
	}

	for _, entry := range entries {
		baseKey := strings.ToLower(entry.Model)
		baseInfo, baseExists := byID[baseKey]
		if !baseExists {
			baseInfo = modelInfoForID(entry.Model, ModelSourceReasoningEffort)
			result = append(result, baseInfo)
			byID[baseKey] = baseInfo
		}

		alias := ReasoningEffortModelAlias(entry.Model, entry.Effort)
		if alias == "" {
			continue
		}
		aliasKey := strings.ToLower(alias)
		if _, exists := byID[aliasKey]; exists {
			continue
		}
		aliasInfo := baseInfo
		aliasInfo.ID = alias
		aliasInfo.Source = ModelSourceReasoningEffort
		aliasInfo.Category = ModelCategoryText
		aliasInfo.LastSeenAt = nil
		aliasInfo.UpdatedAt = nil
		result = append(result, aliasInfo)
		byID[aliasKey] = aliasInfo
	}
	return result
}

// SupportedModelIDs returns enabled runtime model IDs.
func SupportedModelIDs(ctx context.Context, db *database.DB) []string {
	catalog, _ := ListModelCatalog(ctx, db)
	return catalog.Models
}

// TextTestModelIDs returns enabled non-image models for account connection tests.
func TextTestModelIDs(ctx context.Context, db *database.DB) []string {
	catalog, _ := ListModelCatalog(ctx, db)
	ids := enabledModelIDs(catalog.Items, true)
	filtered := ids[:0]
	for _, id := range ids {
		if strings.Contains(id, "(") || strings.Contains(id, ")") {
			continue
		}
		filtered = append(filtered, id)
	}
	return filtered
}

func IsTextTestModelID(ctx context.Context, db *database.DB, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	for _, id := range TextTestModelIDs(ctx, db) {
		if model == id {
			return true
		}
	}
	return false
}

func enabledModelIDs(items []ModelInfo, textOnly bool) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if !item.Enabled {
			continue
		}
		if textOnly && isImageModelInfo(item) {
			continue
		}
		ids = append(ids, item.ID)
	}
	return ids
}

func isImageModelInfo(info ModelInfo) bool {
	return strings.EqualFold(info.Category, ModelCategoryImage) || strings.Contains(strings.ToLower(info.ID), "image")
}

var grokModelIDPattern = regexp.MustCompile(`\bgrok-[a-z0-9]+(?:[.-][a-z0-9]+)*(?:-[a-z][a-z0-9]*(?:-[a-z0-9]+)*)?\b`)

// ParseOfficialGrokModelIDs extracts Grok model IDs from the official docs HTML.
func ParseOfficialGrokModelIDs(html string) (models []string, skipped []string) {
	seen := map[string]struct{}{}
	for _, match := range grokModelIDPattern.FindAllString(strings.ToLower(html), -1) {
		id := strings.TrimSpace(match)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, id)
	}
	sort.SliceStable(models, func(i, j int) bool {
		leftRank := modelSortRank(models[i])
		rightRank := modelSortRank(models[j])
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return models[i] < models[j]
	})
	return models, skipped
}

func modelSortRank(id string) int {
	for index, info := range builtinModelInfos {
		if info.ID == id {
			return index
		}
	}
	return len(builtinModelInfos) + 1000
}

// SyncOfficialGrokModels fetches the fixed official docs page and merges discovered models.
func SyncOfficialGrokModels(ctx context.Context, db *database.DB) (*ModelSyncResult, error) {
	if db == nil {
		return nil, fmt.Errorf("数据库不可用，无法同步模型注册表")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, OfficialGrokModelsURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("官方模型页面暂时不可访问，已保留本地模型列表: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("官方模型页面返回 %d，已保留本地模型列表", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return nil, err
	}
	return ApplyOfficialGrokModelSync(ctx, db, string(body), time.Now().UTC())
}

// ApplyOfficialGrokModelSync merges a fetched official docs page into the registry.
func ApplyOfficialGrokModelSync(ctx context.Context, db *database.DB, html string, syncedAt time.Time) (*ModelSyncResult, error) {
	if db == nil {
		return nil, fmt.Errorf("数据库不可用，无法同步模型注册表")
	}
	ids, skipped := ParseOfficialGrokModelIDs(html)
	if len(ids) == 0 {
		return nil, fmt.Errorf("未从官方模型页面解析到可用模型，已保留本地模型列表")
	}

	existingRows, err := db.ListModelRegistry(ctx)
	if err != nil {
		return nil, err
	}
	existing := make(map[string]database.ModelRegistryRow, len(existingRows))
	for _, row := range existingRows {
		existing[row.ID] = row
	}

	rows := make([]database.ModelRegistryRow, 0, len(ids))
	result := &ModelSyncResult{
		Skipped:   skipped,
		SourceURL: OfficialGrokModelsURL,
	}
	for _, id := range ids {
		info := modelInfoForID(id, ModelSourceOfficialGrokDocs)
		row := modelInfoToRow(info, syncedAt)
		if previous, ok := existing[id]; ok {
			row.Enabled = previous.Enabled
			if modelRegistryMetadataEqual(previous, row) {
				result.Unchanged++
			} else {
				result.Updated++
			}
		} else {
			result.Added++
		}
		rows = append(rows, row)
	}

	if err := db.UpsertModelRegistryRows(ctx, rows); err != nil {
		return nil, err
	}
	if err := db.UpdateModelRegistrySyncState(ctx, OfficialGrokModelsURL, syncedAt); err != nil {
		return nil, err
	}

	catalog, err := ListModelCatalog(ctx, db)
	if err != nil {
		return nil, err
	}
	result.Models = catalog.Models
	result.Items = catalog.Items
	result.LastSyncedAt = syncedAt.UTC()
	return result, nil
}

func modelRegistryMetadataEqual(a database.ModelRegistryRow, b database.ModelRegistryRow) bool {
	return a.Enabled == b.Enabled &&
		valueOrDefault(a.Category, ModelCategoryText) == valueOrDefault(b.Category, ModelCategoryText) &&
		valueOrDefault(a.Source, ModelSourceManual) == valueOrDefault(b.Source, ModelSourceManual) &&
		a.ProOnly == b.ProOnly &&
		a.APIKeyAuthAvailable == b.APIKeyAuthAvailable
}

// AddManualModel inserts a user-defined Grok model into the registry.
func AddManualModel(ctx context.Context, db *database.DB, id, category string) (*ModelCatalog, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("模型 ID 不能为空")
	}
	if !isGrokModelID(id) {
		return nil, fmt.Errorf("仅支持 grok / grok-latest / grok-* 模型 ID")
	}
	if db == nil {
		return nil, fmt.Errorf("数据库不可用，无法添加模型")
	}

	for _, info := range builtinModelInfos {
		if info.ID == id {
			return nil, fmt.Errorf("模型 %s 已存在于内置列表", id)
		}
	}
	existing, err := db.GetModelRegistryRow(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, fmt.Errorf("模型 %s 已存在", id)
	}

	category = strings.TrimSpace(strings.ToLower(category))
	switch category {
	case "":
		if strings.Contains(strings.ToLower(id), "image") {
			category = ModelCategoryImage
		} else {
			category = ModelCategoryText
		}
	case ModelCategoryText, ModelCategoryImage:
	default:
		return nil, fmt.Errorf("category 仅支持 text 或 image")
	}

	info := modelInfoForID(id, ModelSourceManual)
	info.Enabled = true
	info.Category = category
	if err := db.UpsertModelRegistryRows(ctx, []database.ModelRegistryRow{modelInfoToRow(info, time.Time{})}); err != nil {
		return nil, err
	}
	catalog, err := ListModelCatalog(ctx, db)
	if err != nil {
		return nil, err
	}
	return &catalog, nil
}

// RemoveManualModel deletes a manually added model from the registry.
func RemoveManualModel(ctx context.Context, db *database.DB, id string) (*ModelCatalog, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("模型 ID 不能为空")
	}
	if db == nil {
		return nil, fmt.Errorf("数据库不可用，无法删除模型")
	}

	for _, info := range builtinModelInfos {
		if info.ID == id {
			return nil, fmt.Errorf("内置模型不可删除")
		}
	}

	row, err := db.GetModelRegistryRow(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("模型 %s 不存在", id)
	}
	source := valueOrDefault(row.Source, ModelSourceManual)
	if source != ModelSourceManual {
		return nil, fmt.Errorf("仅可删除手动添加的模型")
	}
	if err := db.DeleteModelRegistryRow(ctx, id); err != nil {
		return nil, err
	}
	catalog, err := ListModelCatalog(ctx, db)
	if err != nil {
		return nil, err
	}
	return &catalog, nil
}
