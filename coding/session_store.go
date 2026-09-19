package coding

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dat267/gpi/ai"
)

// Port of core/session-manager.ts (v3 JSONL session trees) and the
// messages.ts custom message shapes it persists.

// CurrentSessionVersion is the on-disk format version.
const CurrentSessionVersion = 3

// SessionHeader is the first line of a session file. Version is nil for
// v1 sessions.
type SessionHeader struct {
	Version       *int    `json:"version,omitempty"`
	ID            string  `json:"id"`
	Timestamp     string  `json:"timestamp"`
	Cwd           string  `json:"cwd"`
	ParentSession *string `json:"parentSession,omitempty"`

	raw json.RawMessage
}

// Type implements the file-entry discriminator.
func (h *SessionHeader) Type() string { return "session" }

// SessionEntryBase is the tree-structured entry identity.
type SessionEntryBase struct {
	ID        string  `json:"id"`
	ParentID  *string `json:"parentId"`
	Timestamp string  `json:"timestamp"`
}

// SessionEntry is the discriminated union of session entries.
type SessionEntry struct {
	Type string `json:"type"`
	SessionEntryBase

	// message entry
	Message json.RawMessage `json:"message,omitempty"`

	// thinking_level_change
	ThinkingLevel string `json:"thinkingLevel,omitempty"`

	// model_change
	Provider string `json:"provider,omitempty"`
	ModelID  string `json:"modelId,omitempty"`

	// compaction
	Summary           string          `json:"summary,omitempty"`
	FirstKeptEntryID  string          `json:"firstKeptEntryId,omitempty"`
	TokensBefore      int64           `json:"tokensBefore,omitempty"`
	Details           json.RawMessage `json:"details,omitempty"`
	Usage             *ai.Usage       `json:"usage,omitempty"`
	FromHook          *bool           `json:"fromHook,omitempty"`
	SystemMessageJSON json.RawMessage `json:"systemMessage,omitempty"`

	// branch_summary
	FromID string `json:"fromId,omitempty"`

	// custom
	CustomType string `json:"customType,omitempty"`

	// custom_message
	Content json.RawMessage `json:"content,omitempty"`
	Display *bool           `json:"display,omitempty"`

	// label
	TargetID string  `json:"targetId,omitempty"`
	Label    *string `json:"label,omitempty"`

	// session_info
	Name *string `json:"name,omitempty"`

	raw json.RawMessage
}

// Raw returns the entry's decoded raw JSON.
func (e *SessionEntry) Raw() json.RawMessage { return e.raw }

// FileEntry is a header or entry line.
type FileEntry struct {
	Header *SessionHeader
	Entry  *SessionEntry
}

// MarshalFileEntry encodes one JSONL line.
func MarshalFileEntry(entry FileEntry) (string, error) {
	if entry.Header != nil {
		enc, err := ai.MarshalJSON(struct {
			Type          string  `json:"type"`
			Version       int     `json:"version,omitempty"`
			ID            string  `json:"id"`
			Timestamp     string  `json:"timestamp"`
			Cwd           string  `json:"cwd"`
			ParentSession *string `json:"parentSession,omitempty"`
		}{
			Type: "session", Version: CurrentSessionVersion,
			ID: entry.Header.ID, Timestamp: entry.Header.Timestamp,
			Cwd: entry.Header.Cwd, ParentSession: entry.Header.ParentSession,
		})
		return string(enc) + "\n", err
	}
	enc, err := ai.MarshalJSON(entry.Entry)
	return string(enc) + "\n", err
}

// UnmarshalFileEntry decodes one JSONL line.
func UnmarshalFileEntry(line string) (*FileEntry, error) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(line), &probe); err != nil {
		return nil, err
	}
	if probe.Type == "session" {
		var header SessionHeader
		if err := json.Unmarshal([]byte(line), &header); err != nil {
			return nil, err
		}
		header.raw = json.RawMessage(line)
		return &FileEntry{Header: &header}, nil
	}
	var entry SessionEntry
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return nil, err
	}
	entry.raw = json.RawMessage(line)
	return &FileEntry{Entry: &entry}, nil
}

// UUIDv7 generates a time-ordered UUID (port of uuidv7).
func UUIDv7() string {
	var b [16]byte
	if _, err := rand.Read(b[8:]); err != nil {
		panic(err)
	}
	ms := uint64(time.Now().UnixMilli())
	binary.BigEndian.PutUint64(b[:8], ms<<16)
	b[6] = 0x70 | (b[6] & 0x0F) // version 7
	b[8] = 0x80 | (b[8] & 0x3F) // variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// AssertValidSessionID validates a user-provided session id.
func AssertValidSessionID(id string) error {
	re := regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)
	if !re.MatchString(id) {
		return fmt.Errorf("Session id must be non-empty, contain only alphanumeric characters, '-', '_', and '.', and start and end with an alphanumeric character")
	}
	return nil
}

// generateID makes a unique short id (8 hex chars, collision-checked).
func generateID(byID map[string]*SessionEntry) string {
	for i := 0; i < 100; i++ {
		id := UUIDv7()
		id = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(id, "-", ""), "_", ""), ".", "")[:8]
		if _, taken := byID[id]; !taken {
			return id
		}
	}
	return strings.ReplaceAll(UUIDv7(), "-", "")
}

// Migration functions.

// migrateV1ToV2 adds id/parentId tree structure (mutates entries).
func migrateV1ToV2(entries []FileEntry) {
	ids := map[string]bool{}
	var prevID *string
	for i := range entries {
		if entries[i].Header != nil {
			v := 2
			entries[i].Header.Version = &v
			continue
		}
		entry := entries[i].Entry
		entry.ID = generateID(nil)
		for ids[entry.ID] {
			entry.ID = generateID(nil)
		}
		ids[entry.ID] = true
		entry.ParentID = prevID
		prevID = &entry.ID

		// firstKeptEntryIndex → firstKeptEntryId for compaction.
		if entry.Type == "compaction" {
			var legacy struct {
				FirstKeptEntryIndex *int `json:"firstKeptEntryIndex"`
			}
			if json.Unmarshal(entry.raw, &legacy) == nil && legacy.FirstKeptEntryIndex != nil {
				idx := *legacy.FirstKeptEntryIndex
				if idx >= 0 && idx < len(entries) && entries[idx].Entry != nil {
					entry.FirstKeptEntryID = entries[idx].Entry.ID
				}
			}
		}
	}
}

// migrateV2ToV3 renames hookMessage roles to custom.
func migrateV2ToV3(entries []FileEntry) {
	for i := range entries {
		if entries[i].Header != nil {
			v := 3
			entries[i].Header.Version = &v
			continue
		}
		entry := entries[i].Entry
		if entry.Type == "message" && len(entry.Message) > 0 {
			var msg map[string]json.RawMessage
			if json.Unmarshal(entry.Message, &msg) == nil {
				if roleRaw, ok := msg["role"]; ok {
					var role string
					if json.Unmarshal(roleRaw, &role) == nil && role == "hookMessage" {
						msg["role"] = json.RawMessage(`"custom"`)
						entry.Message = mustMarshalJSON(msg)
					}
				}
			}
		}
	}
}

// migrateToCurrentVersion brings entries to v3; returns true when applied.
func migrateToCurrentVersion(entries []FileEntry) bool {
	version := 1
	for _, entry := range entries {
		if entry.Header != nil {
			if entry.Header.Version != nil {
				version = *entry.Header.Version
			}
			break
		}
	}
	if version >= CurrentSessionVersion {
		return false
	}
	if version < 2 {
		migrateV1ToV2(entries)
	}
	if version < 3 {
		migrateV2ToV3(entries)
	}
	return true
}

// ParseSessionEntries parses JSONL content skipping malformed lines
// (port of parseSessionEntries).
func ParseSessionEntries(content string) []FileEntry {
	var entries []FileEntry
	for _, line := range strings.Split(strings.Trim(content, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if entry, err := UnmarshalFileEntry(line); err == nil {
			entries = append(entries, *entry)
		}
	}
	return entries
}

// GetLatestCompactionEntry scans entries in reverse.
func GetLatestCompactionEntry(entries []SessionEntry) *SessionEntry {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type == "compaction" {
			return &entries[i]
		}
	}
	return nil
}

// buildSessionPath walks the parent chain from the leaf to the root.
func buildSessionPath(entries []SessionEntry, leafID *string, byID map[string]*SessionEntry) []SessionEntry {
	if byID == nil {
		byID = map[string]*SessionEntry{}
		for i := range entries {
			byID[entries[i].ID] = &entries[i]
		}
	}
	if leafID != nil && *leafID == "" {
		return nil
	}
	var leaf *SessionEntry
	if leafID != nil {
		leaf = byID[*leafID]
	}
	if leaf == nil && len(entries) > 0 {
		leaf = &entries[len(entries)-1]
	}
	if leaf == nil {
		return nil
	}
	var path []SessionEntry
	current := leaf
	for current != nil {
		path = append(path, *current)
		if current.ParentID != nil {
			current = byID[*current.ParentID]
		} else {
			current = nil
		}
	}
	// reverse
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

// getSessionContextSettings resolves thinking level and model from the path.
func getSessionContextSettings(path []SessionEntry) (thinkingLevel string, model *SessionModelRef) {
	thinkingLevel = "off"
	for i := range path {
		entry := &path[i]
		switch entry.Type {
		case "thinking_level_change":
			thinkingLevel = entry.ThinkingLevel
		case "model_change":
			model = &SessionModelRef{Provider: entry.Provider, ModelID: entry.ModelID}
		case "message":
			var msg struct {
				Role     string `json:"role"`
				Provider string `json:"provider"`
				Model    string `json:"model"`
			}
			if json.Unmarshal(entry.Message, &msg) == nil && msg.Role == "assistant" {
				model = &SessionModelRef{Provider: msg.Provider, ModelID: msg.Model}
			}
		}
	}
	return thinkingLevel, model
}

// SessionModelRef is the model reference in a session context.
type SessionModelRef struct {
	Provider string `json:"provider"`
	ModelID  string `json:"modelId"`
}

// SessionContext is the resolved LLM context of a session.
type SessionContext struct {
	Messages      []ai.Message
	ThinkingLevel string
	Model         *SessionModelRef
}

// Custom message shapes (core/messages.ts).

const (
	CompactionSummaryPrefix = "The conversation history before this point was compacted into the following summary:\n\n<summary>\n"
	CompactionSummarySuffix = "\n</summary>"
	BranchSummaryPrefix     = "The following is a summary of a branch that this conversation came back from:\n\n<summary>\n"
	BranchSummarySuffix     = "</summary>"
)

// CreateCustomMessage builds a custom-role AgentMessage.
func CreateCustomMessage(customType string, content string, display bool, timestamp int64) *ai.CustomMessage {
	if timestamp == 0 {
		timestamp = time.Now().UnixMilli()
	}
	raw, _ := ai.MarshalJSON(map[string]any{
		"role": "custom", "customType": customType, "content": content, "display": display, "timestamp": timestamp,
	})
	return &ai.CustomMessage{Role: "custom", Content: raw, Timestamp: timestamp}
}

// CreateCompactionSummaryMessage builds the compaction summary custom message.
func CreateCompactionSummaryMessage(summary string, tokensBefore int64, timestamp int64) *ai.CustomMessage {
	if timestamp == 0 {
		timestamp = time.Now().UnixMilli()
	}
	raw, _ := ai.MarshalJSON(map[string]any{
		"role": "compactionSummary", "summary": summary, "tokensBefore": tokensBefore, "timestamp": timestamp,
	})
	return &ai.CustomMessage{Role: "compactionSummary", Content: raw, Timestamp: timestamp}
}

// CreateBranchSummaryMessage builds the branch summary custom message.
func CreateBranchSummaryMessage(summary string, fromID string, timestamp int64) *ai.CustomMessage {
	if timestamp == 0 {
		timestamp = time.Now().UnixMilli()
	}
	raw, _ := ai.MarshalJSON(map[string]any{
		"role": "branchSummary", "summary": summary, "fromId": fromID, "timestamp": timestamp,
	})
	return &ai.CustomMessage{Role: "branchSummary", Content: raw, Timestamp: timestamp}
}

// SessionEntryToContextMessages projects one entry into LLM messages
// (port of sessionEntryToContextMessages).
func SessionEntryToContextMessages(entry *SessionEntry) []ai.Message {
	switch entry.Type {
	case "message":
		message, err := ai.UnmarshalMessage(entry.Message)
		if err != nil {
			return nil
		}
		return []ai.Message{message}
	case "custom_message":
		return []ai.Message{CreateCustomMessage(entry.CustomType, string(entry.Content), entry.Display != nil && *entry.Display, 0)}
	case "branch_summary":
		if entry.Summary != "" {
			return []ai.Message{CreateBranchSummaryMessage(entry.Summary, entry.FromID, 0)}
		}
	case "compaction":
		return []ai.Message{CreateCompactionSummaryMessage(entry.Summary, entry.TokensBefore, 0)}
	}
	return nil
}

// BuildContextEntries builds the active, compaction-aware session entry
// list following the leaf path (port of buildContextEntries).
func BuildContextEntries(entries []SessionEntry, leafID *string, byID map[string]*SessionEntry) []SessionEntry {
	path := buildSessionPath(entries, leafID, byID)
	var compaction *SessionEntry
	for i := range path {
		if path[i].Type == "compaction" {
			compaction = &path[i]
		}
	}
	if compaction == nil {
		return path
	}
	compactionIdx := -1
	for i := range path {
		if path[i].ID == compaction.ID {
			compactionIdx = i
			break
		}
	}
	if compactionIdx < 0 {
		return path
	}

	contextEntries := []SessionEntry{*compaction}
	foundFirstKept := false
	for i := 0; i < compactionIdx; i++ {
		entry := path[i]
		if entry.ID == compaction.FirstKeptEntryID {
			foundFirstKept = true
		}
		if foundFirstKept {
			var msgRole string
			if entry.Type == "message" && len(entry.Message) > 0 {
				json.Unmarshal(entry.Message, &msgRole)
			}
			if !(entry.Type == "message" && msgRole == "system") {
				contextEntries = append(contextEntries, entry)
			}
		}
	}
	contextEntries = append(contextEntries, path[compactionIdx+1:]...)
	return contextEntries
}

// BuildSessionContext resolves the LLM context from the entry tree
// (port of buildSessionContext).
func BuildSessionContext(entries []SessionEntry, leafID *string, byID map[string]*SessionEntry) SessionContext {
	path := buildSessionPath(entries, leafID, byID)
	thinkingLevel, model := getSessionContextSettings(path)
	contextEntries := BuildContextEntries(entries, leafID, byID)
	var messages []ai.Message
	for i := range contextEntries {
		messages = append(messages, SessionEntryToContextMessages(&contextEntries[i])...)
	}
	return SessionContext{Messages: messages, ThinkingLevel: thinkingLevel, Model: model}
}

// DefaultAgentDir is ~/.pi/agent.
func DefaultAgentDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pi", "agent")
}

// DefaultSessionsDir is ~/.pi/agent/sessions.
func DefaultSessionsDir() string {
	return filepath.Join(DefaultAgentDir(), "sessions")
}

// DefaultSessionDir computes (and creates) the encoded-cwd session directory
// (port of getDefaultSessionDir).
func DefaultSessionDir(cwd string, agentDir string) string {
	if agentDir == "" {
		agentDir = DefaultAgentDir()
	}
	resolvedCwd := ResolvePath(cwd, "", PathInputOptions{})
	safe := "--" + strings.TrimLeft(resolvedCwd, "/\\") + "--"
	safe = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' {
			return '-'
		}
		return r
	}, safe)
	dir := filepath.Join(agentDir, "sessions", safe)
	if _, err := os.Stat(dir); err != nil {
		_ = os.MkdirAll(dir, 0o755)
	}
	return dir
}

// LoadEntriesFromFile loads JSONL entries, appending a newline when the file
// ends mid-line (port of loadEntriesFromFile).
func LoadEntriesFromFile(filePath string) ([]FileEntry, error) {
	resolved := NormalizePath(filePath, PathInputOptions{})
	if _, err := os.Stat(resolved); err != nil {
		return nil, nil
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, err
	}
	var entries []FileEntry
	text := string(data)
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if entry, err := UnmarshalFileEntry(line); err == nil {
			entries = append(entries, *entry)
		}
	}
	// Header validation before repair.
	if len(entries) == 0 {
		return entries, nil
	}
	header := entries[0]
	if header.Header == nil || header.Header.ID == "" {
		return nil, nil
	}
	if !strings.HasSuffix(text, "\n") && text != "" {
		_ = os.WriteFile(resolved, []byte(text+"\n"), 0o644)
	}
	return entries, nil
}

// FindMostRecentSession returns the newest session file, optionally filtered
// by cwd (port of findMostRecentSession).
func FindMostRecentSession(sessionDir string, cwd string) *string {
	resolvedCwd := ""
	if cwd != "" {
		resolvedCwd = ResolvePath(cwd, "", PathInputOptions{})
	}
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return nil
	}
	type candidate struct {
		path    string
		modTime time.Time
	}
	var files []candidate
	for _, dirEntry := range entries {
		if !strings.HasSuffix(dirEntry.Name(), ".jsonl") {
			continue
		}
		full := filepath.Join(sessionDir, dirEntry.Name())
		header := readSessionHeaderForDiscovery(full)
		if header == nil {
			continue
		}
		if resolvedCwd != "" && (header.Cwd == "" || ResolvePath(header.Cwd, "", PathInputOptions{}) != resolvedCwd) {
			continue
		}
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		files = append(files, candidate{path: full, modTime: info.ModTime()})
	}
	for i := 1; i < len(files); i++ {
		for j := i; j > 0 && files[j].modTime.After(files[j-1].modTime); j-- {
			files[j], files[j-1] = files[j-1], files[j]
		}
	}
	if len(files) == 0 {
		return nil
	}
	return &files[0].path
}

func readSessionHeaderForDiscovery(filePath string) *SessionHeader {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		entry, err := UnmarshalFileEntry(line)
		if err != nil {
			continue
		}
		if entry.Header == nil || entry.Header.ID == "" {
			return nil
		}
		return entry.Header
	}
	return nil
}

var _ = sync.Mutex{}
