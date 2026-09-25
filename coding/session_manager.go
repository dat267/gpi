package coding

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/internal/offloop"
)

// Port of the SessionManager class: append-only JSONL session trees.

// SessionManager manages conversation sessions as append-only trees stored
// in JSONL files. Each entry has an id and parentId forming a tree; the leaf
// pointer tracks the current position. Appending creates a child of the
// current leaf; branching moves the leaf to an earlier entry.
type SessionManager struct {
	mu sync.Mutex

	sessionID   string
	sessionFile string
	sessionDir  string
	cwd         string
	persist     bool
	flushed     bool

	// writeQueue, when wired, carries the file writes (offloop); nil keeps
	// them synchronous. See SessionManagerOptions.WriteQueue.
	writeQueue *offloop.Queue

	fileEntries []FileEntry
	// loadedData is the file buffer OpenSession loaded from. Raw message and
	// line subslices of lazily loaded entries alias it, so it is retained for
	// the manager's lifetime; it costs one copy of the file instead of the
	// per-entry copies the eager decode made.
	loadedData []byte

	// cacheScan seeding: the loaded prefix is consumed by a background
	// goroutine (it parses every message's JSON, ~250ms on a 55MB session),
	// while appends queue until the seed installs. lazySeedPending marks the
	// one loadEntries call whose buildIndex must skip the consume; a later
	// buildIndex (fork, session switch) cancels the in-flight seed and consumes
	// synchronously instead. cacheSeedGen invalidates an in-flight seed when
	// the scan state is reset underneath it.
	lazySeedPending  bool
	cacheScanSeeding bool
	cacheScanPending []*SessionEntry
	cacheSeedGen     int
	cacheSeedDone    chan struct{}
	byID             map[string]*SessionEntry
	labelsByID       map[string]string
	labelTimestamps  map[string]string
	leafID           *string

	// branchCache memoizes the current leaf's root path. GetBranch("") is
	// called on every footer paint (context usage) and rebuilding a long
	// session's branch is O(entries). The key is (leaf, entry count); the
	// result is shared read-only.
	// projection is the cached session projection for the branch above; see
	// session_projection.go.
	projection projectionCache
	// messages memoizes each entry's decoded messages and role, so a projection
	// after an append only decodes the appended entries; see
	// session_messagecache.go.
	messages messageCache
	// cacheScan is the running cache-miss scan state, so the cache-miss notice
	// for an assistant message does not rescan the session; see cachestats.go.
	cacheScan cacheScanState

	// branchCache holds the current leaf's path twice: pointers for the
	// internal readers (Projection, ContextSignature), which never copy the
	// path, and values for callers that hand it out (GetBranch, the footer).
	// Both are keyed by the branch version and built independently.
	branchCacheVersion   projectionVersion
	branchCachePtrs      []*SessionEntry
	branchCacheValues    []SessionEntry
	branchCacheHasValues bool
	branchCacheValid     bool
}

// SessionManagerOptions are the constructor inputs.
type SessionManagerOptions struct {
	// SessionDir is the directory for session files.
	SessionDir string
	// Persist enables file persistence. Default true.
	Persist *bool
	// WriteQueue, when non-nil, moves session file writes off the calling
	// goroutine through the internal/offloop uniform mechanism: the line is
	// marshaled synchronously (the caller's lock stays authoritative) and the
	// file write runs on the queue's worker in submission order. Interactive
	// mode wires one because its callers include the UI loop, which must never
	// block; nil (the default) keeps writes synchronous for one-shot
	// consumers. A manager with a queue must have FlushWrites called before
	// process exit.
	WriteQueue *offloop.Queue
}

// NewSessionManager creates a session manager with a fresh session
// (SessionManager.create / inMemory).
func NewSessionManager(cwd string, options *SessionManagerOptions) *SessionManager {
	persist := true
	if options != nil && options.Persist != nil {
		persist = *options.Persist
	}
	sessionDir := ""
	if options != nil {
		sessionDir = options.SessionDir
	}
	m := &SessionManager{
		cwd:             ResolvePath(cwd, "", PathInputOptions{}),
		sessionDir:      NormalizePath(sessionDir, PathInputOptions{}),
		persist:         persist,
		byID:            map[string]*SessionEntry{},
		labelsByID:      map[string]string{},
		labelTimestamps: map[string]string{},
	}
	if options != nil {
		m.writeQueue = options.WriteQueue
	}
	// Without an explicit directory, persisted sessions live under the agent
	// dir's per-cwd sessions path (upstream create -> getDefaultSessionDir);
	// in-memory sessions (persist=false) keep no directory.
	if persist && m.sessionDir == "" {
		m.sessionDir = DefaultSessionDir(cwd, "")
	}
	if persist && m.sessionDir != "" {
		if _, err := os.Stat(m.sessionDir); err != nil {
			_ = os.MkdirAll(m.sessionDir, 0o755)
		}
	}
	m.NewSession(nil)
	return m
}

// NewSessionOptions are NewSession inputs.
type NewSessionOptions struct {
	ID            string
	ParentSession *string
}

// NewSession starts a fresh session (v3 header).
func (m *SessionManager) NewSession(options *NewSessionOptions) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if options != nil && options.ID != "" {
		if err := AssertValidSessionID(options.ID); err != nil {
			panic(err)
		}
	}
	if options != nil && options.ID != "" {
		m.sessionID = options.ID
	} else {
		m.sessionID = UUIDv7()
	}
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	header := SessionHeader{
		Version:   intPtr(CurrentSessionVersion),
		ID:        m.sessionID,
		Timestamp: timestamp,
		Cwd:       m.cwd,
	}
	if options != nil {
		header.ParentSession = options.ParentSession
	}
	m.fileEntries = []FileEntry{{Header: &header}}
	m.byID = map[string]*SessionEntry{}
	m.labelsByID = map[string]string{}
	m.labelTimestamps = map[string]string{}
	m.leafID = nil
	m.messages.reset()
	m.cacheScan.reset()
	m.cacheSeedGen++
	m.cacheScanSeeding = false
	m.cacheScanPending = nil
	m.flushed = false

	if m.persist {
		fileTimestamp := strings.NewReplacer(":", "-", ".", "-").Replace(timestamp)
		m.sessionFile = filepath.Join(m.SessionDir(), fmt.Sprintf("%s_%s.jsonl", fileTimestamp, m.sessionID))
	}
	return m.sessionFile
}

func intPtr(n int) *int { return &n }

// OpenSession loads a session file (SessionManager.open).
func OpenSession(path string, sessionDir string, cwdOverride string) (*SessionManager, error) {
	resolvedPath := ResolvePath(path, "", PathInputOptions{})
	m := &SessionManager{
		persist:         true,
		byID:            map[string]*SessionEntry{},
		labelsByID:      map[string]string{},
		labelTimestamps: map[string]string{},
	}
	entries, data, _, err := LoadEntriesFromFileBuffered(resolvedPath)
	if err != nil {
		return nil, err
	}
	m.loadedData = data
	cwd := cwdOverride
	if cwd == "" {
		if len(entries) > 0 && entries[0].Header != nil {
			cwd = entries[0].Header.Cwd
		}
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
	}
	m.cwd = ResolvePath(cwd, "", PathInputOptions{})
	if sessionDir != "" {
		m.sessionDir = NormalizePath(sessionDir, PathInputOptions{})
	} else {
		m.sessionDir = filepath.Dir(resolvedPath)
	}

	if fileExists(resolvedPath) {
		if len(entries) == 0 {
			info, _ := os.Stat(resolvedPath)
			if info.Size() > 0 {
				return nil, fmt.Errorf("Session file is not a valid pi session: %s", resolvedPath)
			}
			m.NewSession(nil)
			m.sessionFile = resolvedPath
			m.rewriteFile()
			m.flushed = true
			return m, nil
		}
		m.sessionFile = resolvedPath
		m.lazySeedPending = true
		m.loadEntries(entries, nil)
		m.seedCacheScanInBackground()
		m.flushed = true
	} else {
		m.NewSession(nil)
		m.sessionFile = resolvedPath
	}
	return m, nil
}

// ContinueRecentSession continues the most recent session or creates one
// (SessionManager.continueRecent).
func ContinueRecentSession(cwd string, sessionDir string) *SessionManager {
	dir := sessionDir
	if dir == "" {
		dir = DefaultSessionDir(cwd, "")
	}
	filterCwd := sessionDir != "" && dir != DefaultSessionDir(cwd, "")
	filterValue := ""
	if filterCwd {
		filterValue = cwd
	}
	if mostRecent := FindMostRecentSession(dir, filterValue); mostRecent != nil {
		m, err := OpenSession(*mostRecent, dir, cwd)
		if err == nil {
			return m
		}
	}
	return NewSessionManager(cwd, &SessionManagerOptions{SessionDir: dir})
}

// InMemorySession builds a non-persisting session, optionally from entries
// (SessionManager.inMemory).
func InMemorySession(cwd string, options *NewSessionOptions, entries []FileEntry) *SessionManager {
	m := NewSessionManager(cwd, &SessionManagerOptions{Persist: boolPtr(false)})
	if len(entries) > 0 {
		m.loadEntries(entries, options)
	}
	return m
}

// loadEntries initializes from file entries (port of _loadEntries).
func (m *SessionManager) loadEntries(entries []FileEntry, options *NewSessionOptions) {
	var header *SessionHeader
	for _, entry := range entries {
		if entry.Header != nil {
			header = entry.Header
			break
		}
	}
	if header != nil {
		m.fileEntries = entries
		m.sessionID = header.ID
		if migrateToCurrentVersion(m.fileEntries) {
			m.rewriteFile()
		}
	} else {
		m.NewSession(options)
		m.fileEntries = append(m.fileEntries, entries...)
	}
	m.buildIndex()
}

// seedCacheScanInBackground consumes the lazily loaded entries' cache-scan
// facts off the UI goroutine: the scan parses every message's JSON, ~250ms on
// a 55MB session, and the load path must not pay it. Until the seed installs,
// cache statistics are empty and appends queue in order; the seed installs
// under the manager lock and drains the queue, so the combined state matches
// an eager consume exactly.
func (m *SessionManager) seedCacheScanInBackground() {
	m.cacheScanSeeding = true
	m.cacheSeedDone = make(chan struct{})
	gen := m.cacheSeedGen
	shells := make([]FileEntry, len(m.fileEntries))
	copy(shells, m.fileEntries)
	go func() {
		var state cacheScanState
		for i := range shells {
			if entry := shells[i].Entry; entry != nil {
				state.consume(entry)
			}
		}
		m.mu.Lock()
		if m.cacheSeedGen != gen {
			m.mu.Unlock()
			return
		}
		m.cacheScan.prev = state.prev
		m.cacheScan.candidates = state.candidates
		m.cacheScan.entryIndex = state.entryIndex
		m.cacheScanSeeding = false
		pending := m.cacheScanPending
		m.cacheScanPending = nil
		for _, entry := range pending {
			m.cacheScan.consume(entry)
		}
		m.mu.Unlock()
		close(m.cacheSeedDone)
	}()
}

func (m *SessionManager) buildIndex() {
	lazy := m.lazySeedPending
	m.lazySeedPending = false
	if !lazy && m.cacheScanSeeding {
		// A rebuild while a seed is in flight (fork, session switch) replaces
		// the entry list the seed was walking: cancel it and consume this
		// list synchronously. Queued appends belong to the replaced list.
		m.cacheSeedGen++
		m.cacheScanSeeding = false
		m.cacheScanPending = nil
	}
	m.byID = map[string]*SessionEntry{}
	m.labelsByID = map[string]string{}
	m.labelTimestamps = map[string]string{}
	m.leafID = nil
	m.branchCacheValid = false
	// Re-seed the cache-miss scan state over the loaded entries, in file order.
	m.cacheScan.reset()
	for i := range m.fileEntries {
		entry := m.fileEntries[i].Entry
		if entry == nil {
			continue
		}
		if !lazy {
			m.cacheScan.consume(entry)
		}
		m.byID[entry.ID] = entry
		leafID := entry.ID
		m.leafID = &leafID
		if entry.Type == "label" && entry.TargetID != "" {
			if entry.Label != nil && *entry.Label != "" {
				m.labelsByID[entry.TargetID] = *entry.Label
				m.labelTimestamps[entry.TargetID] = entry.Timestamp
			} else {
				delete(m.labelsByID, entry.TargetID)
				delete(m.labelTimestamps, entry.TargetID)
			}
		}
	}
}

func (m *SessionManager) rewriteFile() {
	if !m.persist || m.sessionFile == "" {
		return
	}
	if m.writeQueue == nil {
		var buf strings.Builder
		for _, entry := range m.fileEntries {
			line, err := MarshalFileEntry(entry)
			if err != nil {
				continue
			}
			buf.WriteString(line)
		}
		_ = os.WriteFile(m.sessionFile, []byte(buf.String()), 0o644)
		return
	}
	// Queued: snapshot the entries (a shallow pointer copy taken under the
	// manager lock) and let the worker marshal and write them, ordered behind
	// any queued appends by the same FIFO. The marshal moves off the loop with
	// the write; on a large loaded session that is the expensive part.
	snapshot := make([]FileEntry, len(m.fileEntries))
	copy(snapshot, m.fileEntries)
	path := m.sessionFile
	m.writeQueue.Go(func() {
		var buf strings.Builder
		for _, entry := range snapshot {
			line, err := MarshalFileEntry(entry)
			if err != nil {
				continue
			}
			buf.WriteString(line)
		}
		_ = os.WriteFile(path, []byte(buf.String()), 0o644)
	})
}

// FlushWrites blocks until every queued session file write has finished.
// Shutdown calls it so a clean exit cannot lose the session tail; nil-queue
// managers have nothing to flush.
func (m *SessionManager) FlushWrites() {
	if m.writeQueue != nil {
		m.writeQueue.Flush()
	}
}

// GetWriteQueue returns the wired write queue, or nil for synchronous writes.
func (m *SessionManager) GetWriteQueue() *offloop.Queue { return m.writeQueue }

// SetWriteQueue wires (or replaces) the write queue; used by the interactive
// wiring for managers it opens ad hoc (session rename, model list).
func (m *SessionManager) SetWriteQueue(queue *offloop.Queue) { m.writeQueue = queue }

// IsPersisted reports file persistence.
func (m *SessionManager) IsPersisted() bool { return m.persist }

// GetCwd returns the session working directory.
func (m *SessionManager) GetCwd() string { return m.cwd }

// GetSessionDir returns the session directory.
func (m *SessionManager) GetSessionDir() string { return m.sessionDir }

// GetSessionID returns the session id.
func (m *SessionManager) GetSessionID() string { return m.sessionID }

// GetSessionFile returns the session file path ("" when unset).
func (m *SessionManager) GetSessionFile() string { return m.sessionFile }

// persist writes the entry, honoring the flush-on-first-assistant contract:
// the file is only created once an assistant message exists (upstream
// _persist).
func (m *SessionManager) persistEntry(entry *SessionEntry) {
	if !m.persist || m.sessionFile == "" {
		return
	}
	// Upstream checks `fileEntries.some(...)`: stop at the first assistant
	// instead of unmarshalling every message on every append.
	hasAssistant := false
	for i := range m.fileEntries {
		e := m.fileEntries[i].Entry
		if e != nil && e.Type == "message" {
			var msg struct {
				Role string `json:"role"`
			}
			if json.Unmarshal(e.Message, &msg) == nil && msg.Role == "assistant" {
				hasAssistant = true
				break
			}
		}
	}
	if !hasAssistant {
		if m.flushed {
			m.appendLine(entry)
		} else {
			m.flushed = false
		}
		return
	}
	if !m.flushed {
		m.rewriteFile()
		m.flushed = true
	} else {
		m.appendLine(entry)
	}
}

func (m *SessionManager) appendLine(entry *SessionEntry) {
	line, err := MarshalFileEntry(FileEntry{Entry: entry})
	if err != nil {
		return
	}
	// The path and the marshaled line are captured under the manager lock;
	// the worker touches no manager state.
	path := m.sessionFile
	write := func() { writeSessionLine(path, line) }
	if m.writeQueue == nil {
		write()
		return
	}
	m.writeQueue.Go(write)
}

// writeSessionLine appends one pre-marshaled line to the session file.
func writeSessionLine(path string, line string) {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.WriteString(line)
}

func (m *SessionManager) appendEntry(entry *SessionEntry) string {
	m.fileEntries = append(m.fileEntries, FileEntry{Entry: entry})
	m.byID[entry.ID] = entry
	leafID := entry.ID
	m.leafID = &leafID
	m.cacheScan.consume(entry)
	m.persistEntry(entry)
	return entry.ID
}

// nextEntry builds an entry child of the current leaf.
func (m *SessionManager) nextEntry(entryType string) SessionEntry {
	return SessionEntry{
		Type: entryType,
		SessionEntryBase: SessionEntryBase{
			ID:        generateID(m.byID),
			ParentID:  m.leafID,
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		},
	}
}

// AppendMessage appends a message as a child of the current leaf.
func (m *SessionManager) AppendMessage(message ai.Message) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	encoded, err := ai.MarshalMessage(message)
	if err != nil {
		panic(err)
	}
	entry := m.nextEntry("message")
	entry.Message = encoded
	return m.appendEntry(&entry)
}

// AppendThinkingLevelChange appends a thinking level change.
func (m *SessionManager) AppendThinkingLevelChange(thinkingLevel string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.nextEntry("thinking_level_change")
	entry.ThinkingLevel = thinkingLevel
	return m.appendEntry(&entry)
}

// AppendModelChange appends a model change.
func (m *SessionManager) AppendModelChange(provider, modelID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.nextEntry("model_change")
	entry.Provider = provider
	entry.ModelID = modelID
	return m.appendEntry(&entry)
}

// AppendCompaction appends a compaction summary with the prompt/tool state
// captured at the boundary.
func (m *SessionManager) AppendCompaction(summary string, firstKeptEntryID string, tokensBefore int64, details json.RawMessage, fromHook bool, usage *ai.Usage) string {
	// Resolve the carried system message from a projection taken outside the
	// lock: projecting a large session costs hundreds of milliseconds, and
	// holding m.mu across it parks every other reader (the footer's per-frame
	// session reads) for that long. Upstream is single-threaded and pays the
	// same projection, which its parsed-object entries make cheap.
	systemMessage := m.CurrentSystemMessage()
	m.mu.Lock()
	defer m.mu.Unlock()
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	entry := m.nextEntry("compaction")
	entry.Timestamp = timestamp
	entry.Summary = summary
	entry.FirstKeptEntryID = firstKeptEntryID
	entry.TokensBefore = tokensBefore
	entry.Details = details
	entry.Usage = usage
	if fromHook {
		entry.FromHook = boolPtr(true)
	}
	if systemMessage != nil {
		systemMessage.Timestamp = time.Now().UnixMilli()
		entry.SystemMessageJSON = mustMarshalJSON(systemMessage)
	}
	return m.appendEntry(&entry)
}

// AppendCustomEntry appends an extension state entry (no LLM context).
func (m *SessionManager) AppendCustomEntry(customType string, data json.RawMessage) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.nextEntry("custom")
	entry.CustomType = customType
	entry.Data = data
	return m.appendEntry(&entry)
}

// AppendSessionInfo appends a display-name entry.
func (m *SessionManager) AppendSessionInfo(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	sanitizedName := strings.TrimSpace(newlinePattern.ReplaceAllString(name, " "))
	entry := m.nextEntry("session_info")
	entry.Name = &sanitizedName
	return m.appendEntry(&entry)
}

var newlinePattern = regexp.MustCompile(`[\r\n]+`)

// GetSessionName resolves the latest session_info name (empty clears).
func (m *SessionManager) GetSessionName() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries := m.getEntriesLocked()
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type == "session_info" && entries[i].Name != nil {
			name := strings.TrimSpace(*entries[i].Name)
			if name != "" {
				return name
			}
			return ""
		}
	}
	return ""
}

// AppendCustomMessageEntry appends an LLM-context custom message entry.
func (m *SessionManager) AppendCustomMessageEntry(customType string, content string, display bool, details json.RawMessage) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.nextEntry("custom_message")
	entry.CustomType = customType
	entry.Content = mustMarshalJSON(content)
	entry.Display = &display
	entry.Details = details
	return m.appendEntry(&entry)
}

// GetLeafID returns the current leaf (nil before any entries).
func (m *SessionManager) GetLeafID() *string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.leafID
}

// GetEntry returns one entry by id.
func (m *SessionManager) GetEntry(id string) *SessionEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byID[id]
}

// GetLabel returns an entry's label.
func (m *SessionManager) GetLabel(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.labelsByID[id]
}

// AppendLabelChange sets or clears a label bookmark.
func (m *SessionManager) AppendLabelChange(targetID string, label *string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[targetID]; !ok {
		return "", fmt.Errorf("Entry %s not found", targetID)
	}
	entry := m.nextEntry("label")
	entry.TargetID = targetID
	entry.Label = label
	id := m.appendEntry(&entry)
	if label != nil && *label != "" {
		m.labelsByID[targetID] = *label
		m.labelTimestamps[targetID] = entry.Timestamp
	} else {
		delete(m.labelsByID, targetID)
		delete(m.labelTimestamps, targetID)
	}
	return id, nil
}

// GetBranch walks from an entry (or the leaf) to the root. The result is
// shared and must be treated as read-only; the current leaf's path is cached
// and only rebuilt when the leaf or the entry set changes.
func (m *SessionManager) GetBranch(fromID string) []SessionEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.branchLocked(fromID)
}

// branchLocked is GetBranch for callers already holding m.mu. The current
// leaf's value path is cached as well, because callers hand it out.
func (m *SessionManager) branchLocked(fromID string) []SessionEntry {
	if fromID == "" {
		if m.branchCacheValid && m.branchCacheHasValues && m.branchCacheVersion == m.projectionVersionLocked() {
			return m.branchCacheValues
		}
		values := materializeEntries(m.branchPointersLocked(""))
		// Drop the spare capacity so a caller's append cannot write into the
		// shared cache's backing array.
		values = values[:len(values):len(values)]
		m.branchCacheValues = values
		m.branchCacheHasValues = true
		return values
	}
	return materializeEntries(m.branchPointersLocked(fromID))
}

// branchPointersLocked walks from an entry (or the leaf) to the root and
// returns pointers into the manager's canonical entries. The leaf's path is
// cached by branch version; the result is shared read-only, and entries are
// immutable after they are appended, so a pointer stays valid for as long as
// the caller needs the path.
func (m *SessionManager) branchPointersLocked(fromID string) []*SessionEntry {
	startID := fromID
	if startID == "" {
		if m.leafID == nil {
			return nil
		}
		startID = *m.leafID
		if m.branchCacheValid && m.branchCacheVersion == m.projectionVersionLocked() {
			return m.branchCachePtrs
		}
	}
	var path []*SessionEntry
	current := m.byID[startID]
	for current != nil {
		path = append(path, current)
		if current.ParentID != nil {
			current = m.byID[*current.ParentID]
		} else {
			current = nil
		}
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	if fromID == "" {
		// Drop the spare capacity so a caller's append cannot write into the
		// shared cache's backing array.
		path = path[:len(path):len(path)]
		m.branchCachePtrs = path
		m.branchCacheVersion = m.projectionVersionLocked()
		m.branchCacheValid = true
		m.branchCacheHasValues = false
	}
	return path
}

// SetMessageDecodeForTest replaces the entry-to-messages step of the session's
// message memo (test seam, like WaitForPendingLoads). Tests use it to make a
// scan's decode observable, e.g. to prove the scan is not running on the UI
// loop.
func (m *SessionManager) SetMessageDecodeForTest(decode func(*SessionEntry) []ai.Message) {
	m.messages.mu.Lock()
	m.messages.decode = decode
	m.messages.mu.Unlock()
}

// BuildContextEntriesForLeaf builds the active compaction-aware entry list
// from the current leaf.
func (m *SessionManager) BuildContextEntriesForLeaf() []SessionEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buildContextEntriesForLeafLocked()
}

// buildContextEntriesForLeafLocked is BuildContextEntriesForLeaf for callers
// already holding m.mu.
func (m *SessionManager) buildContextEntriesForLeafLocked() []SessionEntry {
	return compactionWindowFromPointers(m.branchPointersLocked(""), &m.messages)
}

// GetHeader returns the session header.
func (m *SessionManager) GetHeader() *SessionHeader {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range m.fileEntries {
		if entry.Header != nil {
			return entry.Header
		}
	}
	return nil
}

// GetEntries returns all entries (excluding the header).
func (m *SessionManager) GetEntries() []SessionEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.getEntriesLocked()
}

// Branch starts a new branch from an earlier entry.
func (m *SessionManager) Branch(branchFromID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[branchFromID]; !ok {
		return fmt.Errorf("Entry %s not found", branchFromID)
	}
	leafID := branchFromID
	m.leafID = &leafID
	return nil
}

// ResetLeaf clears the leaf pointer (next append creates a new root).
func (m *SessionManager) ResetLeaf() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.leafID = nil
}

// BranchWithSummary starts a branch with a summary of the abandoned path.
func (m *SessionManager) BranchWithSummary(branchFromID string, summary string, details json.RawMessage, fromHook bool, usage *ai.Usage) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if branchFromID != "" {
		if _, ok := m.byID[branchFromID]; !ok {
			return ""
		}
	}
	fromID := "root"
	if m.leafID != nil {
		fromID = *m.leafID
	}
	m.leafID = &branchFromID
	entry := m.nextEntry("branch_summary")
	entry.ParentID = &branchFromID
	entry.FromID = fromID
	entry.Summary = summary
	entry.Details = details
	entry.Usage = usage
	if fromHook {
		entry.FromHook = boolPtr(true)
	}
	return m.appendEntry(&entry)
}

// CreateBranchedSession writes a new session file holding only the path to
// the given leaf (simplified port: label re-chaining included).
func (m *SessionManager) CreateBranchedSession(leafID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path := m.GetBranch(leafID)
	if len(path) == 0 {
		return "", fmt.Errorf("Entry %s not found", leafID)
	}

	newSessionID := UUIDv7()
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	fileTimestamp := strings.NewReplacer(":", "-", ".", "-").Replace(timestamp)
	newSessionFile := filepath.Join(m.sessionDir, fmt.Sprintf("%s_%s.jsonl", fileTimestamp, newSessionID))

	header := SessionHeader{
		Version:   intPtr(CurrentSessionVersion),
		ID:        newSessionID,
		Timestamp: timestamp,
		Cwd:       m.cwd,
	}
	if m.persist {
		header.ParentSession = &m.sessionFile
	}

	// Re-chain the path (dropping label entries) with fresh parent ids.
	var newEntries []FileEntry
	replacementByLabelID := map[string]string{}
	pathParentID := ""
	for i := range path {
		entry := path[i]
		if entry.Type == "label" {
			replacementByLabelID[entry.ID] = "" // resolved lazily by consumers
			continue
		}
		if entry.Type == "compaction" && entry.FirstKeptEntryID != "" {
			if replacement, ok := replacementByLabelID[entry.FirstKeptEntryID]; ok && replacement != "" {
				entry.FirstKeptEntryID = replacement
			}
		}
		if pathParentID == "" {
			entry.ParentID = nil
		} else {
			parent := pathParentID
			entry.ParentID = &parent
		}
		pathParentID = entry.ID
		newEntries = append(newEntries, FileEntry{Entry: &entry})
	}

	var buf strings.Builder
	headerLine, _ := MarshalFileEntry(FileEntry{Header: &header})
	buf.WriteString(headerLine)
	for _, entry := range newEntries {
		line, _ := MarshalFileEntry(entry)
		buf.WriteString(line)
	}

	m.fileEntries = append([]FileEntry{{Header: &header}}, newEntries...)
	m.sessionID = newSessionID
	m.sessionFile = newSessionFile
	m.buildIndex()

	hasAssistant := false
	for _, entry := range m.fileEntries {
		if entry.Entry != nil && entry.Entry.Type == "message" {
			var msg struct {
				Role string `json:"role"`
			}
			if json.Unmarshal(entry.Entry.Message, &msg) == nil && msg.Role == "assistant" {
				hasAssistant = true
			}
		}
	}
	if m.persist {
		if hasAssistant {
			m.rewriteFile()
			m.flushed = true
		} else {
			m.flushed = false
		}
		return newSessionFile, nil
	}
	return "", nil
}

// SessionInfo is the listing info for one session file.
type SessionInfo struct {
	Path              string
	ID                string
	Cwd               string
	Name              string
	ParentSessionPath string
	Created           time.Time
	Modified          time.Time
	MessageCount      int
	FirstMessage      string
	AllMessagesText   string
}

// ListSessions lists sessions for a cwd's directory, newest first
// (port of SessionManager.list; progress callback omitted).
func ListSessions(cwd string, sessionDir string) []SessionInfo {
	dir := sessionDir
	if dir == "" {
		dir = DefaultSessionDir(cwd, "")
	}
	filterCwd := sessionDir != "" && dir != DefaultSessionDir(cwd, "")
	resolvedCwd := ResolvePath(cwd, "", PathInputOptions{})

	var sessions []SessionInfo
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return sessions
	}
	for _, dirEntry := range dirEntries {
		if !strings.HasSuffix(dirEntry.Name(), ".jsonl") {
			continue
		}
		full := filepath.Join(dir, dirEntry.Name())
		if info := buildSessionInfo(full); info != nil {
			if filterCwd && ResolvePath(info.Cwd, "", PathInputOptions{}) != resolvedCwd {
				continue
			}
			sessions = append(sessions, *info)
		}
	}
	for i := 1; i < len(sessions); i++ {
		for j := i; j > 0 && sessions[j].Modified.After(sessions[j-1].Modified); j-- {
			sessions[j], sessions[j-1] = sessions[j-1], sessions[j]
		}
	}
	return sessions
}

// buildSessionInfo extracts listing metadata (port of buildSessionInfo).
func buildSessionInfo(filePath string) *SessionInfo {
	stats, err := os.Stat(filePath)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	var header *SessionHeader
	messageCount := 0
	firstMessage := ""
	var allMessages []string
	name := ""
	var lastActivityTime int64

	for _, line := range strings.Split(string(data), "\n") {
		entry, err := UnmarshalFileEntry(line)
		if err != nil || (entry.Header == nil && entry.Entry == nil) {
			continue
		}
		if header == nil {
			if entry.Header == nil {
				return nil
			}
			header = entry.Header
			continue
		}
		e := entry.Entry
		if e == nil {
			continue
		}
		if e.Type == "session_info" && e.Name != nil {
			name = strings.TrimSpace(*e.Name)
		}
		if e.Type != "message" {
			continue
		}
		messageCount++
		message, mErr := ai.UnmarshalMessage(e.Message)
		if mErr != nil {
			continue
		}

		// Activity time from the message timestamp.
		var msg struct {
			Timestamp *int64 `json:"timestamp"`
		}
		if json.Unmarshal(e.Message, &msg) == nil && msg.Timestamp != nil && *msg.Timestamp > 0 {
			if *msg.Timestamp > lastActivityTime {
				lastActivityTime = *msg.Timestamp
			}
		}

		role := ai.RoleOf(message)
		if role != ai.RoleUser && role != ai.RoleAssistant {
			continue
		}
		textContent := extractTextContent(message)
		if textContent == "" {
			continue
		}
		allMessages = append(allMessages, textContent)
		if firstMessage == "" && role == ai.RoleUser {
			firstMessage = textContent
		}
	}
	if header == nil {
		return nil
	}

	modified := stats.ModTime()
	if lastActivityTime > 0 {
		modified = time.UnixMilli(lastActivityTime)
	} else if header.Timestamp != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, header.Timestamp); err == nil {
			modified = parsed
		}
	}
	created := stats.ModTime()
	if header.Timestamp != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, header.Timestamp); err == nil {
			created = parsed
		}
	}

	cwd := header.Cwd
	first := firstMessage
	if first == "" {
		first = "(no messages)"
	}
	return &SessionInfo{
		Path: filePath, ID: header.ID, Cwd: cwd, Name: name,
		ParentSessionPath: derefOrEmpty(header.ParentSession),
		Created:           created, Modified: modified,
		MessageCount:    messageCount,
		FirstMessage:    first,
		AllMessagesText: strings.Join(allMessages, " "),
	}
}

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// extractTextContent joins text blocks with spaces (message-content only).
func extractTextContent(message ai.Message) string {
	switch m := message.(type) {
	case *ai.SystemMessage:
		return m.Content.Text
	case *ai.UserMessage:
		return contentTextJoined(m.Content)
	case *ai.AssistantMessage:
		var texts []string
		for _, block := range m.Content {
			if tc, ok := block.(ai.TextContent); ok {
				texts = append(texts, tc.Text)
			}
		}
		return strings.Join(texts, " ")
	case *ai.ToolResultMessage:
		var texts []string
		for _, block := range m.Content {
			if tc, ok := block.(ai.TextContent); ok {
				texts = append(texts, tc.Text)
			}
		}
		return strings.Join(texts, " ")
	}
	return ""
}

func contentTextJoined(content ai.StringOrBlocks) string {
	if content.Blocks == nil {
		return content.Text
	}
	var texts []string
	for _, block := range content.Blocks {
		if tc, ok := block.(ai.TextContent); ok {
			texts = append(texts, tc.Text)
		}
	}
	return strings.Join(texts, " ")
}

// SessionDir returns the session directory (exported accessor).
func (m *SessionManager) SessionDir() string { return m.sessionDir }

// SessionTreeNode is a node of the session entry tree (upstream getTree()).
type SessionTreeNode struct {
	Entry          SessionEntry
	Children       []*SessionTreeNode
	Label          string
	HasLabel       bool
	LabelTimestamp string
}

// GetTree builds the session entry tree with resolved labels.
func (m *SessionManager) GetTree() []*SessionTreeNode {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries := m.getEntriesLocked()

	nodeMap := map[string]*SessionTreeNode{}
	var roots []*SessionTreeNode
	for _, entry := range entries {
		node := &SessionTreeNode{Entry: entry}
		if label, ok := m.labelsByID[entry.ID]; ok {
			node.Label = label
			node.HasLabel = true
			node.LabelTimestamp = m.labelTimestamps[entry.ID]
		}
		nodeMap[entry.ID] = node
	}

	for _, entry := range entries {
		node := nodeMap[entry.ID]
		if entry.ParentID == nil || *entry.ParentID == entry.ID {
			roots = append(roots, node)
			continue
		}
		if parent, ok := nodeMap[*entry.ParentID]; ok {
			parent.Children = append(parent.Children, node)
		} else {
			// Orphan: treat as root.
			roots = append(roots, node)
		}
	}

	// Sort children by timestamp (oldest first).
	stack := append([]*SessionTreeNode{}, roots...)
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		sort.SliceStable(node.Children, func(i int, j int) bool {
			return node.Children[i].Entry.Timestamp < node.Children[j].Entry.Timestamp
		})
		stack = append(stack, node.Children...)
	}
	return roots
}

func (m *SessionManager) getEntriesLocked() []SessionEntry {
	var out []SessionEntry
	for _, entry := range m.fileEntries {
		if entry.Entry != nil {
			out = append(out, *entry.Entry)
		}
	}
	return out
}

// UsesDefaultSessionDir reports whether the session uses the default per-cwd
// directory.
func (m *SessionManager) UsesDefaultSessionDir() bool {
	return m.sessionDir == DefaultSessionDir(m.cwd, "")
}

// ListAllSessions lists sessions across every per-cwd directory (or one custom
// directory) sorted by modification time (port of SessionManager.listAll).
func ListAllSessions(sessionDir string) []SessionInfo {
	if sessionDir != "" {
		// Upstream listAll(customSessionDir) scans the directory without a cwd
		// filter (listSessionsFromDir), never through list().
		return listSessionsFromDir(sessionDir)
	}
	sessionsDir := DefaultSessionsDir()
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return nil
	}
	var sessions []SessionInfo
	for _, entry := range entries {
		// Upstream keeps directories and symlinks.
		if !entry.IsDir() && entry.Type()&fs.ModeSymlink == 0 {
			continue
		}
		sessions = append(sessions, listSessionsFromDir(filepath.Join(sessionsDir, entry.Name()))...)
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Modified.After(sessions[j].Modified)
	})
	return sessions
}

// listSessionsFromDir scans one directory for session files without applying
// any cwd filter (upstream listSessionsFromDir).
func listSessionsFromDir(dir string) []SessionInfo {
	var sessions []SessionInfo
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return sessions
	}
	for _, dirEntry := range dirEntries {
		if !strings.HasSuffix(dirEntry.Name(), ".jsonl") {
			continue
		}
		full := filepath.Join(dir, dirEntry.Name())
		if info := buildSessionInfo(full); info != nil {
			sessions = append(sessions, *info)
		}
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Modified.After(sessions[j].Modified)
	})
	return sessions
}
