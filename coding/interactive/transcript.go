package interactive

import (
	"encoding/json"

	"github.com/dat267/gpi/ai"
	"github.com/dat267/gpi/coding"
	"github.com/dat267/gpi/tui"
)

// Port of the transcript-rendering half of
// src/modes/interactive/interactive-mode.ts: addMessageToChat,
// renderSessionItems/Entries, the cache/compaction/thinking notices and the
// status lines.
//
// Divergences: the extension entry/message renderers are seams that return nil
// because extension mechanics are out of scope (D41/D112); the tool renderer
// registry is injected as a resolver.

// RenderSessionItem is one transcript render input.
type RenderSessionItem struct {
	Message     ai.Message
	CustomEntry *coding.SessionEntry
	UsageEntry  *coding.SessionEntry
	CostNotice  *CompactionCostNotice
}

// TranscriptSession is the session surface the transcript needs.
type TranscriptSession interface {
	// GetToolRenderers resolves the renderers for a tool (nil for the fallback).
	GetToolRenderers(toolName string) *ToolRenderers
	// GetRetryAttempt returns the current retry attempt count.
	GetRetryAttempt() int
	// GetModelPriceSource returns the cache-price source.
	GetModelPriceSource() coding.ModelPriceSource
}

// ManagedToolStatus is a managed-tool status update.
type ManagedToolStatus struct {
	Type    string // "info" | "warning"
	Message string
}

// TranscriptRenderer renders messages and session entries into a chat
// container.
type TranscriptRenderer struct {
	Chat        *tui.Container
	UI          tui.RenderRequester
	Settings    *coding.SettingsManager
	Session     TranscriptSession
	SessionInfo *coding.SessionManager

	Footer *FooterComponent
	Editor *CustomEditor

	ToolOutputExpanded  bool
	OutputPad           int
	HideThinkingBlock   bool
	HiddenThinkingLabel string

	MarkdownTheme *tui.MarkdownTheme
	Transformers  []MarkdownTransformer

	// StreamingComponent is the in-progress assistant component (custom
	// entries are inserted before it).
	StreamingComponent tui.Component

	// EntryRenderer resolves extension entry renderers (out of scope).
	EntryRenderer func(customType string) EntryRenderer
	// MessageRenderer resolves extension message renderers (out of scope).
	MessageRenderer func(customType string) MessageRenderer

	pendingTools             map[string]*ToolExecutionComponent
	lastStatusSpacer         *tui.Spacer
	lastStatusText           *tui.Text
	managedToolStatusStarted bool
}

// NewTranscriptRenderer creates the renderer.
func NewTranscriptRenderer(chat *tui.Container, ui tui.RenderRequester, settings *coding.SettingsManager, session TranscriptSession, sessionInfo *coding.SessionManager) *TranscriptRenderer {
	return &TranscriptRenderer{
		Chat:         chat,
		UI:           ui,
		Settings:     settings,
		Session:      session,
		SessionInfo:  sessionInfo,
		pendingTools: map[string]*ToolExecutionComponent{},
	}
}

func (r *TranscriptRenderer) requestRender() {
	if r.UI != nil {
		r.UI.RequestRender(false)
	}
}

// GetUserMessageText extracts the text of a user message.
func (r *TranscriptRenderer) GetUserMessageText(message ai.Message) string {
	user, ok := message.(*ai.UserMessage)
	if !ok {
		return ""
	}
	return ai.ContentText(user.Content, "")
}

// ShowManagedToolStatus shows a managed-tool status update in the chat.
func (r *TranscriptRenderer) ShowManagedToolStatus(status ManagedToolStatus) {
	theme := ActiveTheme()
	if !r.managedToolStatusStarted {
		r.Chat.AddChild(tui.NewSpacer(1))
		r.managedToolStatusStarted = true
	}
	message := status.Message
	if status.Type == "warning" {
		message = "Warning: " + status.Message
	}
	color := "dim"
	if status.Type == "warning" {
		color = "warning"
	}
	r.Chat.AddChild(tui.NewText(theme.Fg(color, message), 1, 0, nil))
	r.lastStatusSpacer = nil
	r.lastStatusText = nil
	r.requestRender()
}

// ShowStatus shows a status message, replacing a previous consecutive status.
func (r *TranscriptRenderer) ShowStatus(message string) {
	theme := ActiveTheme()
	children := r.Chat.Children
	var last, secondLast tui.Component
	if len(children) > 0 {
		last = children[len(children)-1]
	}
	if len(children) > 1 {
		secondLast = children[len(children)-2]
	}

	if last != nil && secondLast != nil && last == tui.Component(r.lastStatusText) && secondLast == tui.Component(r.lastStatusSpacer) {
		r.lastStatusText.SetText(theme.Fg("dim", message))
		r.requestRender()
		return
	}

	spacer := tui.NewSpacer(1)
	text := tui.NewText(theme.Fg("dim", message), 1, 0, nil)
	r.Chat.AddChild(spacer)
	r.Chat.AddChild(text)
	r.lastStatusSpacer = spacer
	r.lastStatusText = text
	r.requestRender()
}

// AddCustomEntryToChat renders a custom session entry.
func (r *TranscriptRenderer) AddCustomEntryToChat(entry *coding.SessionEntry) {
	if r.EntryRenderer == nil {
		return
	}
	renderer := r.EntryRenderer(entry.CustomType)
	if renderer == nil {
		return
	}
	component := NewCustomEntryComponent(CustomEntry{CustomType: entry.CustomType, Data: entry.Raw()}, renderer)
	component.SetExpanded(r.ToolOutputExpanded)
	if !component.HasContent() {
		return
	}
	// Insert before the streaming component when present.
	if r.StreamingComponent != nil {
		for index, child := range r.Chat.Children {
			if child == r.StreamingComponent {
				r.Chat.Children = append(r.Chat.Children[:index],
					append([]tui.Component{component}, r.Chat.Children[index:]...)...)
				return
			}
		}
	}
	r.Chat.AddChild(component)
}

// AddMessageToChat renders one agent message.
func (r *TranscriptRenderer) AddMessageToChat(message ai.Message, populateHistory bool) {
	theme := ActiveTheme()
	switch typed := message.(type) {
	case *ai.CustomMessage:
		switch typed.Role {
		case coding.RoleBashExecution:
			fields := decodeBashExecutionMessage(typed.Content)
			component := NewBashExecutionComponent(fields.Command, r.UI, fields.ExcludeFromContext)
			if fields.Output != "" {
				component.AppendOutput(fields.Output)
			}
			var truncation *coding.TruncationResult
			if fields.Truncated {
				truncation = &coding.TruncationResult{Truncated: true}
			}
			fullOutputPath := ""
			if fields.FullOutputPath != nil {
				fullOutputPath = *fields.FullOutputPath
			}
			component.SetComplete(fields.ExitCode, fields.Cancelled, truncation, fullOutputPath)
			r.Chat.AddChild(component)
		case coding.RoleCompactionSummary:
			summary, tokensBefore := decodeCompactionSummary(typed.Content)
			r.Chat.AddChild(tui.NewSpacer(1))
			component := NewCompactionSummaryMessageComponent(summary, tokensBefore, r.MarkdownTheme)
			component.SetExpanded(r.ToolOutputExpanded)
			r.Chat.AddChild(component)
		case coding.RoleBranchSummary:
			summary := decodeBranchSummary(typed.Content)
			r.Chat.AddChild(tui.NewSpacer(1))
			component := NewBranchSummaryMessageComponent(summary, r.MarkdownTheme)
			component.SetExpanded(r.ToolOutputExpanded)
			r.Chat.AddChild(component)
		default:
			customType, content, display := decodeCustomMessage(typed.Content)
			if !display {
				return
			}
			var renderer MessageRenderer
			if r.MessageRenderer != nil {
				renderer = r.MessageRenderer(customType)
			}
			component := NewCustomMessageComponent(CustomMessagePayload{CustomType: customType, Text: content},
				renderer, r.MarkdownTheme, r.OutputPad)
			component.SetExpanded(r.ToolOutputExpanded)
			r.Chat.AddChild(component)
		}
	case *ai.UserMessage:
		textContent := r.GetUserMessageText(message)
		if textContent == "" {
			return
		}
		if len(r.Chat.Children) > 0 {
			r.Chat.AddChild(tui.NewSpacer(1))
		}
		skillBlock := coding.ParseSkillBlock(textContent)
		if skillBlock != nil {
			component := NewSkillInvocationMessageComponent(skillBlock.Name, skillBlock.Content, r.MarkdownTheme)
			component.SetExpanded(r.ToolOutputExpanded)
			r.Chat.AddChild(component)
			if skillBlock.UserMessage != "" {
				r.Chat.AddChild(tui.NewSpacer(1))
				r.Chat.AddChild(NewUserMessageComponent(skillBlock.UserMessage, r.MarkdownTheme, r.OutputPad, r.Transformers))
			}
		} else {
			r.Chat.AddChild(NewUserMessageComponent(textContent, r.MarkdownTheme, r.OutputPad, r.Transformers))
		}
		if populateHistory && r.Editor != nil {
			r.Editor.AddToHistory(textContent)
		}
	case *ai.AssistantMessage:
		component := NewAssistantMessageComponent(typed, r.HideThinkingBlock, r.MarkdownTheme,
			r.HiddenThinkingLabel, r.OutputPad, r.Transformers)
		r.Chat.AddChild(component)
	case *ai.ToolResultMessage:
		// Tool results render inline with their tool calls.
	case *ai.SystemMessage:
		// Not rendered.
	default:
		_ = theme
	}
}

func decodeBashExecutionMessage(content json.RawMessage) struct {
	Command            string  `json:"command"`
	Output             string  `json:"output"`
	ExitCode           *int    `json:"exitCode"`
	Cancelled          bool    `json:"cancelled"`
	Truncated          bool    `json:"truncated"`
	FullOutputPath     *string `json:"fullOutputPath"`
	ExcludeFromContext bool    `json:"excludeFromContext"`
} {
	var fields struct {
		Command            string  `json:"command"`
		Output             string  `json:"output"`
		ExitCode           *int    `json:"exitCode"`
		Cancelled          bool    `json:"cancelled"`
		Truncated          bool    `json:"truncated"`
		FullOutputPath     *string `json:"fullOutputPath"`
		ExcludeFromContext bool    `json:"excludeFromContext"`
	}
	if len(content) > 0 {
		_ = json.Unmarshal(content, &fields)
	}
	return fields
}

func decodeCompactionSummary(content json.RawMessage) (string, int64) {
	var fields struct {
		Summary      string `json:"summary"`
		TokensBefore int64  `json:"tokensBefore"`
	}
	if len(content) > 0 {
		_ = json.Unmarshal(content, &fields)
	}
	return fields.Summary, fields.TokensBefore
}

func decodeBranchSummary(content json.RawMessage) string {
	var fields struct {
		Summary string `json:"summary"`
	}
	if len(content) > 0 {
		_ = json.Unmarshal(content, &fields)
	}
	return fields.Summary
}

func decodeCustomMessage(content json.RawMessage) (string, string, bool) {
	var fields struct {
		CustomType string          `json:"customType"`
		Content    json.RawMessage `json:"content"`
		Display    *bool           `json:"display"`
	}
	if len(content) > 0 {
		_ = json.Unmarshal(content, &fields)
	}
	text := ""
	if len(fields.Content) > 0 {
		var plain string
		if err := json.Unmarshal(fields.Content, &plain); err == nil {
			text = plain
		} else {
			text = ""
		}
	}
	display := fields.Display == nil || *fields.Display
	return fields.CustomType, text, display
}

// RenderSessionItems renders a list of transcript items.
func (r *TranscriptRenderer) RenderSessionItems(items []RenderSessionItem, updateFooter bool, populateHistory bool) {
	r.pendingTools = map[string]*ToolExecutionComponent{}
	renderedPendingTools := map[string]*ToolExecutionComponent{}

	var cacheMisses map[*ai.AssistantMessage]coding.CacheMiss
	if r.Settings != nil && r.Settings.GetShowCacheMissNotices() && r.SessionInfo != nil && r.Session != nil {
		cacheMisses = map[*ai.AssistantMessage]coding.CacheMiss{}
		for _, entry := range coding.CollectCacheMisses(r.SessionInfo.GetEntries(), r.Session.GetModelPriceSource()) {
			cacheMisses[entry.Message] = entry.Miss
		}
	}

	if updateFooter && r.Footer != nil {
		r.Footer.Invalidate()
	}

	for index := range items {
		item := &items[index]
		if item.CustomEntry != nil {
			r.AddCustomEntryToChat(item.CustomEntry)
			continue
		}
		if item.UsageEntry != nil {
			r.AddCacheWarmingUsage(item.UsageEntry)
			continue
		}
		if item.CostNotice != nil {
			r.AddCompactionCostNotice(*item.CostNotice)
			continue
		}

		assistant, isAssistant := item.Message.(*ai.AssistantMessage)
		if isAssistant {
			r.AddMessageToChat(item.Message, populateHistory)
			for _, content := range assistant.Content {
				toolCall, ok := content.(ai.ToolCall)
				if !ok {
					if pointer, isPointer := content.(*ai.ToolCall); isPointer {
						toolCall, ok = *pointer, true
					}
				}
				if !ok {
					continue
				}
				var definition *ToolRenderers
				if r.Session != nil {
					definition = r.Session.GetToolRenderers(toolCall.Name)
				}
				options := ToolExecutionOptions{}
				if r.Settings != nil {
					showImages := r.Settings.GetShowImages()
					options.ShowImages = &showImages
					options.ImageWidthCells = r.Settings.GetImageWidthCells()
				}
				cwd := ""
				if r.SessionInfo != nil {
					cwd = r.SessionInfo.GetCwd()
				}
				component := NewToolExecutionComponent(toolCall.Name, toolCall.ID, toolCall.Arguments,
					options, definition, r.UI, cwd)
				component.SetExpanded(r.ToolOutputExpanded)
				r.Chat.AddChild(component)

				if assistant.StopReason == ai.StopAborted || assistant.StopReason == ai.StopError {
					errorMessage := "Error"
					if assistant.StopReason == ai.StopAborted {
						retryAttempt := 0
						if r.Session != nil {
							retryAttempt = r.Session.GetRetryAttempt()
						}
						if retryAttempt > 0 {
							suffix := "s"
							if retryAttempt == 1 {
								suffix = ""
							}
							errorMessage = "Aborted after " + itoa(retryAttempt) + " retry attempt" + suffix
						} else {
							errorMessage = "Operation aborted"
						}
					} else if assistant.ErrorMessage != nil && *assistant.ErrorMessage != "" {
						errorMessage = *assistant.ErrorMessage
					}
					component.UpdateResult(&SortToolResultContent{
						Content: []ToolResultContent{{Type: "text", Text: errorMessage}},
						IsError: true,
					}, false)
				} else {
					renderedPendingTools[toolCall.ID] = component
				}
			}
			if assistant.StopReason != ai.StopAborted && assistant.StopReason != ai.StopError {
				if miss, ok := cacheMisses[assistant]; ok {
					r.AddCacheMissNotice(miss)
				}
			}
			continue
		}

		if toolResult, ok := item.Message.(*ai.ToolResultMessage); ok {
			if component, ok := renderedPendingTools[toolResult.ToolCallID]; ok {
				component.UpdateResult(sortToolResultFromMessage(toolResult), false)
				delete(renderedPendingTools, toolResult.ToolCallID)
			}
			continue
		}

		r.AddMessageToChat(item.Message, populateHistory)
	}

	for toolCallID, component := range renderedPendingTools {
		r.pendingTools[toolCallID] = component
	}
	r.requestRender()
}

// RenderSessionEntries renders compaction-aware session entries.
func (r *TranscriptRenderer) RenderSessionEntries(entries []coding.SessionEntry, updateFooter bool, populateHistory bool) {
	var items []RenderSessionItem
	for index := range entries {
		entry := &entries[index]
		if entry.Type == "custom" || (entry.Type == "usage" && entry.Kind == "cache_warm") {
			if entry.Type == "custom" {
				items = append(items, RenderSessionItem{CustomEntry: entry})
			} else {
				items = append(items, RenderSessionItem{UsageEntry: entry})
			}
			continue
		}
		messages := coding.SessionEntryToContextMessages(entry)
		if (entry.Type == "compaction" || entry.Type == "branch_summary") && entry.Usage != nil && len(messages) > 0 {
			for _, message := range messages {
				items = append(items, RenderSessionItem{Message: message})
			}
			items = append(items, RenderSessionItem{CostNotice: &CompactionCostNotice{
				Type: "compaction_cost", Kind: entry.Type, Usage: *entry.Usage,
			}})
			continue
		}
		for _, message := range messages {
			items = append(items, RenderSessionItem{Message: message})
		}
	}
	r.RenderSessionItems(items, updateFooter, populateHistory)
}

// AddCacheWarmingUsage renders a cache-warming usage entry.
func (r *TranscriptRenderer) AddCacheWarmingUsage(entry *coding.SessionEntry) {
	if r.Settings == nil || !r.Settings.GetShowCacheMissNotices() {
		return
	}
	theme := ActiveTheme()
	r.Chat.AddChild(tui.NewSpacer(1))
	r.Chat.AddChild(tui.NewText(theme.Fg("dim", coding.FormatCacheWarmingUsage(entry)), 1, 0, nil))
}

// AddCompactionCostNotice renders a compaction/branch-summary billing notice.
func (r *TranscriptRenderer) AddCompactionCostNotice(notice CompactionCostNotice) {
	if r.Settings == nil || !r.Settings.GetShowCacheMissNotices() {
		return
	}
	theme := ActiveTheme()
	tokens := notice.Usage.Input + notice.Usage.Output + notice.Usage.CacheRead + notice.Usage.CacheWrite
	cost := ""
	if notice.Usage.Cost.Total >= 0.01 {
		cost = " (~$" + formatFixed(notice.Usage.Cost.Total, 2) + ")"
	}
	label := "Branch summary"
	if notice.Kind == "compaction" {
		label = "Compaction"
	}
	r.Chat.AddChild(tui.NewSpacer(1))
	r.Chat.AddChild(tui.NewText(theme.Fg("warning",
		label+": "+FormatTokens(tokens)+" tokens billed"+cost), 1, 0, nil))
}

// CountDroppedThinkingBlocks counts the dropped-thinking diagnostics.
func CountDroppedThinkingBlocks(message *ai.AssistantMessage) int {
	count := 0
	for _, diagnostic := range message.Diagnostics {
		if diagnostic.Type != "anthropic_input_transformations" {
			continue
		}
		var details struct {
			Transformations []map[string]any `json:"transformations"`
		}
		if len(diagnostic.Details) == 0 {
			continue
		}
		if err := json.Unmarshal(diagnostic.Details, &details); err != nil {
			continue
		}
		for _, transformation := range details.Transformations {
			if transformation["type"] == "thinking_dropped" {
				count++
			}
		}
	}
	return count
}

// MaybeShowThinkingDropNotice shows a notice when new thinking blocks dropped.
func (r *TranscriptRenderer) MaybeShowThinkingDropNotice(message *ai.AssistantMessage) {
	if r.Settings == nil || !r.Settings.GetShowCacheMissNotices() || r.SessionInfo == nil {
		return
	}
	droppedCount := CountDroppedThinkingBlocks(message)
	if droppedCount == 0 {
		return
	}

	previousDroppedCount := 0
	branch := r.SessionInfo.GetBranch("")
	for index := len(branch) - 1; index >= 0; index-- {
		entry := branch[index]
		if entry.Type != "message" || len(entry.Message) == 0 {
			continue
		}
		decoded, err := ai.UnmarshalMessage(entry.Message)
		if err != nil {
			continue
		}
		if assistant, ok := decoded.(*ai.AssistantMessage); ok {
			previousDroppedCount = CountDroppedThinkingBlocks(assistant)
			break
		}
	}
	if droppedCount <= previousDroppedCount {
		return
	}

	theme := ActiveTheme()
	noun := "thinking blocks"
	if droppedCount == 1 {
		noun = "thinking block"
	}
	r.Chat.AddChild(tui.NewSpacer(1))
	r.Chat.AddChild(tui.NewText(theme.Fg("warning",
		"Anthropic dropped "+itoa(droppedCount)+" "+noun+" (details in session)"), 1, 0, nil))
}

// MaybeShowCacheMissNotice shows a notice for a significant cache miss.
func (r *TranscriptRenderer) MaybeShowCacheMissNotice(message *ai.AssistantMessage) {
	if r.Settings == nil || !r.Settings.GetShowCacheMissNotices() || r.SessionInfo == nil || r.Session == nil {
		return
	}
	miss, ok := coding.DetectCacheMiss(r.SessionInfo.GetEntries(), message, r.Session.GetModelPriceSource())
	if ok {
		r.AddCacheMissNotice(miss)
	}
}

// AddCacheMissNotice renders a cache-miss notice when it is significant.
func (r *TranscriptRenderer) AddCacheMissNotice(miss coding.CacheMiss) {
	if miss.MissedTokens < 20000 && miss.MissedCost < 0.1 {
		return
	}
	cost := ""
	if miss.MissedCost >= 0.01 {
		cost = " (~$" + formatFixed(miss.MissedCost, 2) + ")"
	}
	reBilled := FormatTokens(miss.MissedTokens) + " tokens re-billed" + cost
	label := "Cache miss"
	if miss.ModelChanged {
		label = "Cache miss after model switch"
	} else if miss.IdleMs >= coding.CacheTTLMs {
		label = "Cache miss after " + itoa(int((miss.IdleMs+30000)/60000)) + "m idle"
	}
	theme := ActiveTheme()
	r.Chat.AddChild(tui.NewSpacer(1))
	r.Chat.AddChild(tui.NewText(theme.Fg("warning", label+": "+reBilled), 1, 0, nil))
}

// RenderInitialMessages renders the initial transcript and the compaction
// status.
func (r *TranscriptRenderer) RenderInitialMessages() {
	if r.SessionInfo == nil {
		return
	}
	entries := r.SessionInfo.BuildContextEntriesForLeaf()
	r.RenderSessionEntries(entries, true, true)

	allEntries := r.SessionInfo.GetEntries()
	compactionCount := 0
	for _, entry := range allEntries {
		if entry.Type == "compaction" {
			compactionCount++
		}
	}
	if compactionCount > 0 {
		times := itoa(compactionCount) + " times"
		if compactionCount == 1 {
			times = "1 time"
		}
		r.ShowStatus("Session compacted " + times)
	}
}

// sortToolResultFromMessage converts a tool-result message into the render
// payload.
func sortToolResultFromMessage(message *ai.ToolResultMessage) *SortToolResultContent {
	result := &SortToolResultContent{IsError: message.IsError}
	if message.Details != nil {
		result.Details = message.Details
	}
	for _, block := range message.Content {
		if text, ok := block.(ai.TextContent); ok {
			result.Content = append(result.Content, ToolResultContent{Type: "text", Text: text.Text})
		}
	}
	return result
}

// PendingTools returns the pending tool components (test helper).
func (r *TranscriptRenderer) PendingTools() map[string]*ToolExecutionComponent { return r.pendingTools }
