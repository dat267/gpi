package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/dat267/gpi/ai"
)

// Port of core/compaction/compaction.ts and compaction/utils.ts: context
// compaction for long sessions (pure functions; the session manager does I/O).

// CompactionSettings tune automatic compaction.
type CompactionSettings struct {
	Enabled          bool
	ReserveTokens    int64
	KeepRecentTokens int64
}

// DefaultCompactionSettings are the upstream defaults.
var DefaultCompactionSettings = CompactionSettings{
	Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 20000,
}

// CompactionDetails is the file-tracking payload in CompactionEntry.details.
type CompactionDetails struct {
	ReadFiles     []string `json:"readFiles"`
	ModifiedFiles []string `json:"modifiedFiles"`
}

// FileOperations tracks file usage across the summarized span.
type FileOperations struct {
	Read    map[string]bool
	Written map[string]bool
	Edited  map[string]bool
}

// CreateFileOps builds an empty set.
func CreateFileOps() *FileOperations {
	return &FileOperations{Read: map[string]bool{}, Written: map[string]bool{}, Edited: map[string]bool{}}
}

// ExtractFileOpsFromMessage extracts file paths from a tool call
// (read/write/edit) in an assistant message.
func ExtractFileOpsFromMessage(message ai.Message, fileOps *FileOperations) {
	assistant, ok := message.(*ai.AssistantMessage)
	if !ok {
		return
	}
	for _, block := range assistant.Content {
		call, ok := block.(ai.ToolCall)
		if !ok {
			continue
		}
		var args struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(call.Arguments, &args) != nil || args.Path == "" {
			continue
		}
		switch call.Name {
		case "read":
			fileOps.Read[args.Path] = true
		case "write":
			fileOps.Written[args.Path] = true
		case "edit":
			fileOps.Edited[args.Path] = true
		}
	}
}

// ComputeFileLists finalizes the file lists: read-only files (read but never
// modified) and modified files, sorted. Always non-nil (upstream emits []).
func ComputeFileLists(fileOps *FileOperations) (readFiles, modifiedFiles []string) {
	readFiles = []string{}
	modifiedFiles = []string{}
	modified := map[string]bool{}
	for f := range fileOps.Edited {
		modified[f] = true
	}
	for f := range fileOps.Written {
		modified[f] = true
	}
	for f := range fileOps.Read {
		if !modified[f] {
			readFiles = append(readFiles, f)
		}
	}
	sortStrings(readFiles)
	for f := range modified {
		modifiedFiles = append(modifiedFiles, f)
	}
	sortStrings(modifiedFiles)
	return readFiles, modifiedFiles
}

// FormatFileOperations renders the XML file lists for the summary.
func FormatFileOperations(readFiles, modifiedFiles []string) string {
	var sections []string
	if len(readFiles) > 0 {
		sections = append(sections, "<read-files>\n"+strings.Join(readFiles, "\n")+"\n</read-files>")
	}
	if len(modifiedFiles) > 0 {
		sections = append(sections, "<modified-files>\n"+strings.Join(modifiedFiles, "\n")+"\n</modified-files>")
	}
	if len(sections) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(sections, "\n\n")
}

// ============================================================================
// Token calculation (compaction-specific: iterates AgentMessages including
// custom roles).
// ============================================================================

const estimatedImageChars = 4800

// CalculateCompactionContextTokens of a usage block (native totalTokens when
// available).
func CalculateCompactionContextTokens(usage ai.Usage) int {
	if usage.TotalTokens != 0 {
		return int(usage.TotalTokens)
	}
	return int(usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite)
}

// getAssistantUsage skips aborted, error, and all-zero usage messages.
func getAssistantUsage(msg ai.Message) *ai.Usage {
	assistant, ok := msg.(*ai.AssistantMessage)
	if !ok {
		return nil
	}
	if assistant.StopReason != ai.StopAborted && assistant.StopReason != ai.StopError &&
		CalculateCompactionContextTokens(assistant.Usage) > 0 {
		return &assistant.Usage
	}
	return nil
}

// customMessageText renders a custom message's summary/content for
// estimation.
func customMessageText(msg *ai.CustomMessage, content json.RawMessage) string {
	// Custom roles carry their text payload in role-specific shapes; the
	// summary-bearing roles use `summary`, bashExecution command+output,
	// custom its content.
	switch msg.Role {
	case RoleBashExecution:
		var fields bashExecutionFields
		if json.Unmarshal(content, &fields) == nil {
			return fields.Command + fields.Output
		}
	case RoleBranchSummary, RoleCompactionSummary:
		var fields struct {
			Summary string `json:"summary"`
		}
		if json.Unmarshal(content, &fields) == nil {
			return fields.Summary
		}
	default:
		var text string
		if json.Unmarshal(content, &text) == nil {
			return text
		}
	}
	return string(content)
}

// EstimateAgentMessageTokens estimates tokens with the chars/4 heuristic
// (conservative; port of estimateTokens).
func EstimateAgentMessageTokens(message ai.Message) int {
	chars := 0
	switch m := message.(type) {
	case *ai.UserMessage:
		if m.Content.Blocks == nil {
			chars = JSLength(m.Content.Text)
		} else {
			for _, block := range m.Content.Blocks {
				switch b := block.(type) {
				case ai.TextContent:
					chars += JSLength(b.Text)
				case ai.ImageContent:
					chars += estimatedImageChars
				}
			}
		}
		return ceilDiv(chars, 4)
	case *ai.AssistantMessage:
		for _, block := range m.Content {
			switch b := block.(type) {
			case ai.TextContent:
				chars += JSLength(b.Text)
			case ai.ThinkingContent:
				chars += JSLength(b.Thinking)
			case ai.ToolCall:
				chars += JSLength(b.Name) + len(mustMarshalJSON(json.RawMessage(b.Arguments)))
			}
		}
		return ceilDiv(chars, 4)
	case *ai.ToolResultMessage:
		for _, block := range m.Content {
			switch b := block.(type) {
			case ai.TextContent:
				chars += JSLength(b.Text)
			case ai.ImageContent:
				chars += estimatedImageChars
			}
		}
		return ceilDiv(chars, 4)
	case *ai.CustomMessage:
		chars = JSLength(customMessageText(m, m.Content))
		return ceilDiv(chars, 4)
	case *ai.SystemMessage:
		return ceilDiv(JSLength(GetSystemMessageTextOf(m)), 4)
	default:
		return 0
	}
}

// GetSystemMessageTextOf renders a system message (re-export shape).
func GetSystemMessageTextOf(m *ai.SystemMessage) string {
	return ai.GetSystemMessageText(m)
}

// CompactionContextUsageEstimate is the estimate outcome.
type CompactionContextUsageEstimate struct {
	Tokens         int
	UsageTokens    int
	TrailingTokens int
	LastUsageIndex int
}

// EstimateContextTokens estimates from messages, preferring the last valid
// assistant usage and estimating only the trailing messages.
func EstimateContextTokens(messages []ai.Message) CompactionContextUsageEstimate {
	usageInfo := -1
	var usage ai.Usage
	for i := len(messages) - 1; i >= 0; i-- {
		if u := getAssistantUsage(messages[i]); u != nil {
			usage = *u
			usageInfo = i
			break
		}
	}
	if usageInfo == -1 {
		estimated := 0
		for _, message := range messages {
			estimated += EstimateAgentMessageTokens(message)
		}
		return CompactionContextUsageEstimate{Tokens: estimated, UsageTokens: 0, TrailingTokens: estimated, LastUsageIndex: -1}
	}
	usageTokens := CalculateCompactionContextTokens(usage)
	trailing := 0
	for i := usageInfo + 1; i < len(messages); i++ {
		trailing += EstimateAgentMessageTokens(messages[i])
	}
	return CompactionContextUsageEstimate{
		Tokens: usageTokens + trailing, UsageTokens: usageTokens,
		TrailingTokens: trailing, LastUsageIndex: usageInfo,
	}
}

// ShouldCompact reports whether compaction should trigger.
func ShouldCompact(contextTokens int64, contextWindow int64, settings CompactionSettings) bool {
	if !settings.Enabled {
		return false
	}
	return contextTokens > contextWindow-settings.ReserveTokens
}

// ============================================================================
// Cut points
// ============================================================================

func isCutPointMessage(message ai.Message) bool {
	switch ai.RoleOf(message) {
	case ai.RoleUser, ai.RoleAssistant, RoleBashExecution, RoleCustom, RoleBranchSummary, RoleCompactionSummary:
		return true
	}
	return false // toolResult
}

func isTurnStartMessage(message ai.Message) bool {
	switch ai.RoleOf(message) {
	case ai.RoleUser, RoleBashExecution, RoleCustom, RoleBranchSummary, RoleCompactionSummary:
		return true
	}
	return false // assistant, toolResult
}

func isTurnStartEntry(entry *SessionEntry) bool {
	if entry.Type == "compaction" {
		return false
	}
	for _, message := range SessionEntryToContextMessages(entry) {
		if isTurnStartMessage(message) {
			return true
		}
	}
	return false
}

// findValidCutPoints finds indices of context-visible user-like or assistant
// messages; never cut at tool results (they must follow their tool call).
func findValidCutPoints(entries []SessionEntry, startIndex, endIndex int) []int {
	var cutPoints []int
	for i := startIndex; i < endIndex; i++ {
		entry := entries[i]
		if entry.Type == "compaction" {
			continue
		}
		for _, message := range SessionEntryToContextMessages(&entry) {
			if isCutPointMessage(message) {
				cutPoints = append(cutPoints, i)
				break
			}
		}
	}
	return cutPoints
}

// FindTurnStartIndex finds the user-role message starting the turn containing
// entryIndex; -1 when none.
func FindTurnStartIndex(entries []SessionEntry, entryIndex, startIndex int) int {
	for i := entryIndex; i >= startIndex; i-- {
		if isTurnStartEntry(&entries[i]) {
			return i
		}
	}
	return -1
}

// CutPointResult is the findCutPoint outcome.
type CutPointResult struct {
	// FirstKeptEntryIndex is the first entry to keep.
	FirstKeptEntryIndex int
	// TurnStartIndex is the user message starting the split turn, or -1.
	TurnStartIndex int
	// IsSplitTurn reports a mid-turn cut.
	IsSplitTurn bool
}

// FindCutPoint finds the cut point keeping approximately keepRecentTokens
// (walk backwards from newest accumulating estimated sizes).
func FindCutPoint(entries []SessionEntry, startIndex, endIndex, keepRecentTokens int) CutPointResult {
	cutPoints := findValidCutPoints(entries, startIndex, endIndex)
	if len(cutPoints) == 0 {
		return CutPointResult{FirstKeptEntryIndex: startIndex, TurnStartIndex: -1}
	}

	accumulatedTokens := 0
	cutIndex := cutPoints[0]

	for i := endIndex - 1; i >= startIndex; i-- {
		messageTokens := 0
		for _, message := range SessionEntryToContextMessages(&entries[i]) {
			messageTokens += EstimateAgentMessageTokens(message)
		}
		if messageTokens == 0 {
			continue
		}
		accumulatedTokens += messageTokens
		if accumulatedTokens >= keepRecentTokens {
			// Prefer the closest valid cut point at or after this entry; if
			// trailing tool results exceed the budget alone, keep their
			// preceding assistant tool call.
			cutIndex = cutPoints[len(cutPoints)-1]
			for _, candidate := range cutPoints {
				if candidate >= i {
					cutIndex = candidate
					break
				}
			}
			break
		}
	}

	// Back up over adjacent metadata entries that do not affect context.
	for cutIndex > startIndex {
		prevEntry := entries[cutIndex-1]
		if prevEntry.Type == "compaction" || len(SessionEntryToContextMessages(&prevEntry)) > 0 {
			break
		}
		cutIndex--
	}

	cutEntry := entries[cutIndex]
	startsTurn := isTurnStartEntry(&cutEntry)
	turnStartIndex := -1
	if !startsTurn {
		turnStartIndex = FindTurnStartIndex(entries, cutIndex, startIndex)
	}
	return CutPointResult{
		FirstKeptEntryIndex: cutIndex, TurnStartIndex: turnStartIndex,
		IsSplitTurn: !startsTurn && turnStartIndex != -1,
	}
}

// ============================================================================
// Message serialization for summarization
// ============================================================================

const toolResultMaxChars = 2000

func truncateForSummary(text string, maxChars int) string {
	if JSLength(text) <= maxChars {
		return text
	}
	truncatedChars := JSLength(text) - maxChars
	return JSSlice(text, 0, maxChars) + fmt.Sprintf("\n\n[... %d more characters truncated]", truncatedChars)
}

// SerializeConversation renders LLM messages as text so the model summarizes
// rather than continues (port of serializeConversation).
func SerializeConversation(messages []ai.Message) string {
	var parts []string
	for _, msg := range messages {
		switch m := msg.(type) {
		case *ai.UserMessage:
			content := contentTextJoinedNoSep(m.Content)
			if content != "" {
				parts = append(parts, "[User]: "+content)
			}
		case *ai.AssistantMessage:
			var thinkingParts, toolCalls []string
			hasText := false
			for _, block := range m.Content {
				switch b := block.(type) {
				case ai.ThinkingContent:
					thinkingParts = append(thinkingParts, b.Thinking)
				case ai.ToolCall:
					var args map[string]any
					json.Unmarshal(b.Arguments, &args)
					var argPairs []string
					for k, v := range args {
						argPairs = append(argPairs, fmt.Sprintf("%s=%s", k, mustMarshalJSON(v)))
					}
					sortStrings(argPairs)
					toolCalls = append(toolCalls, fmt.Sprintf("%s(%s)", b.Name, strings.Join(argPairs, ", ")))
				case ai.TextContent:
					hasText = true
				}
			}
			if len(thinkingParts) > 0 {
				parts = append(parts, "[Assistant thinking]: "+strings.Join(thinkingParts, "\n"))
			}
			if hasText {
				parts = append(parts, "[Assistant]: "+contentTextJoined(ai.StringOrBlocks{Blocks: m.Content}))
			}
			if len(toolCalls) > 0 {
				parts = append(parts, "[Assistant tool calls]: "+strings.Join(toolCalls, "; "))
			}
		case *ai.ToolResultMessage:
			content := contentTextJoinedNoSep(StringOrBlocksOf(m.Content))
			if content != "" {
				parts = append(parts, "[Tool result]: "+truncateForSummary(content, toolResultMaxChars))
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// StringOrBlocksOf adapts user content for text joining.
func StringOrBlocksOf(content ai.UserContentList) ai.StringOrBlocks {
	blocks := make(ai.ContentList, 0, len(content))
	for _, c := range content {
		blocks = append(blocks, c)
	}
	return ai.StringOrBlocks{Blocks: blocks}
}

func contentTextJoinedNoSep(content ai.StringOrBlocks) string {
	if content.Blocks == nil {
		return content.Text
	}
	var texts []string
	for _, block := range content.Blocks {
		if tc, ok := block.(ai.TextContent); ok {
			texts = append(texts, tc.Text)
		}
	}
	return strings.Join(texts, "")
}

// ============================================================================
// Summarization
// ============================================================================

const SummarizationSystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

const summarizationPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const updateSummarizationPrompt = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Update the existing structured summary with new information. RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context from the new messages
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- If something is no longer relevant, you may remove it

Use this EXACT format:

## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing, add new ones discovered]

## Progress
### Done
- [x] [Include previously done items AND newly completed items]

### In Progress
- [ ] [Current work - update based on progress]

### Blocked
- [Current blockers - remove if resolved]

## Key Decisions
- **[Decision]**: [Brief rationale] (preserve all previous, add new)

## Next Steps
1. [Update based on current state]

## Critical Context
- [Preserve important context, add new if needed]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const turnPrefixSummarizationPrompt = `This is the PREFIX of a turn that was too large to keep. The SUFFIX (recent work) is retained.

Summarize the prefix to provide context for the retained suffix:

## Original Request
[What did the user ask for in this turn?]

## Early Progress
- [Key decisions and work done in the prefix]

## Context for Suffix
- [Information needed to understand the retained recent work]

Be concise. Focus on what's needed to understand the kept suffix.`

// GetSummarizationFailure reports why a summarization response cannot safely
// be persisted (a length stop holds partial text).
func GetSummarizationFailure(response *ai.AssistantMessage, label string) string {
	if response.StopReason == ai.StopError {
		message := "Unknown error"
		if response.ErrorMessage != nil {
			message = *response.ErrorMessage
		}
		return fmt.Sprintf("%s failed: %s", label, message)
	}
	if response.StopReason == ai.StopLength {
		return fmt.Sprintf("%s failed: generation hit the token cap and the summary is incomplete", label)
	}
	return ""
}

// CompactionResult is the compact() outcome.
type CompactionResult struct {
	Summary              string
	FirstKeptEntryID     string
	TokensBefore         int64
	EstimatedTokensAfter int64
	Usage                *ai.Usage
	Details              json.RawMessage
}

// CombineUsage merges two usage blocks (port of combineUsage).
func CombineUsage(first, second ai.Usage) ai.Usage {
	out := ai.Usage{
		Input: first.Input + second.Input, Output: first.Output + second.Output,
		CacheRead:   first.CacheRead + second.CacheRead,
		CacheWrite:  first.CacheWrite + second.CacheWrite,
		TotalTokens: first.TotalTokens + second.TotalTokens,
		Cost: ai.UsageCost{
			Input:      first.Cost.Input + second.Cost.Input,
			Output:     first.Cost.Output + second.Cost.Output,
			CacheRead:  first.Cost.CacheRead + second.Cost.CacheRead,
			CacheWrite: first.Cost.CacheWrite + second.Cost.CacheWrite,
			Total:      first.Cost.Total + second.Cost.Total,
		},
	}
	if first.CacheWrite1h != nil || second.CacheWrite1h != nil {
		v := int64(0)
		if first.CacheWrite1h != nil {
			v += *first.CacheWrite1h
		}
		if second.CacheWrite1h != nil {
			v += *second.CacheWrite1h
		}
		out.CacheWrite1h = &v
	}
	if first.Reasoning != nil || second.Reasoning != nil {
		v := int64(0)
		if first.Reasoning != nil {
			v += *first.Reasoning
		}
		if second.Reasoning != nil {
			v += *second.Reasoning
		}
		out.Reasoning = &v
	}
	return out
}

// SummarizationCallbacks hook retry notifications.
type SummarizationCallbacks struct {
	OnRetryScheduled    func(attempt, maxAttempts int, delayMS int64, errorMessage string) error
	OnRetryAttemptStart func() error
	OnRetryFinished     func(success bool, attempt int, finalError string) error
}

// CompleteSummarization wraps one summarization LLM call in the assistant
// retry loop (port of completeSummarization). One-off summaries skip cache
// writes and receive a fresh routing session id when the caller has none.
func CompleteSummarization(
	model *ai.Model,
	context ai.TranscriptContext,
	options *ai.SimpleStreamOptions,
	streamFn StreamFnFn,
	retry *ai.RetryPolicy,
	callbacks *SummarizationCallbacks,
) (*ai.AssistantMessage, error) {
	requestOptions := *options
	requestOptions.CacheRetention = ai.CacheRetentionNone
	if requestOptions.SessionID == "" {
		requestOptions.SessionID = UUIDv7()
	}
	produce := func() (*ai.AssistantMessage, error) {
		if streamFn != nil {
			return streamFn(model, context, &requestOptions).Result(contextOf(&requestOptions))
		}
		return StreamFnDefault(model, context, &requestOptions)
	}
	var cb *ai.RetryCallbacks
	if callbacks != nil {
		cb = &ai.RetryCallbacks{
			OnRetryScheduled:    callbacks.OnRetryScheduled,
			OnRetryAttemptStart: callbacks.OnRetryAttemptStart,
			OnRetryFinished:     callbacks.OnRetryFinished,
		}
	}
	return ai.RetryAssistantCall(produce, retry, contextOf(&requestOptions), cb)
}

// StreamFnFn is the stream function signature for compaction.
type StreamFnFn = func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream

// StreamFnDefault is the fallback (nil streamFn → caller-provided default).
var StreamFnDefault = func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
	return nil, fmt.Errorf("No stream function configured for summarization")
}

func contextOf(options *ai.SimpleStreamOptions) context.Context {
	if options != nil && options.Ctx != nil {
		return options.Ctx
	}
	return context.Background()
}

// CompactionPreparation is the prepareCompaction outcome.
type CompactionPreparation struct {
	FirstKeptEntryID    string
	MessagesToSummarize []ai.Message
	TurnPrefixMessages  []ai.Message
	IsSplitTurn         bool
	TokensBefore        int
	PreviousSummary     string
	FileOps             *FileOperations
	Settings            CompactionSettings
}

// PrepareCompaction pre-computes the compaction cut and spans
// (port of prepareCompaction); undefined when there is nothing to compact.
func PrepareCompaction(pathEntries []SessionEntry, settings CompactionSettings) *CompactionPreparation {
	// Nothing to compact without entries (upstream returns null).
	if len(pathEntries) == 0 {
		return nil
	}
	if pathEntries[len(pathEntries)-1].Type == "compaction" {
		return nil
	}

	prevCompactionIndex := -1
	for i := len(pathEntries) - 1; i >= 0; i-- {
		if pathEntries[i].Type == "compaction" {
			prevCompactionIndex = i
			break
		}
	}

	previousSummary := ""
	boundaryStart := 0
	if prevCompactionIndex >= 0 {
		prevCompaction := pathEntries[prevCompactionIndex]
		previousSummary = prevCompaction.Summary
		firstKeptEntryIndex := -1
		for i := range pathEntries {
			if pathEntries[i].ID == prevCompaction.FirstKeptEntryID {
				firstKeptEntryIndex = i
				break
			}
		}
		boundaryStart = firstKeptEntryIndex
		if firstKeptEntryIndex < 0 {
			boundaryStart = prevCompactionIndex + 1
		}
	}
	boundaryEnd := len(pathEntries)

	var contextMessages []ai.Message
	for i := range BuildContextEntries(pathEntries, nil, nil) {
		entry := BuildContextEntries(pathEntries, nil, nil)[i]
		contextMessages = append(contextMessages, SessionEntryToContextMessages(&entry)...)
	}
	tokensBefore := EstimateContextTokens(contextMessages).Tokens

	cutPoint := FindCutPoint(pathEntries, boundaryStart, boundaryEnd, int(settings.KeepRecentTokens))

	firstKeptEntry := pathEntries[cutPoint.FirstKeptEntryIndex]
	if firstKeptEntry.ID == "" {
		return nil // session needs migration
	}

	historyEnd := cutPoint.FirstKeptEntryIndex
	if cutPoint.IsSplitTurn {
		historyEnd = cutPoint.TurnStartIndex
	}

	var messagesToSummarize []ai.Message
	for i := boundaryStart; i < historyEnd; i++ {
		if msg := getMessageFromEntryForCompaction(&pathEntries[i]); msg != nil {
			messagesToSummarize = append(messagesToSummarize, msg)
		}
	}
	var turnPrefixMessages []ai.Message
	if cutPoint.IsSplitTurn {
		for i := cutPoint.TurnStartIndex; i < cutPoint.FirstKeptEntryIndex; i++ {
			if msg := getMessageFromEntryForCompaction(&pathEntries[i]); msg != nil {
				turnPrefixMessages = append(turnPrefixMessages, msg)
			}
		}
	}
	if len(messagesToSummarize) == 0 && len(turnPrefixMessages) == 0 {
		return nil
	}

	fileOps := extractFileOperations(messagesToSummarize, pathEntries, prevCompactionIndex)
	if cutPoint.IsSplitTurn {
		for _, msg := range turnPrefixMessages {
			ExtractFileOpsFromMessage(msg, fileOps)
		}
	}

	return &CompactionPreparation{
		FirstKeptEntryID: firstKeptEntry.ID, MessagesToSummarize: messagesToSummarize,
		TurnPrefixMessages: turnPrefixMessages, IsSplitTurn: cutPoint.IsSplitTurn,
		TokensBefore: tokensBefore, PreviousSummary: previousSummary,
		FileOps: fileOps, Settings: settings,
	}
}

// extractFileOperations collects file ops from messages and the previous
// pi-generated compaction entry.
func extractFileOperations(messages []ai.Message, entries []SessionEntry, prevCompactionIndex int) *FileOperations {
	fileOps := CreateFileOps()
	if prevCompactionIndex >= 0 && prevCompactionIndex < len(entries) {
		prev := entries[prevCompactionIndex]
		if (prev.FromHook == nil || !*prev.FromHook) && len(prev.Details) > 0 {
			var details CompactionDetails
			if json.Unmarshal(prev.Details, &details) == nil {
				for _, f := range details.ReadFiles {
					fileOps.Read[f] = true
				}
				for _, f := range details.ModifiedFiles {
					fileOps.Edited[f] = true
				}
			}
		}
	}
	for _, msg := range messages {
		ExtractFileOpsFromMessage(msg, fileOps)
	}
	return fileOps
}

// getMessageFromEntryForCompaction projects an entry, dropping compaction
// entries and system messages (prompt state travels with the compaction
// entry's replay).
func getMessageFromEntryForCompaction(entry *SessionEntry) ai.Message {
	if entry.Type == "compaction" {
		return nil
	}
	messages := SessionEntryToContextMessages(entry)
	if len(messages) == 0 {
		return nil
	}
	if ai.RoleOf(messages[0]) == ai.RoleSystem {
		return nil
	}
	return messages[0]
}

// CompactionOptions carry the LLM access inputs for Compact.
type CompactionOptions struct {
	Model              *ai.Model
	APIKey             string
	Headers            map[string]string
	CustomInstructions string
	Ctx                context.Context
	ThinkingLevel      ai.ThinkingLevel
	StreamFn           StreamFnFn
	Env                map[string]string
	Retry              *ai.RetryPolicy
	Callbacks          *SummarizationCallbacks
	SessionID          string
}

// Compact generates summaries for compaction using prepared data
// (port of compact).
func Compact(preparation *CompactionPreparation, options CompactionOptions) (*CompactionResult, error) {
	var summary string
	var summaryUsage ai.Usage
	hasUsage := false

	if preparation.IsSplitTurn && len(preparation.TurnPrefixMessages) > 0 {
		historyText := preparation.PreviousSummary
		if historyText == "" {
			historyText = "No prior history."
		}
		var historyUsage *ai.Usage
		if len(preparation.MessagesToSummarize) > 0 {
			result, err := generateSummaryWithUsage(preparation.MessagesToSummarize, options, preparation.PreviousSummary)
			if err != nil {
				return nil, err
			}
			historyText = result.text
			historyUsage = result.usage
		}
		turnPrefixResult, err := generateTurnPrefixSummary(preparation.TurnPrefixMessages, options)
		if err != nil {
			return nil, err
		}
		summary = historyText + "\n\n---\n\n**Turn Context (split turn):**\n\n" + turnPrefixResult.text
		if historyUsage != nil {
			summaryUsage = CombineUsage(*historyUsage, *turnPrefixResult.usage)
		} else {
			summaryUsage = *turnPrefixResult.usage
		}
		hasUsage = true
	} else {
		result, err := generateSummaryWithUsage(preparation.MessagesToSummarize, options, preparation.PreviousSummary)
		if err != nil {
			return nil, err
		}
		summary = result.text
		summaryUsage = *result.usage
		hasUsage = true
	}

	readFiles, modifiedFiles := ComputeFileLists(preparation.FileOps)
	summary += FormatFileOperations(readFiles, modifiedFiles)

	if preparation.FirstKeptEntryID == "" {
		return nil, fmt.Errorf("First kept entry has no UUID - session may need migration")
	}

	details, _ := ai.MarshalJSON(CompactionDetails{ReadFiles: readFiles, ModifiedFiles: modifiedFiles})
	result := &CompactionResult{
		Summary: summary, FirstKeptEntryID: preparation.FirstKeptEntryID,
		TokensBefore: int64(preparation.TokensBefore),
		Details:      details,
	}
	if hasUsage {
		u := summaryUsage
		result.Usage = &u
	}
	return result, nil
}

// summaryWithUsage is the generateSummaryWithUsage outcome.
type summaryWithUsage struct {
	text  string
	usage *ai.Usage
}

// generateSummaryWithUsage generates or updates a conversation summary.
func generateSummaryWithUsage(currentMessages []ai.Message, options CompactionOptions, previousSummary string) (*summaryWithUsage, error) {
	model := options.Model
	maxTokens := math.Floor(0.8 * float64(DefaultCompactionSettings.ReserveTokens))
	if model.MaxTokens > 0 && maxTokens > float64(model.MaxTokens) {
		maxTokens = float64(model.MaxTokens)
	}

	basePrompt := summarizationPrompt
	if previousSummary != "" {
		basePrompt = updateSummarizationPrompt
	}
	if options.CustomInstructions != "" {
		basePrompt = fmt.Sprintf("%s\n\nAdditional focus: %s", basePrompt, options.CustomInstructions)
	}

	llmMessages := ConvertToLlm(currentMessages)
	conversationText := SerializeConversation(llmMessages)
	promptText := fmt.Sprintf("<conversation>\n%s\n</conversation>\n\n", conversationText)
	if previousSummary != "" {
		promptText += fmt.Sprintf("<previous-summary>\n%s\n</previous-summary>\n\n", previousSummary)
	}
	promptText += basePrompt

	response, err := runSummarization(model, promptText, int(maxTokens), options)
	if err != nil {
		return nil, err
	}
	if failure := GetSummarizationFailure(response, "Summarization"); failure != "" {
		return nil, fmt.Errorf("%s", failure)
	}
	for _, block := range response.Content {
		if _, ok := block.(ai.ToolCall); ok {
			return nil, fmt.Errorf("Summarization attempted to call a tool")
		}
	}
	return &summaryWithUsage{text: contentTextOf(response.Content), usage: &response.Usage}, nil
}

// generateTurnPrefixSummary summarizes a split turn's prefix.
func generateTurnPrefixSummary(messages []ai.Message, options CompactionOptions) (*summaryWithUsage, error) {
	model := options.Model
	maxTokens := math.Floor(0.5 * float64(DefaultCompactionSettings.ReserveTokens))
	if model.MaxTokens > 0 && maxTokens > float64(model.MaxTokens) {
		maxTokens = float64(model.MaxTokens)
	}
	llmMessages := ConvertToLlm(messages)
	conversationText := SerializeConversation(llmMessages)
	promptText := fmt.Sprintf("<conversation>\n%s\n</conversation>\n\n%s", conversationText, turnPrefixSummarizationPrompt)

	response, err := runSummarization(model, promptText, int(maxTokens), options)
	if err != nil {
		return nil, err
	}
	if failure := GetSummarizationFailure(response, "Turn prefix summarization"); failure != "" {
		return nil, fmt.Errorf("%s", failure)
	}
	for _, block := range response.Content {
		if _, ok := block.(ai.ToolCall); ok {
			return nil, fmt.Errorf("Turn prefix summarization attempted to call a tool")
		}
	}
	return &summaryWithUsage{text: contentTextOf(response.Content), usage: &response.Usage}, nil
}

// runSummarization builds the context and calls the LLM once (via the
// retry-wrapped choke point).
func runSummarization(model *ai.Model, promptText string, maxTokens int, options CompactionOptions) (*ai.AssistantMessage, error) {
	context := ai.NormalizeContext(ai.Context{
		SystemPrompt: strPtrOf(SummarizationSystemPrompt),
		Messages: []ai.Message{&ai.UserMessage{
			Content:   ai.StringOrBlocks{Blocks: ai.ContentList{ai.TextContent{Text: promptText}}},
			Timestamp: time.Now().UnixMilli(),
		}},
	})
	streamOptions := ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			APIKey:    options.APIKey,
			Ctx:       options.Ctx,
			SessionID: options.SessionID,
		},
		Reasoning: options.ThinkingLevel,
	}
	// maxTokens plumbs through SamplingExtras-free path: the named field.
	streamOptions.MaxTokens = &maxTokens
	if model.Reasoning && options.ThinkingLevel != "" && options.ThinkingLevel != ai.ThinkOff {
		// reasoning set above
	} else {
		streamOptions.Reasoning = ""
	}
	return CompleteSummarization(model, context, &streamOptions, options.StreamFn, options.Retry, options.Callbacks)
}

func strPtrOf(s string) *string { return &s }

// contentTextOf joins assistant content text with newlines.
func contentTextOf(content ai.ContentList) string {
	var texts []string
	for _, block := range content {
		if tc, ok := block.(ai.TextContent); ok {
			texts = append(texts, tc.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// Local aliases to the ai package's JS-semantics helpers.
func sortStrings(list []string) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j] < list[j-1]; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

func JSLength(s string) int { return ai.JSLength(s) }

func JSSlice(s string, start, end int) string { return ai.JSSlice(s, start, end) }

func ceilDiv(a, b int) int { return (a + b - 1) / b }
