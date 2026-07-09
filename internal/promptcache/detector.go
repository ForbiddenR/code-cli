package promptcache

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"code-cli/internal/core"
)

const (
	// MaxTrackedSources matches the TypeScript prompt-cache detector cap.
	MaxTrackedSources = 10
	// MinCacheMissTokens is the minimum absolute cache-read drop that is worth reporting.
	MinCacheMissTokens int64 = 2_000
	// CacheTTL5Min is the short server-side prompt-cache TTL used for diagnostics.
	CacheTTL5Min = 5 * time.Minute
	// CacheTTL1Hour is the long server-side prompt-cache TTL used for diagnostics.
	CacheTTL1Hour = time.Hour
)

var trackedSourcePrefixes = []string{
	"repl_main_thread",
	"sdk",
	"agent:custom",
	"agent:default",
	"agent:builtin",
}

// ToolSchema is the prompt-cache relevant subset of a tool definition.
type ToolSchema struct {
	Name         string             `json:"name"`
	Description  string             `json:"description,omitempty"`
	InputSchema  json.RawMessage    `json:"input_schema,omitempty"`
	CacheControl *core.CacheControl `json:"cache_control,omitempty"`
}

// Snapshot captures client-side request fields that can affect server-side prompt-cache keys.
type Snapshot struct {
	System              []core.SystemBlock
	ToolSchemas         []ToolSchema
	QuerySource         string
	Model               string
	AgentID             string
	FastMode            bool
	GlobalCacheStrategy string
	Betas               []string
	AutoModeActive      bool
	IsUsingOverage      bool
	CachedMCEnabled     bool
	EffortValue         string
	ExtraBodyParams     any
}

// Observation captures cache-token usage from a completed API response.
type Observation struct {
	QuerySource              string
	AgentID                  string
	CacheReadTokens          int64
	CacheCreationTokens      int64
	LastAssistantMessageTime *time.Time
	RequestID                string
}

// ChangeSet describes prompt-cache relevant changes detected before the API call.
type ChangeSet struct {
	SystemPromptChanged         bool
	ToolSchemasChanged          bool
	ModelChanged                bool
	FastModeChanged             bool
	CacheControlChanged         bool
	GlobalCacheStrategyChanged  bool
	BetasChanged                bool
	AutoModeChanged             bool
	OverageChanged              bool
	CachedMCChanged             bool
	EffortChanged               bool
	ExtraBodyChanged            bool
	AddedToolCount              int
	RemovedToolCount            int
	SystemCharDelta             int
	AddedTools                  []string
	RemovedTools                []string
	ChangedToolSchemas          []string
	PreviousModel               string
	NewModel                    string
	PreviousGlobalCacheStrategy string
	NewGlobalCacheStrategy      string
	AddedBetas                  []string
	RemovedBetas                []string
	PreviousEffortValue         string
	NewEffortValue              string
}

// BreakReport is returned when response usage suggests a prompt-cache break.
type BreakReport struct {
	Reason                    string
	Summary                   string
	Source                    string
	CallNumber                int
	PreviousCacheReadTokens   int64
	CacheReadTokens           int64
	CacheCreationTokens       int64
	TokenDrop                 int64
	LastAssistantOver5MinAgo  bool
	LastAssistantOver1HourAgo bool
	TimeSinceLastAssistant    time.Duration
	RequestID                 string
	Changes                   ChangeSet
}

type previousState struct {
	systemHash            uint32
	toolsHash             uint32
	cacheControlHash      uint32
	toolNames             []string
	perToolHashes         map[string]uint32
	systemCharCount       int
	model                 string
	fastMode              bool
	globalCacheStrategy   string
	betas                 []string
	autoModeActive        bool
	isUsingOverage        bool
	cachedMCEnabled       bool
	effortValue           string
	extraBodyHash         uint32
	callCount             int
	pendingChanges        *ChangeSet
	prevCacheReadTokens   *int64
	cacheDeletionsPending bool
}

// Detector tracks prompt-cache relevant request state across calls.
type Detector struct {
	Now func() time.Time

	mu     sync.Mutex
	states map[string]*previousState
	order  []string
}

// NewDetector creates a prompt-cache break detector.
func NewDetector() *Detector {
	return &Detector{Now: time.Now}
}

// ToolSchemasFromCore converts normalized core tool definitions into prompt-cache schemas.
func ToolSchemasFromCore(tools []core.ToolDefinition) []ToolSchema {
	schemas := make([]ToolSchema, 0, len(tools))
	for _, tool := range tools {
		schemas = append(schemas, ToolSchema{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: append(json.RawMessage(nil), tool.InputSchema...),
		})
	}
	return schemas
}

// RecordPromptState records request state before an API call and stores pending cache-key changes.
func (d *Detector) RecordPromptState(snapshot Snapshot) {
	d.normalize()
	key := trackingKey(snapshot.QuerySource, snapshot.AgentID)
	if key == "" {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	strippedSystem := stripSystemCacheControl(snapshot.System)
	strippedTools := stripToolCacheControl(snapshot.ToolSchemas)
	systemHash := computeHash(strippedSystem)
	toolsHash := computeHash(strippedTools)
	cacheControlHash := computeHash(systemCacheControls(snapshot.System))
	toolNames := make([]string, 0, len(snapshot.ToolSchemas))
	for _, tool := range snapshot.ToolSchemas {
		if tool.Name == "" {
			toolNames = append(toolNames, "unknown")
			continue
		}
		toolNames = append(toolNames, tool.Name)
	}
	computeToolHashes := func() map[string]uint32 {
		return computePerToolHashes(strippedTools, toolNames)
	}
	systemCharCount := systemCharCount(snapshot.System)
	sortedBetas := append([]string(nil), snapshot.Betas...)
	sort.Strings(sortedBetas)
	extraBodyHash := uint32(0)
	if snapshot.ExtraBodyParams != nil {
		extraBodyHash = computeHash(snapshot.ExtraBodyParams)
	}

	prev := d.states[key]
	if prev == nil {
		d.evictIfNeeded()
		d.states[key] = &previousState{
			systemHash:          systemHash,
			toolsHash:           toolsHash,
			cacheControlHash:    cacheControlHash,
			toolNames:           toolNames,
			perToolHashes:       computeToolHashes(),
			systemCharCount:     systemCharCount,
			model:               snapshot.Model,
			fastMode:            snapshot.FastMode,
			globalCacheStrategy: snapshot.GlobalCacheStrategy,
			betas:               sortedBetas,
			autoModeActive:      snapshot.AutoModeActive,
			isUsingOverage:      snapshot.IsUsingOverage,
			cachedMCEnabled:     snapshot.CachedMCEnabled,
			effortValue:         snapshot.EffortValue,
			extraBodyHash:       extraBodyHash,
			callCount:           1,
		}
		d.order = append(d.order, key)
		return
	}

	prev.callCount++
	systemPromptChanged := systemHash != prev.systemHash
	toolSchemasChanged := toolsHash != prev.toolsHash
	modelChanged := snapshot.Model != prev.model
	fastModeChanged := snapshot.FastMode != prev.fastMode
	cacheControlChanged := cacheControlHash != prev.cacheControlHash
	globalCacheStrategyChanged := snapshot.GlobalCacheStrategy != prev.globalCacheStrategy
	betasChanged := !slices.Equal(sortedBetas, prev.betas)
	autoModeChanged := snapshot.AutoModeActive != prev.autoModeActive
	overageChanged := snapshot.IsUsingOverage != prev.isUsingOverage
	cachedMCChanged := snapshot.CachedMCEnabled != prev.cachedMCEnabled
	effortChanged := snapshot.EffortValue != prev.effortValue
	extraBodyChanged := extraBodyHash != prev.extraBodyHash

	if systemPromptChanged || toolSchemasChanged || modelChanged || fastModeChanged || cacheControlChanged || globalCacheStrategyChanged || betasChanged || autoModeChanged || overageChanged || cachedMCChanged || effortChanged || extraBodyChanged {
		prevToolSet := stringSet(prev.toolNames)
		newToolSet := stringSet(toolNames)
		prevBetaSet := stringSet(prev.betas)
		newBetaSet := stringSet(sortedBetas)
		addedTools := filterMissing(toolNames, prevToolSet)
		removedTools := filterMissing(prev.toolNames, newToolSet)
		changedToolSchemas := []string{}
		if toolSchemasChanged {
			newHashes := computeToolHashes()
			for _, name := range toolNames {
				if !prevToolSet[name] {
					continue
				}
				if newHashes[name] != prev.perToolHashes[name] {
					changedToolSchemas = append(changedToolSchemas, name)
				}
			}
			prev.perToolHashes = newHashes
		}
		prev.pendingChanges = &ChangeSet{
			SystemPromptChanged:         systemPromptChanged,
			ToolSchemasChanged:          toolSchemasChanged,
			ModelChanged:                modelChanged,
			FastModeChanged:             fastModeChanged,
			CacheControlChanged:         cacheControlChanged,
			GlobalCacheStrategyChanged:  globalCacheStrategyChanged,
			BetasChanged:                betasChanged,
			AutoModeChanged:             autoModeChanged,
			OverageChanged:              overageChanged,
			CachedMCChanged:             cachedMCChanged,
			EffortChanged:               effortChanged,
			ExtraBodyChanged:            extraBodyChanged,
			AddedToolCount:              len(addedTools),
			RemovedToolCount:            len(removedTools),
			SystemCharDelta:             systemCharCount - prev.systemCharCount,
			AddedTools:                  addedTools,
			RemovedTools:                removedTools,
			ChangedToolSchemas:          changedToolSchemas,
			PreviousModel:               prev.model,
			NewModel:                    snapshot.Model,
			PreviousGlobalCacheStrategy: prev.globalCacheStrategy,
			NewGlobalCacheStrategy:      snapshot.GlobalCacheStrategy,
			AddedBetas:                  filterMissing(sortedBetas, prevBetaSet),
			RemovedBetas:                filterMissing(prev.betas, newBetaSet),
			PreviousEffortValue:         prev.effortValue,
			NewEffortValue:              snapshot.EffortValue,
		}
	} else {
		prev.pendingChanges = nil
	}

	prev.systemHash = systemHash
	prev.toolsHash = toolsHash
	prev.cacheControlHash = cacheControlHash
	prev.toolNames = toolNames
	prev.systemCharCount = systemCharCount
	prev.model = snapshot.Model
	prev.fastMode = snapshot.FastMode
	prev.globalCacheStrategy = snapshot.GlobalCacheStrategy
	prev.betas = sortedBetas
	prev.autoModeActive = snapshot.AutoModeActive
	prev.isUsingOverage = snapshot.IsUsingOverage
	prev.cachedMCEnabled = snapshot.CachedMCEnabled
	prev.effortValue = snapshot.EffortValue
	prev.extraBodyHash = extraBodyHash
}

// CheckResponseForCacheBreak compares response cache usage against the previous response for the same source.
func (d *Detector) CheckResponseForCacheBreak(observation Observation) *BreakReport {
	d.normalize()
	key := trackingKey(observation.QuerySource, observation.AgentID)
	if key == "" {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	state := d.states[key]
	if state == nil || isExcludedModel(state.model) {
		return nil
	}
	prevCacheRead := state.prevCacheReadTokens
	currentCacheRead := observation.CacheReadTokens
	state.prevCacheReadTokens = &currentCacheRead
	if prevCacheRead == nil {
		return nil
	}

	if state.cacheDeletionsPending {
		state.cacheDeletionsPending = false
		state.pendingChanges = nil
		return nil
	}

	tokenDrop := *prevCacheRead - observation.CacheReadTokens
	if observation.CacheReadTokens >= int64(float64(*prevCacheRead)*0.95) || tokenDrop < MinCacheMissTokens {
		state.pendingChanges = nil
		return nil
	}

	changes := state.pendingChanges
	parts := reasonParts(changes)
	var since time.Duration
	lastOver5Min := false
	lastOver1Hour := false
	if observation.LastAssistantMessageTime != nil {
		since = d.now().Sub(*observation.LastAssistantMessageTime)
		lastOver5Min = since > CacheTTL5Min
		lastOver1Hour = since > CacheTTL1Hour
	}
	reason := "unknown cause"
	if len(parts) > 0 {
		reason = strings.Join(parts, ", ")
	} else if lastOver1Hour {
		reason = "possible 1h TTL expiry (prompt unchanged)"
	} else if lastOver5Min {
		reason = "possible 5min TTL expiry (prompt unchanged)"
	} else if observation.LastAssistantMessageTime != nil {
		reason = "likely server-side (prompt unchanged, <5min gap)"
	}

	reportChanges := ChangeSet{}
	if changes != nil {
		reportChanges = sanitizeChanges(*changes)
	}
	report := &BreakReport{
		Reason:                    reason,
		Source:                    observation.QuerySource,
		CallNumber:                state.callCount,
		PreviousCacheReadTokens:   *prevCacheRead,
		CacheReadTokens:           observation.CacheReadTokens,
		CacheCreationTokens:       observation.CacheCreationTokens,
		TokenDrop:                 tokenDrop,
		LastAssistantOver5MinAgo:  lastOver5Min,
		LastAssistantOver1HourAgo: lastOver1Hour,
		TimeSinceLastAssistant:    since,
		RequestID:                 observation.RequestID,
		Changes:                   reportChanges,
	}
	report.Summary = fmt.Sprintf("[PROMPT CACHE BREAK] %s [source=%s, call #%d, cache read: %d → %d, creation: %d]", report.Reason, report.Source, report.CallNumber, report.PreviousCacheReadTokens, report.CacheReadTokens, report.CacheCreationTokens)
	state.pendingChanges = nil
	return report
}

// NotifyCacheDeletion marks the next cache-read drop for a source as expected.
func (d *Detector) NotifyCacheDeletion(querySource string, agentID string) {
	d.normalize()
	key := trackingKey(querySource, agentID)
	if key == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if state := d.states[key]; state != nil {
		state.cacheDeletionsPending = true
	}
}

// NotifyCompaction resets the cache-read baseline for a source after compaction.
func (d *Detector) NotifyCompaction(querySource string, agentID string) {
	d.normalize()
	key := trackingKey(querySource, agentID)
	if key == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if state := d.states[key]; state != nil {
		state.prevCacheReadTokens = nil
	}
}

// CleanupAgentTracking removes state for one tracked agent.
func (d *Detector) CleanupAgentTracking(agentID string) {
	d.normalize()
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.states, agentID)
	for i, key := range d.order {
		if key == agentID {
			d.order = slices.Delete(d.order, i, i+1)
			return
		}
	}
}

// Reset clears all tracked prompt-cache state.
func (d *Detector) Reset() {
	d.normalize()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.states = map[string]*previousState{}
	d.order = nil
}

func (d *Detector) normalize() {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.states == nil {
		d.states = map[string]*previousState{}
	}
}

func (d *Detector) now() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

func (d *Detector) evictIfNeeded() {
	for len(d.states) >= MaxTrackedSources && len(d.order) > 0 {
		oldest := d.order[0]
		d.order = d.order[1:]
		delete(d.states, oldest)
	}
}

func trackingKey(querySource string, agentID string) string {
	if querySource == "compact" {
		return "repl_main_thread"
	}
	for _, prefix := range trackedSourcePrefixes {
		if strings.HasPrefix(querySource, prefix) {
			if agentID != "" {
				return agentID
			}
			return querySource
		}
	}
	return ""
}

func isExcludedModel(model string) bool {
	return strings.Contains(model, "haiku")
}

func stripSystemCacheControl(blocks []core.SystemBlock) []core.SystemBlock {
	stripped := append([]core.SystemBlock(nil), blocks...)
	for i := range stripped {
		stripped[i].CacheControl = nil
	}
	return stripped
}

func stripToolCacheControl(tools []ToolSchema) []ToolSchema {
	stripped := append([]ToolSchema(nil), tools...)
	for i := range stripped {
		stripped[i].CacheControl = nil
	}
	return stripped
}

func systemCacheControls(blocks []core.SystemBlock) []any {
	controls := make([]any, 0, len(blocks))
	for _, block := range blocks {
		controls = append(controls, block.CacheControl)
	}
	return controls
}

func computeHash(value any) uint32 {
	content, _ := json.Marshal(value)
	h := fnv.New32a()
	_, _ = h.Write(content)
	return h.Sum32()
}

func computePerToolHashes(tools []ToolSchema, names []string) map[string]uint32 {
	hashes := map[string]uint32{}
	for i, tool := range tools {
		name := fmt.Sprintf("__idx_%d", i)
		if i < len(names) {
			name = names[i]
		}
		hashes[name] = computeHash(tool)
	}
	return hashes
}

func systemCharCount(blocks []core.SystemBlock) int {
	total := 0
	for _, block := range blocks {
		total += len(block.Text)
	}
	return total
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func filterMissing(values []string, present map[string]bool) []string {
	missing := []string{}
	for _, value := range values {
		if !present[value] {
			missing = append(missing, value)
		}
	}
	return missing
}

func reasonParts(changes *ChangeSet) []string {
	if changes == nil {
		return nil
	}
	parts := []string{}
	if changes.ModelChanged {
		parts = append(parts, fmt.Sprintf("model changed (%s → %s)", changes.PreviousModel, changes.NewModel))
	}
	if changes.SystemPromptChanged {
		charInfo := ""
		if changes.SystemCharDelta > 0 {
			charInfo = fmt.Sprintf(" (+%d chars)", changes.SystemCharDelta)
		} else if changes.SystemCharDelta < 0 {
			charInfo = fmt.Sprintf(" (%d chars)", changes.SystemCharDelta)
		}
		parts = append(parts, "system prompt changed"+charInfo)
	}
	if changes.ToolSchemasChanged {
		toolDiff := " (tool prompt/schema changed, same tool set)"
		if changes.AddedToolCount > 0 || changes.RemovedToolCount > 0 {
			toolDiff = fmt.Sprintf(" (+%d/-%d tools)", changes.AddedToolCount, changes.RemovedToolCount)
		}
		parts = append(parts, "tools changed"+toolDiff)
	}
	if changes.FastModeChanged {
		parts = append(parts, "fast mode toggled")
	}
	if changes.GlobalCacheStrategyChanged {
		parts = append(parts, fmt.Sprintf("global cache strategy changed (%s → %s)", emptyAsNone(changes.PreviousGlobalCacheStrategy), emptyAsNone(changes.NewGlobalCacheStrategy)))
	}
	if changes.CacheControlChanged && !changes.GlobalCacheStrategyChanged && !changes.SystemPromptChanged {
		parts = append(parts, "cache_control changed (scope or TTL)")
	}
	if changes.BetasChanged {
		diffParts := []string{}
		if len(changes.AddedBetas) > 0 {
			diffParts = append(diffParts, "+"+strings.Join(changes.AddedBetas, ","))
		}
		if len(changes.RemovedBetas) > 0 {
			diffParts = append(diffParts, "-"+strings.Join(changes.RemovedBetas, ","))
		}
		diff := strings.Join(diffParts, " ")
		if diff != "" {
			parts = append(parts, "betas changed ("+diff+")")
		} else {
			parts = append(parts, "betas changed")
		}
	}
	if changes.AutoModeChanged {
		parts = append(parts, "auto mode toggled")
	}
	if changes.OverageChanged {
		parts = append(parts, "overage state changed (TTL latched, no flip)")
	}
	if changes.CachedMCChanged {
		parts = append(parts, "cached microcompact toggled")
	}
	if changes.EffortChanged {
		parts = append(parts, fmt.Sprintf("effort changed (%s → %s)", emptyAsDefault(changes.PreviousEffortValue), emptyAsDefault(changes.NewEffortValue)))
	}
	if changes.ExtraBodyChanged {
		parts = append(parts, "extra body params changed")
	}
	return parts
}

func sanitizeChanges(changes ChangeSet) ChangeSet {
	changes.AddedTools = sanitizeToolNames(changes.AddedTools)
	changes.RemovedTools = sanitizeToolNames(changes.RemovedTools)
	changes.ChangedToolSchemas = sanitizeToolNames(changes.ChangedToolSchemas)
	return changes
}

func sanitizeToolNames(names []string) []string {
	sanitized := make([]string, 0, len(names))
	for _, name := range names {
		if strings.HasPrefix(name, "mcp__") {
			sanitized = append(sanitized, "mcp")
			continue
		}
		sanitized = append(sanitized, name)
	}
	return sanitized
}

func emptyAsNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

func emptyAsDefault(value string) string {
	if value == "" {
		return "default"
	}
	return value
}
