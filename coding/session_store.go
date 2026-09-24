package coding

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dat267/pier/ai"
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
	// Data is the custom entry payload (upstream CustomEntry.data; D129).
	Data json.RawMessage `json:"data,omitempty"`

	// custom_message
	Content json.RawMessage `json:"content,omitempty"`
	Display *bool           `json:"display,omitempty"`

	// label
	TargetID string  `json:"targetId,omitempty"`
	Label    *string `json:"label,omitempty"`

	// session_info
	Name *string `json:"name,omitempty"`

	// usage: model-attributed usage that is not part of LLM context
	Kind  string  `json:"kind,omitempty"`
	Model string  `json:"model,omitempty"`
	Note  *string `json:"note,omitempty"`

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

// sessionLineMeta is what the index scan extracts from one JSONL line.
type sessionLineMeta struct {
	typ       string
	id        string
	parentID  *string
	timestamp string
	message   []byte // the raw "message" member value, nil when absent
}

// scanSessionLine extracts the top-level members a lazy load needs without a
// structural JSON decode. Message entries are ~98% of a real session file's
// bytes and are complete with their base fields plus the raw message, so the
// loader can skip decoding them entirely. The scanner is conservative: any
// shape it does not fully understand — escaped strings, unexpected members,
// non-objects, malformed structure — returns ok=false and the caller
// full-decodes the line instead, so the two paths can never disagree.
func scanSessionLine(line []byte) (sessionLineMeta, bool) {
	var meta sessionLineMeta
	i := 0
	skipSpace := func() {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t' || line[i] == '\r' || line[i] == '\n') {
			i++
		}
	}
	skipSpace()
	if i >= len(line) || line[i] != '{' {
		return meta, false
	}
	i++
	first := true
	seen := map[string]bool{}
	for {
		skipSpace()
		if i >= len(line) {
			return meta, false
		}
		if line[i] == '}' {
			i++
			break
		}
		if !first {
			if line[i] != ',' {
				return meta, false
			}
			i++
			skipSpace()
			if i < len(line) && line[i] == '}' {
				return meta, false // trailing comma: not valid JSON
			}
		}
		key, n, ok := scanJSONStringBytes(line[i:])
		if !ok || strings.IndexByte(key, '\\') >= 0 {
			// An escaped key decodes to a name the switch would miss, leaving
			// a field silently unset in the shell; reject the fast path.
			return meta, false
		}
		if seen[key] {
			// The v2 decoder rejects duplicate members, so the scanner must
			// not shell out a line the decode would drop.
			return meta, false
		}
		seen[key] = true
		i += n
		skipSpace()
		if i >= len(line) || line[i] != ':' {
			return meta, false
		}
		i++
		skipSpace()
		switch key {
		case "type", "id", "timestamp":
			val, n, ok := scanJSONStringBytes(line[i:])
			if !ok || strings.IndexByte(val, '\\') >= 0 {
				return meta, false
			}
			switch key {
			case "type":
				meta.typ = val
			case "id":
				meta.id = val
			case "timestamp":
				meta.timestamp = val
			}
			i += n
		case "parentId":
			if bytes.HasPrefix(line[i:], []byte("null")) {
				i += 4
			} else {
				val, n, ok := scanJSONStringBytes(line[i:])
				if !ok || strings.IndexByte(val, '\\') >= 0 {
					return meta, false
				}
				parent := val
				meta.parentID = &parent
				i += n
			}
		case "message":
			n, ok := skipJSONValue(line[i:])
			if !ok {
				return meta, false
			}
			meta.message = line[i : i+n]
			i += n
		default:
			// An unknown member means the shell would silently drop it;
			// only lines whose members are all understood can be shelled.
			return meta, false
		}
		first = false
	}
	skipSpace()
	if i != len(line) {
		return meta, false
	}
	return meta, true
}

// scanJSONStringBytes consumes one JSON string and returns its raw inner bytes
// (escape sequences left in; callers reject strings containing them).
func scanJSONStringBytes(s []byte) (content string, consumed int, ok bool) {
	if len(s) == 0 || s[0] != '"' {
		return "", 0, false
	}
	i := 1
	for i < len(s) {
		switch s[i] {
		case '"':
			return string(s[1:i]), i + 1, true
		case '\\':
			i += 2
		default:
			i++
		}
	}
	return "", 0, false
}

// skipJSONValue consumes one JSON value of any type, tracking nesting and
// strings, without validating beyond structure.
func skipJSONValue(s []byte) (consumed int, ok bool) {
	if len(s) == 0 {
		return 0, false
	}
	switch s[0] {
	case '"':
		_, n, ok := scanJSONStringBytes(s)
		return n, ok
	case '{', '[':
		depth := 0
		for i := 0; i < len(s); i++ {
			switch s[i] {
			case '"':
				_, n, ok := scanJSONStringBytes(s[i:])
				if !ok {
					return 0, false
				}
				i += n - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1, true
				}
			}
		}
		return 0, false
	case 't':
		if bytes.HasPrefix(s, []byte("true")) {
			return 4, true
		}
	case 'f':
		if bytes.HasPrefix(s, []byte("false")) {
			return 5, true
		}
	case 'n':
		if bytes.HasPrefix(s, []byte("null")) {
			return 4, true
		}
	default:
		for i := 0; i < len(s); i++ {
			switch {
			case s[i] >= '0' && s[i] <= '9', s[i] == '-', s[i] == '+', s[i] == '.', s[i] == 'e', s[i] == 'E':
			default:
				if i == 0 {
					return 0, false
				}
				return i, true
			}
		}
		return len(s), true
	}
	return 0, false
}

// LoadEntriesFromFileBuffered loads a session file the lazy way: one scan
// extracts per-line metadata and the raw message member without a structural
// decode, and message entries become complete shells referencing the buffer.
// The returned buffer backs every raw subslice and must be kept alive alongside
// the entries (OpenSession keeps it on the manager). fast reports whether the
// scanner path was used: older-format files are fully decoded instead, because
// the version migration may rewrite them.
func LoadEntriesFromFileBuffered(filePath string) ([]FileEntry, []byte, bool, error) {
	resolved := NormalizePath(filePath, PathInputOptions{})
	if _, err := os.Stat(resolved); err != nil {
		return nil, nil, false, nil
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, nil, false, err
	}
	// The header's version decides the path: a file that needs the v2->v3
	// migration is fully decoded so the migration sees exactly what it always
	// has.
	fast := true
	if headerLine, _, found := firstJSONLLine(data); found {
		if headerEntry, err := UnmarshalFileEntry(string(headerLine)); err == nil && headerEntry.Header != nil {
			if headerEntry.Header.Version != nil && *headerEntry.Header.Version < CurrentSessionVersion {
				fast = false
			}
		}
	}

	var entries []FileEntry
	start := 0
	for start < len(data) {
		end := bytes.IndexByte(data[start:], '\n')
		var line []byte
		if end == -1 {
			line = data[start:]
			start = len(data)
		} else {
			line = data[start : start+end]
			start += end + 1
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if fast {
			if meta, ok := scanSessionLine(line); ok && meta.typ == "message" && meta.id != "" {
				entry := &SessionEntry{
					Type: meta.typ,
					SessionEntryBase: SessionEntryBase{
						ID:        meta.id,
						ParentID:  meta.parentID,
						Timestamp: meta.timestamp,
					},
					Message: json.RawMessage(meta.message),
				}
				entry.raw = json.RawMessage(line)
				entries = append(entries, FileEntry{Entry: entry})
				continue
			}
		}
		entry, err := UnmarshalFileEntry(string(line))
		if err == nil {
			entries = append(entries, *entry)
		}
	}
	// Header validation before repair, as in the eager loader.
	if len(entries) == 0 {
		return entries, data, fast, nil
	}
	header := entries[0]
	if header.Header == nil || header.Header.ID == "" {
		return entries, data, fast, nil
	}
	if !bytes.HasSuffix(data, []byte("\n")) && len(data) > 0 {
		_ = os.WriteFile(resolved, append(append([]byte{}, data...), '\n'), 0o644)
	}
	return entries, data, fast, nil
}

// firstJSONLLine returns the first non-blank line of the buffer.
func firstJSONLLine(data []byte) (line []byte, start int, found bool) {
	s := 0
	for s < len(data) {
		end := bytes.IndexByte(data[s:], '\n')
		var line []byte
		if end == -1 {
			line = data[s:]
			s = len(data)
		} else {
			line = data[s : s+end]
			s += end + 1
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		return line, s, true
	}
	return nil, 0, false
}

// UnmarshalFileEntry decodes one JSONL line.
// entryTypeFast reads the top-level "type" member straight from the prefix.
// Our writer (and upstream's) emits "type" first, so this dispatches without
// decoding: the probe pass otherwise scans and validates the entire line to
// read one field, doubling the JSON work on 23k-line session loads. Anything
// that does not match the shape falls back to the probe decode.
func entryTypeFast(line string) (string, bool) {
	const prefix = `{"type":"`
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	rest := line[len(prefix):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}

func UnmarshalFileEntry(line string) (*FileEntry, error) {
	// encoding/json/v2 directly: the v1 API wraps v2 with legacy compatibility
	// (case-insensitive members, etc.) that costs ~2.5x on the 23k-line session
	// loads — measured at 707ms vs 276ms against upstream for a 55MB file.
	// Custom UnmarshalJSON methods on the message types still run, through v2's
	// arshaler wrapper.
	typ, fast := entryTypeFast(line)
	if !fast {
		var probe struct {
			Type string `json:"type"`
		}
		if err := jsonv2.Unmarshal([]byte(line), &probe); err != nil {
			return nil, err
		}
		typ = probe.Type
	}
	if typ == "session" {
		var header SessionHeader
		if err := jsonv2.Unmarshal([]byte(line), &header); err != nil {
			return nil, err
		}
		header.raw = json.RawMessage(line)
		return &FileEntry{Header: &header}, nil
	}
	var entry SessionEntry
	if err := jsonv2.Unmarshal([]byte(line), &entry); err != nil {
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

// sessionIDRegex matches a session id: alphanumeric at both ends, with
// '-', '_' and '.' allowed inside.
var sessionIDRegex = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)

// AssertValidSessionID validates a user-provided session id.
func AssertValidSessionID(id string) error {
	if !sessionIDRegex.MatchString(id) {
		return fmt.Errorf("Session id must be non-empty, contain only alphanumeric characters, '-', '_', and '.', and start and end with an alphanumeric character")
	}
	return nil
}

// generateID makes a unique short id (8 hex chars, collision-checked). The
// characters come from the random tail of the UUID: upstream slices a random
// (v4) UUID, whose every character is random, while a UUIDv7 starts with the
// millisecond timestamp — slicing its head made every id generated in the same
// ~4.3 s window identical, so the retry loop burned 100 UUIDs and the fallback
// handed out full-length ids.
func generateID(byID map[string]*SessionEntry) string {
	for i := 0; i < 100; i++ {
		id := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(UUIDv7(), "-", ""), "_", ""), ".", "")
		id = id[len(id)-8:]
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
	return getSessionContextSettingsFromPointers(materializePointers(path))
}

// getSessionContextSettingsFromPointers resolves thinking level and model from
// a path of pointers.
func getSessionContextSettingsFromPointers(path []*SessionEntry) (thinkingLevel string, model *SessionModelRef) {
	// Later entries win, so resolve both by scanning from the end and stopping
	// at the first hit for each. Decoding every message entry to look for the
	// last assistant (as the forward scan did) made a projection of a large
	// session O(messages) in JSON parsing for settings alone — it is what
	// AppendCompaction paid under the session lock.
	thinkingLevel = "off"
	foundThinking := false
	for i := len(path) - 1; i >= 0; i-- {
		entry := path[i]
		if !foundThinking && entry.Type == "thinking_level_change" {
			thinkingLevel = entry.ThinkingLevel
			foundThinking = true
		}
		if model == nil {
			switch entry.Type {
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
		if foundThinking && model != nil {
			break
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
	// Entries are the context entries the messages were resolved from
	// (upstream buildSessionProjection returns them alongside the messages).
	Entries []SessionEntry
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
		return []ai.Message{CreateCustomMessage(entry.CustomType, string(entry.Content), entry.Display != nil && *entry.Display, entryTimestampMS(entry))}
	case "branch_summary":
		if entry.Summary != "" {
			return []ai.Message{CreateBranchSummaryMessage(entry.Summary, entry.FromID, entryTimestampMS(entry))}
		}
	case "compaction":
		// The summary stands in for the entries it replaced, so it is stamped
		// with the compaction's own time (upstream passes entry.timestamp to the
		// message factory, which converts it).
		summary := CreateCompactionSummaryMessage(entry.Summary, entry.TokensBefore, entryTimestampMS(entry))
		// A compaction carries the system message its summary replaced (upstream
		// appendCompaction records it), and the projection emits it first so a
		// resumed session keeps the tool declarations.
		if system := recordedSystemMessage(entry); system != nil {
			return []ai.Message{system, summary}
		}
		return []ai.Message{summary}
	}
	return nil
}

// recordedSystemMessage reads the system message a compaction recorded. An
// unreadable one is dropped; the summary is never lost with it.
func recordedSystemMessage(entry *SessionEntry) *ai.SystemMessage {
	if len(entry.SystemMessageJSON) == 0 {
		return nil
	}
	message := &ai.SystemMessage{}
	if err := json.Unmarshal(entry.SystemMessageJSON, message); err != nil {
		return nil
	}
	return message
}

// BuildContextEntries builds the active, compaction-aware session entry
// list following the leaf path (port of buildContextEntries).
func BuildContextEntries(entries []SessionEntry, leafID *string, byID map[string]*SessionEntry) []SessionEntry {
	return buildContextEntries(entries, leafID, byID, nil)
}

// buildContextEntries is BuildContextEntries reading entries through the
// session's message cache (nil is the reference path).
func buildContextEntries(entries []SessionEntry, leafID *string, byID map[string]*SessionEntry, cache *messageCache) []SessionEntry {
	return compactionWindowFromValues(buildSessionPath(entries, leafID, byID), cache)
}

// applyCompactionWindow drops the entries a compaction replaced, keeping the
// summary and the window after firstKeptEntryId (the tail of
// BuildContextEntries, reusable on an already-resolved path).
func applyCompactionWindow(path []SessionEntry) []SessionEntry {
	return compactionWindowFromValues(path, nil)
}

// compactionWindowFromValues is the window applied to a value path. Without a
// compaction the path is the window and is returned as it is.
func compactionWindowFromValues(path []SessionEntry, cache *messageCache) []SessionEntry {
	for i := range path {
		if path[i].Type == "compaction" {
			return compactionWindowFromPointers(materializePointers(path), cache)
		}
	}
	return path
}

// compactionWindowFromPointers applies the last compaction on the branch to a
// path of pointers and materializes only the entries the window keeps. A
// projection of a large compacted session used to copy every entry on the path
// (and, before that, the whole entry tree); the pointer walk makes the
// resolution proportional to what it hands out, not to the session size.
func compactionWindowFromPointers(path []*SessionEntry, cache *messageCache) []SessionEntry {
	var kept []SessionEntry
	walkCompactionWindow(path, cache, func(entry *SessionEntry) {
		kept = append(kept, *entry)
	})
	if kept == nil {
		return nil
	}
	return kept
}

// walkCompactionWindow visits the entries the window keeps, in order, without
// materializing them. The message count a ContextSignature needs comes from
// here, so a signature of a large session never copies its path.
func walkCompactionWindow(path []*SessionEntry, cache *messageCache, visit func(*SessionEntry)) {
	compactionIdx := -1
	for i := range path {
		if path[i].Type == "compaction" {
			compactionIdx = i
		}
	}
	if compactionIdx < 0 {
		for _, entry := range path {
			visit(entry)
		}
		return
	}
	compaction := path[compactionIdx]
	visit(compaction)
	foundFirstKept := false
	for i := 0; i < compactionIdx; i++ {
		entry := path[i]
		if entry.ID == compaction.FirstKeptEntryID {
			foundFirstKept = true
		}
		if foundFirstKept && !(entry.Type == "message" && projectedRole(entry, cache) == "system") {
			visit(entry)
		}
	}
	for _, entry := range path[compactionIdx+1:] {
		visit(entry)
	}
}

// materializePointers views a value path as pointers for the reference
// (BuildSessionContext / BuildContextEntries) callers.
func materializePointers(entries []SessionEntry) []*SessionEntry {
	pointers := make([]*SessionEntry, len(entries))
	for i := range entries {
		pointers[i] = &entries[i]
	}
	return pointers
}

// materializeEntries copies a pointer path into the entries a caller hands out.
func materializeEntries(pointers []*SessionEntry) []SessionEntry {
	if pointers == nil {
		return nil
	}
	entries := make([]SessionEntry, 0, len(pointers))
	for _, entry := range pointers {
		entries = append(entries, *entry)
	}
	return entries
}

// BuildSessionContext resolves the LLM context from the entry tree
// (port of buildSessionContext).
func BuildSessionContext(entries []SessionEntry, leafID *string, byID map[string]*SessionEntry) SessionContext {
	return buildSessionContext(entries, leafID, byID, nil)
}

// buildSessionContext is BuildSessionContext reading entries through the
// session's message cache (nil is the reference path). The cache is what keeps
// a resolution after an append from decoding the rest of the session.
func buildSessionContext(entries []SessionEntry, leafID *string, byID map[string]*SessionEntry, cache *messageCache) SessionContext {
	// One path walk: the settings scan and the compaction window both work on
	// the resolved path instead of walking the tree again.
	return buildSessionContextFromPointers(materializePointers(buildSessionPath(entries, leafID, byID)), cache)
}

// buildSessionContextFromPointers is buildSessionContext for a path of
// pointers: it resolves settings and the window without copying the path, and
// copies only the entries the projection hands out.
func buildSessionContextFromPointers(path []*SessionEntry, cache *messageCache) SessionContext {
	thinkingLevel, model := getSessionContextSettingsFromPointers(path)
	contextEntries := compactionWindowFromPointers(path, cache)
	messages := make([]ai.Message, 0, len(contextEntries))
	for i := range contextEntries {
		messages = append(messages, projectedMessages(&contextEntries[i], cache)...)
	}
	return SessionContext{Messages: messages, ThinkingLevel: thinkingLevel, Model: model, Entries: contextEntries}
}

// DefaultAgentDir is the agent config directory (upstream getDefaultAgentDir
// aliases getAgentDir, which honors PI_CODING_AGENT_DIR).
func DefaultAgentDir() string {
	return GetAgentDir()
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

// AppendUsage records model-attributed usage that does not participate in LLM
// context (upstream appendUsage; kind is a category such as "cache_warm").
func (m *SessionManager) AppendUsage(kind, provider, model string, usage ai.Usage, note string) *SessionEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.nextEntry("usage")
	entry.Kind = kind
	entry.Provider = provider
	entry.Model = model
	copied := usage
	entry.Usage = &copied
	if note != "" {
		noteCopy := note
		entry.Note = &noteCopy
	}
	m.appendEntry(&entry)
	return &entry
}
