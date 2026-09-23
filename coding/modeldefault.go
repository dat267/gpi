package coding

import (
	"context"
)

// modeldefault — every session starts on the settings default model.
//
// This is a port of the user's modeldefault extension (divergence D151). pi
// scopes the model to the session: /model writes a model_change entry and
// resuming restores it, so the settings default only reaches sessions that
// never chose. The sync closes that gap from the other side — the settings
// default (what /model + Ctrl+S writes) is the target, read live, and every
// session start moves the session onto it. A manual switch therefore lasts for
// the current session only; there is no persisted per-session claim, no
// command, and no opt-out short of clearing the default from settings.
//
// Where the port differs from the extension: the extension polls (150 ms over a
// 4 s budget) because pi's provider-auth snapshot is populated by an async
// refresh that can still be in flight at session_start. This port's
// availability refresh runs inline (queueAvailabilityRefresh calls
// runAvailabilityRefresh directly), so the snapshot is settled by the time the
// session exists. A single attempt carries the same meaning, and the two
// failure outcomes — not in the catalog, or no configured auth — are reported
// exactly as the extension reports them.

// ModelDefaultRef is the default model named by settings.
type ModelDefaultRef struct {
	Provider string
	ID       string
}

// String renders the ref the way the extension's messages do.
func (r *ModelDefaultRef) String() string {
	return r.Provider + "/" + r.ID
}

// ReadModelDefaultRef returns the settings default model, or nil when settings
// name no usable default (either half missing or blank).
func ReadModelDefaultRef(settings *SettingsManager) *ModelDefaultRef {
	if settings == nil {
		return nil
	}
	provider := settings.GetDefaultProvider()
	modelID := settings.GetDefaultModel()
	if provider == nil || modelID == nil || *provider == "" || *modelID == "" {
		return nil
	}
	return &ModelDefaultRef{Provider: *provider, ID: *modelID}
}

// ModelDefaultSyncResult reports what a sync did. A zero result means there was
// nothing to do and nothing to say.
type ModelDefaultSyncResult struct {
	// Applied reports whether the session was moved onto the default.
	Applied bool
	// Message is the notification text (empty when silent).
	Message string
	// Warning marks Message as a warning rather than information.
	Warning bool
}

// SyncSessionModelToDefault moves the session onto the settings default model,
// reporting what happened.
//
// The full catalog model object is applied, never a bare reference: a ref
// without its limits reaches the footer as "?/0" (the regression the
// extension's suite guards).
func (s *AgentSession) SyncSessionModelToDefault(ctx context.Context, reason string) ModelDefaultSyncResult {
	ref := ReadModelDefaultRef(s.control.Settings)
	if ref == nil || s.control.ModelRuntime == nil {
		return ModelDefaultSyncResult{}
	}
	if current := s.Model(); current != nil && current.Provider == ref.Provider && current.ID == ref.ID {
		return ModelDefaultSyncResult{}
	}

	full := s.control.ModelRuntime.GetModel(ref.Provider, ref.ID)
	if full != nil {
		if err := s.SetModel(ctx, full, ModelMutationOptions{}); err == nil {
			return ModelDefaultSyncResult{
				Applied: true,
				Message: "[modeldefault] using default " + ref.String() + " (" + reason + ")",
			}
		}
	}

	return ModelDefaultSyncResult{
		Warning: true,
		Message: "[modeldefault] default " + ref.String() + " is not available yet (" +
			ref.unavailableReason(full != nil) + ") — staying on " + s.currentModelLabel(),
	}
}

// unavailableReason explains why the default could not be applied. From the
// session's seat an absent model and an unauthenticated one are different
// faults, so they are named differently.
func (r *ModelDefaultRef) unavailableReason(inCatalog bool) string {
	if inCatalog {
		return "no configured auth"
	}
	return "no configured auth, or not in the catalog"
}

// currentModelLabel names the model the session stays on, or says there is none.
func (s *AgentSession) currentModelLabel() string {
	current := s.Model()
	if current == nil || current.Provider == "unknown" || current.ID == "" || current.ID == "unknown" {
		return "no model yet"
	}
	return current.Provider + "/" + current.ID
}

// recordModelDefaultSync stores the last sync result so the interactive layer
// can report it at session start and after a reload.
func (s *AgentSession) recordModelDefaultSync(result ModelDefaultSyncResult) {
	s.control.stateMu.Lock()
	defer s.control.stateMu.Unlock()
	s.control.modelDefaultSync = result
}

// LastModelDefaultSync returns the most recent sync result (a zero result when
// no sync has run or it had nothing to report).
func (s *AgentSession) LastModelDefaultSync() ModelDefaultSyncResult {
	s.control.stateMu.Lock()
	defer s.control.stateMu.Unlock()
	return s.control.modelDefaultSync
}

// suspendModelDefaultSync records that this session carries an explicit model
// override (a --model/--provider choice), so the settings default must not
// reassert itself over it — including on reload.
func (s *AgentSession) suspendModelDefaultSync() {
	s.control.stateMu.Lock()
	defer s.control.stateMu.Unlock()
	s.control.modelDefaultSuspended = true
}

// modelDefaultSuspended reports whether an explicit override pinned the model.
func (s *AgentSession) modelDefaultSuspended() bool {
	s.control.stateMu.Lock()
	defer s.control.stateMu.Unlock()
	return s.control.modelDefaultSuspended
}
