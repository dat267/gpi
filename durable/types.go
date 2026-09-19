// Package durable is a Go port of @earendil-works/pi-durable
// (pi/packages/durable): the durable conversation, task, and document runtime.
//
// Ground truth: pi/packages/durable/src at the pinned upstream commit.
//
// D-row D14: upstream's Context (from chord) is the ambient capability handle
// threaded through storage calls; the Go port uses context.Context, and JSON
// payload types (JsonValue, Message[]) map to json.RawMessage and ai.Message.
package durable

import (
	"context"
	"encoding/json"

	"github.com/dat267/gpi/ai"
)

// Id is a session-global identifier shared by every durable record table.
type Id = int64

// Seq is the monotonic sequence assigned to one atomic storage commit.
type Seq = int64

// RootConversationID is the reserved ID of the root conversation.
const RootConversationID Id = 1

// StoredError is a JSON-safe error snapshot persisted instead of a runtime
// error object.
type StoredError struct {
	Message string          `json:"message"`
	Detail  json.RawMessage `json:"detail,omitempty"`
}

// ConversationParent is the fork source and inclusive parent entry through
// which history is inherited.
type ConversationParent struct {
	ConversationID Id `json:"conversationId"`
	At             Id `json:"at"`
}

// ConversationOwner is the creator edge used for authorization, subtree abort,
// and subtree idle waits.
type ConversationOwner struct {
	ConversationID Id `json:"conversationId"`
	TaskID         Id `json:"taskId"`
}

// ConversationRecord is the immutable identity, history ancestry, and task
// ownership of a transcript scope.
type ConversationRecord struct {
	ID     Id                  `json:"id"`
	Parent *ConversationParent `json:"parent,omitempty"`
	Owner  *ConversationOwner  `json:"owner,omitempty"`
}

// Context edit actions.
const (
	EditOmit    = "omit"
	EditReplace = "replace"
)

// ContextEdit is an immutable override of one visible entry's contribution to
// model context.
type ContextEdit struct {
	// Target is the entry whose model messages are omitted or replaced.
	Target Id     `json:"target"`
	Action string `json:"action"`
	// Messages are contributed instead of the target's messages (replace).
	Messages []ai.Message `json:"messages,omitempty"`
}

// EntryRecord is an immutable transcript event with separate model-facing and
// application-facing payloads.
type EntryRecord struct {
	ID             Id `json:"id"`
	ConversationID Id `json:"conversationId"`
	// Kind is the application-defined entry discriminator.
	Kind string `json:"kind"`
	// Model holds messages contributed to model context (absent for display
	// or bookkeeping entries).
	Model []ai.Message `json:"model,omitempty"`
	// Data is the JSON payload consumed by views, plugins, or bookkeeping.
	Data json.RawMessage `json:"data,omitempty"`
	// Head is the first entry in the active context selected by this entry.
	Head *Id `json:"head,omitempty"`
	// Edits are context-only overrides of earlier visible entries.
	Edits []ContextEdit `json:"edits,omitempty"`
	// ByTaskID is the task that appended this entry, for durable work.
	ByTaskID *Id `json:"byTaskId,omitempty"`
}

// EntryDraft is entry content supplied before the session assigns identity and
// task attribution.
type EntryDraft struct {
	Kind  string          `json:"kind"`
	Model []ai.Message    `json:"model,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
	// Head: nil starts no active context; HeadSelf starts it at the newly
	// assigned entry id; otherwise the given entry id.
	Head     *Id           `json:"head,omitempty"`
	HeadSelf bool          `json:"headSelf,omitempty"`
	Edits    []ContextEdit `json:"edits,omitempty"`
}

// Input lifecycle statuses.
const (
	InputQueued     = "queued"
	InputPlaced     = "placed"
	InputDone       = "done"
	InputUnanswered = "unanswered"
)

// Input is the durable lifecycle of one admitted host input.
type Input struct {
	ID             Id `json:"id"`
	ConversationID Id `json:"conversationId"`
	// RequestID is the host-provided deduplication key, scoped to the
	// conversation.
	RequestID *string `json:"requestId,omitempty"`
	// Status is one of queued/placed/done/unanswered.
	Status string `json:"status"`
	// Entry is the user transcript entry created when this input was placed
	// (placed/done/unanswered).
	Entry *Id `json:"entry,omitempty"`
	// Answer is the assistant answer entry (done, absent for passive writes).
	Answer *Id `json:"answer,omitempty"`
	// Reason is a stable machine-readable explanation ("aborted", "stale").
	Reason *string `json:"reason,omitempty"`
	// Detail is optional structured diagnostic data.
	Detail json.RawMessage `json:"detail,omitempty"`
}

// Task outcome statuses.
const (
	OutcomeCompleted = "completed"
	OutcomeFailed    = "failed"
	OutcomeAborted   = "aborted"
	OutcomeOrphaned  = "orphaned"
	OutcomeFaulted   = "faulted"
)

// TaskOutcome is the durable reason and optional result of a terminal task.
type TaskOutcome struct {
	Status string          `json:"status"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *StoredError    `json:"error,omitempty"`
	Reason *string         `json:"reason,omitempty"`
}

// Task state statuses.
const (
	TaskPending  = "pending"
	TaskRunning  = "running"
	TaskTerminal = "terminal"
)

// TaskState is the complete durable execution state of a task.
type TaskState struct {
	// Status is one of pending/running/terminal.
	Status string `json:"status"`
	// Checkpoint is the complete durable state from which execution resumes
	// (pending/running only).
	Checkpoint json.RawMessage `json:"checkpoint,omitempty"`
	// Outcome is set for terminal tasks.
	Outcome *TaskOutcome `json:"outcome,omitempty"`
}

// TaskRecord is the complete replacement record for one durable task state
// machine.
type TaskRecord struct {
	ID             Id `json:"id"`
	ConversationID Id `json:"conversationId"`
	// Kind is the registered task definition name.
	Kind string `json:"kind"`
	// Version is the definition version used to migrate input/checkpoints.
	Version int `json:"version"`
	// Input is the original task input, retained while live or terminal.
	Input json.RawMessage `json:"input,omitempty"`
	// After lists tasks that must be terminal before ordinary execution.
	After []Id `json:"after,omitempty"`
	// Background excludes this task from ordinary idle waits and conversation
	// aborts.
	Background bool `json:"background,omitempty"`
	// AbortRequested is the durable abort mark checked before run-mode
	// progress is committed.
	AbortRequested bool      `json:"abortRequested,omitempty"`
	State          TaskState `json:"state"`
	// Memos are small first-writer-wins values retained while the task is live.
	Memos map[string]json.RawMessage `json:"memos,omitempty"`
}

// Document scope kinds.
const (
	ScopeSession      = "session"
	ScopeConversation = "conversation"
	ScopeTask         = "task"
)

// DocumentRecord is the persisted lifecycle record for one create-to-retire
// document incarnation.
type DocumentRecord struct {
	// ID is the unique incarnation ID; never reused when the same logical
	// document is recreated.
	ID Id `json:"id"`
	// Kind is the registered document kind.
	Kind string `json:"kind"`
	// Key is the family member key (absent for singleton documents).
	Key *string `json:"key,omitempty"`
	// CreatedAt is the commit that created the incarnation.
	CreatedAt Seq `json:"createdAt"`
	// RetiredAt is the commit that retired the incarnation; absent while
	// current.
	RetiredAt *Seq `json:"retiredAt,omitempty"`
	// Scope is session, conversation, or task (see Scope* constants).
	Scope string `json:"scope"`
	// ConversationID applies to conversation-scoped documents.
	ConversationID *Id `json:"conversationId,omitempty"`
	// TaskID applies to task-scoped documents.
	TaskID *Id `json:"taskId,omitempty"`
	// History is "latest" or "rewindable" (conversation scope).
	History *string `json:"history,omitempty"`
	// Fork is "current" | "initial" | "asOf" (conversation scope).
	Fork *string `json:"fork,omitempty"`
}

// DocumentCreate is the fields supplied when storage stamps a new document.
type DocumentCreate struct {
	ID             Id      `json:"id"`
	Kind           string  `json:"kind"`
	Key            *string `json:"key,omitempty"`
	Scope          string  `json:"scope"`
	ConversationID *Id     `json:"conversationId,omitempty"`
	TaskID         *Id     `json:"taskId,omitempty"`
	History        *string `json:"history,omitempty"`
	Fork           *string `json:"fork,omitempty"`
}

// Cursor is backend-owned JSON continuation state that callers only round-trip
// to the same scan.
type Cursor = map[string]json.RawMessage

// CursorAfter builds a cursor positioned after an id.
func CursorAfter(id Id) Cursor {
	encoded, _ := json.Marshal(id)
	return Cursor{"after": encoded}
}

// Page is one ordered scan result and its optional continuation state.
type Page[T any] struct {
	Items []T
	Next  Cursor
}

// EntryQuery is the inclusive id bounds for a newest-first scan of one
// conversation's fork-aware history.
type EntryQuery struct {
	ConversationID Id
	// MinEntryID is the oldest entry id that may be returned.
	MinEntryID *Id
	// MaxEntryID is the newest entry id that may be returned.
	MaxEntryID *Id
}

// TaskQuery filters an ordered scan of durable task records.
type TaskQuery struct {
	ConversationID *Id
	Kind           *string
	Status         *string
	AbortRequested *bool
	Background     *bool
}

// StorageWrite is one table mutation in an atomic storage commit. Task and
// input writes replace whole records.
type StorageWrite struct {
	// Type is "conversation" | "entry" | "task" | "input".
	Type string

	Conversation *ConversationRecord
	Entry        *EntryRecord
	Task         *TaskRecord
	Input        *Input
}

// EntryCommit is one stored entry plus the commit that persisted it.
type EntryCommit struct {
	Entry     EntryRecord
	CommitSeq Seq
}

// Storage is the atomic persistence boundary for session records.
//
// Storage trusts the owning session to supply semantically valid records,
// references, ancestry, and transitions. Implementations enforce atomicity,
// global id ownership, immutable conversation/entry creation, and detached
// values; the session serializes commits.
type Storage interface {
	// Commit atomically persists one batch and returns the sequence assigned
	// to that commit.
	Commit(ctx context.Context, writes []StorageWrite) (Seq, error)

	// MintID returns a fresh candidate from the session-global record id
	// namespace.
	MintID(ctx context.Context) (Id, error)

	// Conversation looks up one conversation by exact id.
	Conversation(ctx context.Context, id Id) (*ConversationRecord, error)

	// ScanConversations scans conversations in ascending id order.
	ScanConversations(ctx context.Context, cursor Cursor, limit int) (Page[ConversationRecord], error)

	// Entry looks up one global entry and the commit that persisted it.
	Entry(ctx context.Context, id Id) (*EntryCommit, error)

	// FindLatestHeadMarker returns the newest visible entry with a head at or
	// below the optional inclusive cutoff.
	FindLatestHeadMarker(ctx context.Context, conversationID Id, atOrBeforeEntryID *Id) (*EntryRecord, error)

	// ScanEntries scans the inclusive visible range newest-first.
	ScanEntries(ctx context.Context, query EntryQuery, cursor Cursor, limit int) (Page[EntryRecord], error)

	// Task looks up the latest complete record for one task.
	Task(ctx context.Context, id Id) (*TaskRecord, error)

	// ScanTasks scans task records matching every supplied filter.
	ScanTasks(ctx context.Context, query TaskQuery, cursor Cursor, limit int) (Page[TaskRecord], error)

	// Input looks up the latest complete record for one admitted input.
	Input(ctx context.Context, id Id) (*Input, error)

	// InputByRequest finds an input by its conversation-scoped host
	// deduplication key.
	InputByRequest(ctx context.Context, conversationID Id, requestID string) (*Input, error)

	// Close releases backend resources; all later operations must reject.
	Close(ctx context.Context) error
}
