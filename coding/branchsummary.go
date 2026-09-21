package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dat267/pier/ai"
)

// Port of core/compaction/branch-summarization.ts: summarizing the branch being
// left when navigating the session tree.

// BranchSummaryResult is the outcome of branch summarization.
type BranchSummaryResult struct {
	Summary       string
	Usage         *ai.Usage
	ReadFiles     []string
	ModifiedFiles []string
	Aborted       bool
	Error         string
}

// BranchSummaryDetails is the file tracking stored on a branch summary entry.
type BranchSummaryDetails struct {
	ReadFiles     []string `json:"readFiles"`
	ModifiedFiles []string `json:"modifiedFiles"`
}

// BranchPreparation is the summarization input.
type BranchPreparation struct {
	Messages    []ai.Message
	FileOps     *FileOperations
	TotalTokens int
}

// CollectEntriesResult is the collected branch to summarize.
type CollectEntriesResult struct {
	Entries          []SessionEntry
	CommonAncestorID string
	HasAncestor      bool
}

// CollectEntriesForBranchSummary walks from oldLeafID back to the common
// ancestor with targetID, collecting the entries to summarize. Compaction
// boundaries are included: their summaries become context.
func CollectEntriesForBranchSummary(session *SessionManager, oldLeafID, targetID string) CollectEntriesResult {
	if oldLeafID == "" {
		return CollectEntriesResult{}
	}
	oldPath := map[string]bool{}
	for _, entry := range session.GetBranch(oldLeafID) {
		oldPath[entry.ID] = true
	}
	targetPath := session.GetBranch(targetID)

	// targetPath is root-first: iterate backwards for the deepest common node.
	commonAncestorID := ""
	hasAncestor := false
	for index := len(targetPath) - 1; index >= 0; index-- {
		if oldPath[targetPath[index].ID] {
			commonAncestorID = targetPath[index].ID
			hasAncestor = true
			break
		}
	}

	var entries []SessionEntry
	current := oldLeafID
	for current != "" && current != commonAncestorID {
		entry := session.GetEntry(current)
		if entry == nil {
			break
		}
		entries = append(entries, *entry)
		if entry.ParentID == nil {
			current = ""
			continue
		}
		current = *entry.ParentID
	}
	// Reverse to chronological order.
	for left, right := 0, len(entries)-1; left < right; left, right = left+1, right-1 {
		entries[left], entries[right] = entries[right], entries[left]
	}
	return CollectEntriesResult{Entries: entries, CommonAncestorID: commonAncestorID, HasAncestor: hasAncestor}
}

// branchMessageFromEntry extracts the agent message an entry contributes.
func branchMessageFromEntry(entry *SessionEntry) ai.Message {
	switch entry.Type {
	case "message":
		message, err := ai.UnmarshalMessage(entry.Message)
		if err != nil {
			return nil
		}
		// Tool results are skipped: their context is in the assistant's call.
		if _, ok := message.(*ai.ToolResultMessage); ok {
			return nil
		}
		// Custom (non-LLM) messages contribute through the custom arm.
		if custom, ok := message.(*ai.CustomMessage); ok {
			switch custom.Role {
			case RoleBashExecution:
				return nil
			case RoleCustom, RoleBranchSummary, RoleCompactionSummary:
				return custom
			}
			return custom
		}
		return message
	case "custom_message":
		display := entry.Display != nil && *entry.Display
		payload, err := ai.MarshalJSON(map[string]any{
			"role": RoleCustom, "customType": entry.CustomType,
			"content": decodeCustomMessageContent(entry.Content), "display": display,
			"timestamp": entryTimestampMS(entry),
		})
		if err != nil {
			return nil
		}
		return &ai.CustomMessage{Role: RoleCustom, Content: payload, Timestamp: entryTimestampMS(entry)}
	case "branch_summary":
		return &ai.CustomMessage{
			Role: RoleBranchSummary,
			Content: mustMarshalJSON(struct {
				Summary string `json:"summary"`
				FromID  string `json:"fromId"`
			}{Summary: entry.Summary, FromID: entry.FromID}),
			Timestamp: entryTimestampMS(entry),
		}
	case "compaction":
		return &ai.CustomMessage{
			Role: RoleCompactionSummary,
			Content: mustMarshalJSON(struct {
				Summary      string `json:"summary"`
				TokensBefore int64  `json:"tokensBefore"`
			}{Summary: entry.Summary, TokensBefore: entry.TokensBefore}),
			Timestamp: entryTimestampMS(entry),
		}
	default:
		return nil
	}
}

func decodeCustomMessageContent(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`""`)
	}
	return raw
}

func entryTimestampMS(entry *SessionEntry) int64 {
	if entry.Timestamp == "" {
		return time.Now().UnixMilli()
	}
	parsed, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
	if err != nil {
		return time.Now().UnixMilli()
	}
	return parsed.UnixMilli()
}

// PrepareBranchEntries selects entries for summarization within a token budget,
// walking newest to oldest, and collects file operations (including cumulative
// tracking from nested branch summaries).
func PrepareBranchEntries(entries []SessionEntry, tokenBudget int) BranchPreparation {
	var messages []ai.Message
	fileOps := CreateFileOps()
	totalTokens := 0

	// First pass: file ops from every entry so nested summaries contribute.
	for index := range entries {
		entry := &entries[index]
		if entry.Type != "branch_summary" || entry.FromHook != nil && *entry.FromHook || len(entry.Details) == 0 {
			continue
		}
		var details BranchSummaryDetails
		if err := json.Unmarshal(entry.Details, &details); err != nil {
			continue
		}
		for _, file := range details.ReadFiles {
			fileOps.Read[file] = true
		}
		for _, file := range details.ModifiedFiles {
			fileOps.Edited[file] = true
		}
	}

	// Second pass: newest to oldest until the budget is exhausted.
	for index := len(entries) - 1; index >= 0; index-- {
		entry := &entries[index]
		message := branchMessageFromEntry(entry)
		if message == nil {
			continue
		}
		ExtractFileOpsFromMessage(message, fileOps)
		tokens := EstimateAgentMessageTokens(message)
		if tokenBudget > 0 && totalTokens+tokens > tokenBudget {
			// A summary entry still fits when there is room enough, because it
			// carries important context.
			if entry.Type == "compaction" || entry.Type == "branch_summary" {
				if float64(totalTokens) < float64(tokenBudget)*0.9 {
					messages = append([]ai.Message{message}, messages...)
					totalTokens += tokens
				}
			}
			break
		}
		messages = append([]ai.Message{message}, messages...)
		totalTokens += tokens
	}
	return BranchPreparation{Messages: messages, FileOps: fileOps, TotalTokens: totalTokens}
}

// Branch summary prompt text (core/compaction/branch-summarization.ts).
const branchSummaryPreamble = `The user explored a different conversation branch before returning here.
Summary of that exploration:

`

const branchSummaryPrompt = `Create a structured summary of this conversation branch for context when returning later.

Use this EXACT format:

## Goal
[What was the user trying to accomplish in this branch?]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Work that was started but not finished]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [What should happen next to continue this work]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// GenerateBranchSummaryOptions configure branch summarization.
type GenerateBranchSummaryOptions struct {
	Model               *ai.Model
	APIKey              string
	Headers             ai.ProviderHeaders
	Env                 map[string]string
	Ctx                 context.Context
	CustomInstructions  string
	ReplaceInstructions bool
	ReserveTokens       int
	StreamFn            StreamFnFn
	Retry               *ai.RetryPolicy
	Callbacks           *SummarizationCallbacks
}

// GenerateBranchSummary summarizes the abandoned branch entries.
func GenerateBranchSummary(entries []SessionEntry, options GenerateBranchSummaryOptions) (*BranchSummaryResult, error) {
	reserveTokens := options.ReserveTokens
	if reserveTokens == 0 {
		reserveTokens = 16384
	}
	contextWindow := options.Model.ContextWindow
	if contextWindow <= 0 {
		contextWindow = 128000
	}
	tokenBudget := int(contextWindow) - reserveTokens

	preparation := PrepareBranchEntries(entries, tokenBudget)
	if len(preparation.Messages) == 0 {
		return &BranchSummaryResult{Summary: "No content to summarize"}, nil
	}

	llmMessages := ConvertToLlm(preparation.Messages)
	conversationText := SerializeConversation(llmMessages)

	instructions := branchSummaryPrompt
	if options.ReplaceInstructions && options.CustomInstructions != "" {
		instructions = options.CustomInstructions
	} else if options.CustomInstructions != "" {
		instructions = branchSummaryPrompt + "\n\nAdditional focus: " + options.CustomInstructions
	}
	promptText := "<conversation>\n" + conversationText + "\n</conversation>\n\n" + instructions

	summarizationMessages := []ai.Message{&ai.UserMessage{
		Content:   ai.StringOrBlocks{Blocks: ai.ContentList{ai.TextContent{Text: promptText}}},
		Timestamp: time.Now().UnixMilli(),
	}}
	maxTokens := int64(4096)
	if options.Model.MaxTokens > 0 && options.Model.MaxTokens < maxTokens {
		maxTokens = options.Model.MaxTokens
	}

	systemPrompt := SummarizationSystemPrompt
	transcript := ai.NormalizeContext(ai.Context{
		SystemPrompt: &systemPrompt,
		Messages:     summarizationMessages,
	})
	requestOptions := &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			APIKey: options.APIKey, Headers: options.Headers,
			MaxTokens: intPtr(int(maxTokens)),
		},
	}
	if len(options.Env) > 0 {
		requestOptions.Env = options.Env
	}
	if options.Ctx != nil {
		requestOptions.Ctx = options.Ctx
	}

	response, err := CompleteSummarization(options.Model, transcript, requestOptions, options.StreamFn, options.Retry, options.Callbacks)
	if err != nil {
		// A cancelled context is an abort, not a failure (upstream's abort signal
		// surfaces as result.aborted; D132).
		if options.Ctx != nil && options.Ctx.Err() != nil {
			return &BranchSummaryResult{Aborted: true}, nil
		}
		return nil, err
	}
	if response.StopReason == ai.StopAborted {
		return &BranchSummaryResult{Aborted: true}, nil
	}
	if failure := GetSummarizationFailure(response, "Branch summarization"); failure != "" {
		return &BranchSummaryResult{Error: failure}, nil
	}
	for _, block := range response.Content {
		if _, ok := block.(ai.ToolCall); ok {
			return &BranchSummaryResult{Error: "Branch summarization attempted to call a tool"}, nil
		}
	}

	summary := contentTextOf(response.Content)
	summary = branchSummaryPreamble + summary

	readFiles, modifiedFiles := ComputeFileLists(preparation.FileOps)
	summary += FormatFileOperations(readFiles, modifiedFiles)
	if summary == "" {
		summary = "No summary generated"
	}
	usage := response.Usage
	return &BranchSummaryResult{
		Summary: summary, Usage: &usage, ReadFiles: readFiles, ModifiedFiles: modifiedFiles,
	}, nil
}

// NavigateTreeOptions configure tree navigation.
type NavigateTreeOptions struct {
	Summarize           bool
	CustomInstructions  string
	ReplaceInstructions bool
	Label               string
}

// NavigateTreeResult is the outcome of tree navigation.
type NavigateTreeResult struct {
	EditorText   string
	Cancelled    bool
	Aborted      bool
	SummaryEntry *SessionEntry
}

// NavigateTree moves the session leaf to another node, optionally summarizing
// the abandoned branch (port of navigateTree; D41 omits the extension
// session_before_tree/session_tree hooks).
func (s *AgentSession) NavigateTree(ctx context.Context, targetID string, options NavigateTreeOptions) (*NavigateTreeResult, error) {
	if s.IsStreaming() {
		return nil, fmt.Errorf("Wait for the current response to finish before navigating the session tree.")
	}
	if s.IsCompacting() {
		return nil, fmt.Errorf("Wait for the current compaction or tree navigation to finish before navigating the session tree.")
	}

	oldLeafID := ""
	if leaf := s.Sessions.GetLeafID(); leaf != nil {
		oldLeafID = *leaf
	}
	if targetID == oldLeafID {
		return &NavigateTreeResult{}, nil
	}
	if options.Summarize && !s.HasModel() {
		return nil, fmt.Errorf("No model available for summarization")
	}
	targetEntry := s.Sessions.GetEntry(targetID)
	if targetEntry == nil {
		return nil, fmt.Errorf("Entry %s not found", targetID)
	}

	// Set up the branch-summary abort controller (upstream sets it for the whole
	// navigation, so isCompacting reports the navigation as busy; D132).
	summaryCtx, cancel := context.WithCancel(ctx)
	s.control.stateMu.Lock()
	s.control.branchSummaryOpen = true
	s.control.branchSummaryCancel = cancel
	s.control.stateMu.Unlock()
	defer func() {
		s.control.stateMu.Lock()
		s.control.branchSummaryOpen = false
		s.control.branchSummaryCancel = nil
		s.control.stateMu.Unlock()
		cancel()
	}()

	collection := CollectEntriesForBranchSummary(s.Sessions, oldLeafID, targetID)

	var summaryText string
	var summaryDetails json.RawMessage
	var summaryUsage *ai.Usage
	if options.Summarize && len(collection.Entries) > 0 {
		model := s.Model()
		request := SummarizationRequestAuth{Model: model}
		if s.control != nil && s.control.ModelRuntime != nil {
			resolution, err := s.control.ModelRuntime.GetAuthForModel(model, nil)
			if err != nil {
				return nil, err
			}
			if resolution != nil {
				request.Headers = resolution.Auth.Headers
				request.Env = resolution.Env
				request.APIKey = resolution.Auth.APIKey
				if resolution.Auth.BaseURL != "" {
					copied := *model
					copied.BaseURL = resolution.Auth.BaseURL
					request.Model = &copied
				}
			}
		}
		reserveTokens := 16384
		if s.control != nil && s.control.Settings != nil {
			reserveTokens = int(s.control.Settings.GetBranchSummarySettings().ReserveTokens)
		}
		result, err := GenerateBranchSummary(collection.Entries, GenerateBranchSummaryOptions{
			Model: request.Model, APIKey: request.APIKey, Headers: request.Headers, Env: request.Env,
			Ctx: summaryCtx, CustomInstructions: options.CustomInstructions,
			ReplaceInstructions: options.ReplaceInstructions, ReserveTokens: reserveTokens,
			StreamFn:  s.compactionStreamFn(),
			Callbacks: s.summarizationRetryCallbacks("branchSummary"),
		})
		if err != nil {
			return nil, err
		}
		if result.Aborted {
			return &NavigateTreeResult{Cancelled: true, Aborted: true}, nil
		}
		if result.Error != "" {
			return nil, fmt.Errorf("%s", result.Error)
		}
		summaryText = result.Summary
		summaryUsage = result.Usage
		details, err := ai.MarshalJSON(BranchSummaryDetails{
			ReadFiles: result.ReadFiles, ModifiedFiles: result.ModifiedFiles,
		})
		if err == nil {
			summaryDetails = details
		}
	}

	// The new leaf depends on the target kind.
	newLeafID := targetID
	editorText := ""
	if targetEntry.Type == "message" {
		message, err := ai.UnmarshalMessage(targetEntry.Message)
		if err == nil {
			if user, ok := message.(*ai.UserMessage); ok {
				if targetEntry.ParentID != nil {
					newLeafID = *targetEntry.ParentID
				} else {
					newLeafID = ""
				}
				editorText = contentTextJoinedNoSep(user.Content)
			}
		}
	} else if targetEntry.Type == "custom_message" {
		if targetEntry.ParentID != nil {
			newLeafID = *targetEntry.ParentID
		} else {
			newLeafID = ""
		}
		var content ai.StringOrBlocks
		if err := json.Unmarshal(targetEntry.Content, &content); err == nil {
			editorText = contentTextJoinedNoSep(content)
		}
	}

	var summaryEntry *SessionEntry
	if summaryText != "" {
		summaryID := s.Sessions.BranchWithSummary(newLeafID, summaryText, summaryDetails, false, summaryUsage)
		summaryEntry = s.Sessions.GetEntry(summaryID)
		if options.Label != "" {
			label := options.Label
			if _, err := s.Sessions.AppendLabelChange(summaryID, &label); err != nil {
				return nil, err
			}
		}
	} else if newLeafID == "" {
		s.Sessions.ResetLeaf()
	} else {
		if err := s.Sessions.Branch(newLeafID); err != nil {
			return nil, err
		}
	}
	if options.Label != "" && summaryText == "" {
		label := options.Label
		if _, err := s.Sessions.AppendLabelChange(targetID, &label); err != nil {
			return nil, err
		}
	}

	// Refresh agent state from the new branch.
	sessionContext := s.Sessions.BuildSessionContext()
	s.Agent.SetMessages(sessionContext.Messages)
	s.RestoreToolsFromTranscript()

	return &NavigateTreeResult{EditorText: editorText, SummaryEntry: summaryEntry}, nil
}

// SummarizationRequestAuth is the resolved summarization request auth.
type SummarizationRequestAuth struct {
	Model   *ai.Model
	APIKey  string
	Headers ai.ProviderHeaders
	Env     map[string]string
}

// compactionStreamFn exposes the compaction stream function: the explicit
// CompactionStreamFn when configured, otherwise the session's stream function
// (upstream uses the agent's stream function).
func (s *AgentSession) compactionStreamFn() StreamFnFn {
	if s.CompactionStreamFn != nil {
		return s.CompactionStreamFn
	}
	if s.streamFn != nil {
		return adaptStreamFn(s.streamFn)
	}
	return nil
}

// RestoreToolsFromTranscript re-declares the tool set the branch last used,
// taken from the transcript's current system message (port of
// _restoreToolsFromTranscript).
func (s *AgentSession) RestoreToolsFromTranscript() {
	if s.control == nil {
		return
	}
	current := ai.GetCurrentSystemMessage(s.Sessions.BuildSessionContext().Messages)
	if current == nil {
		return
	}
	var toolNames []string
	var tools []agentTool
	for _, tool := range current.ToolsAdded {
		entry, ok := s.control.Tools[tool.Name]
		if !ok {
			continue
		}
		toolNames = append(toolNames, tool.Name)
		tools = append(tools, entry.Tool)
	}
	s.Agent.SetTools(tools)
	s.RebuildSystemPrompt(toolNames)
}
