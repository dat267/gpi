package durable

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
)

// Port of src/memory-storage.ts: the detached in-memory reference Storage.
//
// Reads and retained writes are cloned intentionally to match the ownership
// boundary of serialization-backed stores. This is backend conformance, not
// validation.

type memoryState struct {
	conversations     map[Id]*ConversationRecord
	conversationIDs   []Id
	entries           map[Id]*EntryRecord
	entryIDs          map[Id][]Id
	headEntryIDs      map[Id][]Id
	entryCommitSeqs   map[Id]Seq
	tasks             map[Id]*TaskRecord
	taskIDs           []Id
	taskIDsByStatus   map[string][]Id
	inputs            map[Id]*Input
	inputIDsByRequest map[Id]map[string]Id
}

// MemoryStorage is the in-memory reference storage.
type MemoryStorage struct {
	mu      sync.Mutex
	state   memoryState
	nextID  Id
	nextSeq Seq
	closed  bool
}

// NewMemoryStorage builds an empty storage. Ids start at 2 (the root
// conversation reserves id 1).
func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{
		state: memoryState{
			conversations:     map[Id]*ConversationRecord{},
			entries:           map[Id]*EntryRecord{},
			entryIDs:          map[Id][]Id{},
			headEntryIDs:      map[Id][]Id{},
			entryCommitSeqs:   map[Id]Seq{},
			tasks:             map[Id]*TaskRecord{},
			taskIDsByStatus:   map[string][]Id{TaskPending: {}, TaskRunning: {}, TaskTerminal: {}},
			inputs:            map[Id]*Input{},
			inputIDsByRequest: map[Id]map[string]Id{},
		},
		nextID:  2,
		nextSeq: 1,
	}
}

// cloneValue deep-copies a JSON-shaped value (upstream clone).
func cloneValue[T any](value T) T {
	switch any(value).(type) {
	case nil, string, bool, int, int64, float64:
		return value
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return value
		}
		var out T
		if err := json.Unmarshal(encoded, &out); err != nil {
			return value
		}
		return out
	}
}

func cloneConversation(value *ConversationRecord) *ConversationRecord {
	if value == nil {
		return nil
	}
	return cloneValue(value)
}

func cloneEntry(value *EntryRecord) *EntryRecord {
	if value == nil {
		return nil
	}
	return cloneValue(value)
}

func cloneTask(value *TaskRecord) *TaskRecord {
	if value == nil {
		return nil
	}
	return cloneValue(value)
}

func cloneInput(value *Input) *Input {
	if value == nil {
		return nil
	}
	return cloneValue(value)
}

// cursorID extracts the after id from a cursor.
func cursorID(cursor Cursor) *Id {
	if cursor == nil {
		return nil
	}
	raw, ok := cursor["after"]
	if !ok {
		return nil
	}
	var id Id
	if err := json.Unmarshal(raw, &id); err != nil {
		return nil
	}
	return &id
}

// lowerBound is the first index whose value is >= target.
func lowerBound(ids []Id, target Id) int {
	low, high := 0, len(ids)
	for low < high {
		middle := int(uint(low+high) >> 1)
		if ids[middle] < target {
			low = middle + 1
		} else {
			high = middle
		}
	}
	return low
}

// upperBound is the first index whose value is > target.
func upperBound(ids []Id, target Id) int {
	low, high := 0, len(ids)
	for low < high {
		middle := int(uint(low+high) >> 1)
		if ids[middle] <= target {
			low = middle + 1
		} else {
			high = middle
		}
	}
	return low
}

func insertSorted(ids []Id, id Id) []Id {
	if len(ids) == 0 || ids[len(ids)-1] < id {
		return append(ids, id)
	}
	index := lowerBound(ids, id)
	ids = append(ids, 0)
	copy(ids[index+1:], ids[index:])
	ids[index] = id
	return ids
}

func removeSorted(ids []Id, id Id) []Id {
	index := lowerBound(ids, id)
	if index < len(ids) && ids[index] == id {
		return append(ids[:index], ids[index+1:]...)
	}
	return ids
}

func (s *MemoryStorage) tableContaining(id Id) string {
	if _, ok := s.state.conversations[id]; ok {
		return "conversation"
	}
	if _, ok := s.state.entries[id]; ok {
		return "entry"
	}
	if _, ok := s.state.tasks[id]; ok {
		return "task"
	}
	if _, ok := s.state.inputs[id]; ok {
		return "input"
	}
	return ""
}

// page paginates values, cloning the returned items.
func pageOf[T any](values []T, limit int, idOf func(T) Id) Page[T] {
	count := min(limit, len(values))
	items := make([]T, 0, count)
	for _, value := range values[:count] {
		items = append(items, cloneValue(value))
	}
	result := Page[T]{Items: items}
	if len(values) > limit {
		last := items[len(items)-1]
		result.Next = CursorAfter(idOf(last))
	}
	return result
}

// Commit atomically persists one batch.
func (s *MemoryStorage) Commit(ctx context.Context, writes []StorageWrite) (Seq, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return 0, err
	}
	prepared := make([]StorageWrite, 0, len(writes))
	for _, write := range writes {
		prepared = append(prepared, cloneWrite(write))
	}
	if err := s.checkImmutableIDs(prepared); err != nil {
		return 0, err
	}
	seq := s.nextSeq

	for _, write := range prepared {
		switch write.Type {
		case "conversation":
			value := write.Conversation
			s.state.conversations[value.ID] = value
			s.state.conversationIDs = insertSorted(s.state.conversationIDs, value.ID)
			s.bumpNextID(value.ID)
		case "entry":
			value := write.Entry
			s.state.entries[value.ID] = value
			s.state.entryCommitSeqs[value.ID] = seq
			s.state.entryIDs[value.ConversationID] = insertSorted(s.state.entryIDs[value.ConversationID], value.ID)
			if value.Head != nil {
				s.state.headEntryIDs[value.ConversationID] = insertSorted(s.state.headEntryIDs[value.ConversationID], value.ID)
			}
			s.bumpNextID(value.ID)
		case "task":
			value := write.Task
			previous, existed := s.state.tasks[value.ID]
			if !existed {
				s.state.taskIDs = insertSorted(s.state.taskIDs, value.ID)
				s.state.taskIDsByStatus[value.State.Status] = insertSorted(s.state.taskIDsByStatus[value.State.Status], value.ID)
			} else if previous.State.Status != value.State.Status {
				s.state.taskIDsByStatus[previous.State.Status] = removeSorted(s.state.taskIDsByStatus[previous.State.Status], value.ID)
				s.state.taskIDsByStatus[value.State.Status] = insertSorted(s.state.taskIDsByStatus[value.State.Status], value.ID)
			}
			s.state.tasks[value.ID] = value
			s.bumpNextID(value.ID)
		case "input":
			value := write.Input
			if previous, ok := s.state.inputs[value.ID]; ok && previous.RequestID != nil {
				if requests := s.state.inputIDsByRequest[previous.ConversationID]; requests != nil {
					if requests[*previous.RequestID] == value.ID {
						delete(requests, *previous.RequestID)
						if len(requests) == 0 {
							delete(s.state.inputIDsByRequest, previous.ConversationID)
						}
					}
				}
			}
			s.state.inputs[value.ID] = value
			if value.RequestID != nil {
				requests := s.state.inputIDsByRequest[value.ConversationID]
				if requests == nil {
					requests = map[string]Id{}
					s.state.inputIDsByRequest[value.ConversationID] = requests
				}
				requests[*value.RequestID] = value.ID
			}
			s.bumpNextID(value.ID)
		default:
			return 0, fmt.Errorf("unknown storage write type: %s", write.Type)
		}
	}
	s.nextSeq++
	return seq, nil
}

func cloneWrite(write StorageWrite) StorageWrite {
	switch write.Type {
	case "conversation":
		return StorageWrite{Type: write.Type, Conversation: cloneConversation(write.Conversation)}
	case "entry":
		return StorageWrite{Type: write.Type, Entry: cloneEntry(write.Entry)}
	case "task":
		return StorageWrite{Type: write.Type, Task: cloneTask(write.Task)}
	case "input":
		return StorageWrite{Type: write.Type, Input: cloneInput(write.Input)}
	default:
		return write
	}
}

func (s *MemoryStorage) bumpNextID(id Id) {
	if id+1 > s.nextID {
		s.nextID = id + 1
	}
}

// MintID returns the next id from the global namespace.
func (s *MemoryStorage) MintID(ctx context.Context) (Id, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return 0, err
	}
	if s.nextID == math.MaxInt64 {
		return 0, fmt.Errorf("ID space is exhausted")
	}
	id := s.nextID
	s.nextID++
	return id, nil
}

// Conversation looks up one conversation.
func (s *MemoryStorage) Conversation(ctx context.Context, id Id) (*ConversationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	return cloneConversation(s.state.conversations[id]), nil
}

// ScanConversations scans conversations in ascending id order.
func (s *MemoryStorage) ScanConversations(ctx context.Context, cursor Cursor, limit int) (Page[ConversationRecord], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return Page[ConversationRecord]{}, err
	}
	start := 0
	if after := cursorID(cursor); after != nil {
		start = upperBound(s.state.conversationIDs, *after)
	}
	end := min(start+limit+1, len(s.state.conversationIDs))
	values := make([]ConversationRecord, 0, end-start)
	for _, id := range s.state.conversationIDs[start:end] {
		values = append(values, *s.state.conversations[id])
	}
	return pageOf(values, limit, func(value ConversationRecord) Id { return value.ID }), nil
}

// Entry looks up one entry and its commit sequence.
func (s *MemoryStorage) Entry(ctx context.Context, id Id) (*EntryCommit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	entry, ok := s.state.entries[id]
	if !ok {
		return nil, nil
	}
	return &EntryCommit{Entry: *cloneEntry(entry), CommitSeq: s.state.entryCommitSeqs[id]}, nil
}

// FindLatestHeadMarker walks the fork chain for the newest head marker at or
// below the cutoff.
func (s *MemoryStorage) FindLatestHeadMarker(ctx context.Context, conversationID Id, atOrBeforeEntryID *Id) (*EntryRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	if _, ok := s.state.conversations[conversationID]; !ok {
		return nil, fmt.Errorf("Unknown conversation: %d", conversationID)
	}
	currentID := conversationID
	upperEntryID := Id(math.MaxInt64)
	if atOrBeforeEntryID != nil {
		upperEntryID = *atOrBeforeEntryID
	}
	for {
		ids := s.state.headEntryIDs[currentID]
		index := upperBound(ids, upperEntryID) - 1
		if index >= 0 {
			entry := s.state.entries[ids[index]]
			copied := cloneEntry(entry)
			return copied, nil
		}
		conversation := s.state.conversations[currentID]
		if conversation.Parent == nil {
			return nil, nil
		}
		if conversation.Parent.At < upperEntryID {
			upperEntryID = conversation.Parent.At
		}
		currentID = conversation.Parent.ConversationID
	}
}

// ScanEntries scans the inclusive visible range newest-first.
func (s *MemoryStorage) ScanEntries(ctx context.Context, query EntryQuery, cursor Cursor, limit int) (Page[EntryRecord], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return Page[EntryRecord]{}, err
	}
	maxEntryID := Id(math.MaxInt64)
	if query.MaxEntryID != nil {
		maxEntryID = *query.MaxEntryID
	}
	if after := cursorID(cursor); after != nil && *after-1 < maxEntryID {
		maxEntryID = *after - 1
	}
	minEntryID := Id(math.MinInt64)
	if query.MinEntryID != nil {
		minEntryID = *query.MinEntryID
	}

	var visible []EntryRecord
	for _, entry := range s.visibleEntries(query.ConversationID, minEntryID, maxEntryID) {
		visible = append(visible, entry)
		if len(visible) > limit {
			break
		}
	}
	return pageOf(visible, limit, func(value EntryRecord) Id { return value.ID }), nil
}

// Task looks up the latest record for one task.
func (s *MemoryStorage) Task(ctx context.Context, id Id) (*TaskRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	return cloneTask(s.state.tasks[id]), nil
}

// ScanTasks scans task records matching every supplied filter.
func (s *MemoryStorage) ScanTasks(ctx context.Context, query TaskQuery, cursor Cursor, limit int) (Page[TaskRecord], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return Page[TaskRecord]{}, err
	}
	ids := s.state.taskIDs
	if query.Status != nil {
		ids = s.state.taskIDsByStatus[*query.Status]
	}
	start := 0
	if after := cursorID(cursor); after != nil {
		start = upperBound(ids, *after)
	}
	var values []TaskRecord
	for index := start; index < len(ids) && len(values) <= limit; index++ {
		value := s.state.tasks[ids[index]]
		if query.ConversationID != nil && value.ConversationID != *query.ConversationID {
			continue
		}
		if query.Kind != nil && value.Kind != *query.Kind {
			continue
		}
		if query.AbortRequested != nil && value.AbortRequested != *query.AbortRequested {
			continue
		}
		if query.Background != nil && value.Background != *query.Background {
			continue
		}
		values = append(values, *value)
	}
	return pageOf(values, limit, func(value TaskRecord) Id { return value.ID }), nil
}

// Input looks up one input.
func (s *MemoryStorage) Input(ctx context.Context, id Id) (*Input, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	return cloneInput(s.state.inputs[id]), nil
}

// InputByRequest finds an input by its conversation-scoped request key.
func (s *MemoryStorage) InputByRequest(ctx context.Context, conversationID Id, requestID string) (*Input, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	requests := s.state.inputIDsByRequest[conversationID]
	if requests == nil {
		return nil, nil
	}
	id, ok := requests[requestID]
	if !ok {
		return nil, nil
	}
	return cloneInput(s.state.inputs[id]), nil
}

// Close releases the storage; later operations reject.
func (s *MemoryStorage) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// visibleEntries yields the fork-aware visible range newest-first.
func (s *MemoryStorage) visibleEntries(conversationID Id, minEntryID, maxEntryID Id) []EntryRecord {
	if _, ok := s.state.conversations[conversationID]; !ok {
		return nil
	}
	var out []EntryRecord
	currentID := conversationID
	upperEntryID := maxEntryID
	for {
		ids := s.state.entryIDs[currentID]
		for index := upperBound(ids, upperEntryID) - 1; index >= 0; index-- {
			id := ids[index]
			if id < minEntryID {
				break
			}
			out = append(out, *s.state.entries[id])
		}
		conversation := s.state.conversations[currentID]
		if conversation.Parent == nil {
			break
		}
		if conversation.Parent.At < upperEntryID {
			upperEntryID = conversation.Parent.At
		}
		if upperEntryID < minEntryID {
			break
		}
		currentID = conversation.Parent.ConversationID
	}
	return out
}

// checkImmutableIDs enforces global id ownership and immutable
// conversation/entry creation.
func (s *MemoryStorage) checkImmutableIDs(writes []StorageWrite) error {
	claimed := map[Id]string{}
	for _, write := range writes {
		table := write.Type
		id := writeID(write)
		existing := s.tableContaining(id)
		earlier, claimedBefore := claimed[id]
		if table == "conversation" || table == "entry" {
			if existing != "" {
				return fmt.Errorf("ID %d already belongs to %s", id, existing)
			}
			if claimedBefore {
				return fmt.Errorf("ID %d is written more than once", id)
			}
		} else {
			if existing != "" && existing != table {
				return fmt.Errorf("ID %d already belongs to %s", id, existing)
			}
			if claimedBefore && earlier != table {
				return fmt.Errorf("ID %d is written as two record types", id)
			}
		}
		claimed[id] = table
	}
	return nil
}

func writeID(write StorageWrite) Id {
	switch write.Type {
	case "conversation":
		return write.Conversation.ID
	case "entry":
		return write.Entry.ID
	case "task":
		return write.Task.ID
	case "input":
		return write.Input.ID
	default:
		return 0
	}
}

func (s *MemoryStorage) assertOpen() error {
	if s.closed {
		return fmt.Errorf("MemoryStorage is closed")
	}
	return nil
}
