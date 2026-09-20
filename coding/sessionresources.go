package coding

import (
	"fmt"
	"sync"
)

// Port of packages/ai/src/session-resources.ts: the cleanup registry for
// per-session resources (bash executor temp files, watchers, and so on).

// SessionResourceCleanup cleans up one session's resources.
type SessionResourceCleanup func(sessionID string)

var (
	sessionResourceMu   sync.Mutex
	sessionResourceSet  = map[*sessionResourceCleanupHandle]bool{}
	sessionResourceList []*sessionResourceCleanupHandle
)

type sessionResourceCleanupHandle struct {
	fn SessionResourceCleanup
}

// RegisterSessionResourceCleanup registers a cleanup and returns the
// unregister function. Cleanups run in registration order (upstream's Set
// preserves insertion order; Go maps do not).
func RegisterSessionResourceCleanup(cleanup SessionResourceCleanup) func() {
	handle := &sessionResourceCleanupHandle{fn: cleanup}
	sessionResourceMu.Lock()
	sessionResourceSet[handle] = true
	sessionResourceList = append(sessionResourceList, handle)
	sessionResourceMu.Unlock()
	return func() {
		sessionResourceMu.Lock()
		delete(sessionResourceSet, handle)
		for index, candidate := range sessionResourceList {
			if candidate == handle {
				sessionResourceList = append(sessionResourceList[:index], sessionResourceList[index+1:]...)
				break
			}
		}
		sessionResourceMu.Unlock()
	}
}

// CleanupSessionResources runs every registered cleanup (upstream
// cleanupSessionResources). Individual failures are collected and reported as
// one aggregated error.
func CleanupSessionResources(sessionID string) error {
	sessionResourceMu.Lock()
	cleanups := make([]SessionResourceCleanup, 0, len(sessionResourceList))
	for _, handle := range sessionResourceList {
		if sessionResourceSet[handle] {
			cleanups = append(cleanups, handle.fn)
		}
	}
	sessionResourceMu.Unlock()

	var errors []error
	for _, cleanup := range cleanups {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					errors = append(errors, toError(recovered))
				}
			}()
			cleanup(sessionID)
		}()
	}
	if len(errors) > 0 {
		message := "Failed to cleanup session resources"
		aggregated := fmt.Errorf("%s", message)
		for _, err := range errors {
			aggregated = fmt.Errorf("%w: %v", aggregated, err)
		}
		return aggregated
	}
	return nil
}
