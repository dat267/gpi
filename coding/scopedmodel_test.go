package coding

import (
	"context"
	"testing"

	"github.com/dat267/pier/ai"
)

// reasoningProvider is stubProvider with a model that can actually think, so a
// thinking level survives GetSupportedThinkingLevels instead of clamping to off.
func reasoningProvider(id string) *ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID: id, Name: id + " provider", BaseURL: "https://" + id + ".example.com",
		Models: []*ai.Model{{
			ID: id + "-model", Name: id + " model", API: ai.APIOpenAICompletions, Provider: id,
			BaseURL: "https://" + id + ".example.com", Input: []string{"text"},
			ContextWindow: 1000, MaxTokens: 100, Reasoning: true,
		}},
		Single: funcStreams{},
	})
}

func scopedModelFrom(t *testing.T, runtime *ModelRuntime, provider, modelID string) ScopedModel {
	t.Helper()
	model := runtime.GetModel(provider, modelID)
	if model == nil {
		t.Fatalf("the test runtime has no model %s/%s", provider, modelID)
	}
	return ScopedModel{Model: model}
}

// A model cycle scope also seeds the model (upstream buildSessionOptions): with
// no model named and a new session, the settings' default is used when the scope
// contains it, and the first scoped model otherwise. Without a scope, nothing
// here applies and the settings default resolves as it always did.
func TestScopedModelsSeedTheModel(t *testing.T) {
	tempAgentDir(t)
	runtime := runtimeWithProviders(t, stubProvider("alpha"), stubProvider("gamma"))

	newSettings := func() *SettingsManager {
		return NewSettingsManagerFromFiles(t.TempDir(), t.TempDir(), SettingsManagerCreateOptions{})
	}
	create := func(t *testing.T, settings *SettingsManager, options *CreateAgentSessionOptions) *AgentSession {
		t.Helper()
		options.Cwd = t.TempDir()
		options.ModelRuntime = runtime
		options.SettingsManager = settings
		created, err := CreateAgentSession(context.Background(), options)
		if err != nil {
			t.Fatal(err)
		}
		return created.Session
	}

	t.Run("the first scoped model wins when the default is out of scope", func(t *testing.T) {
		settings := newSettings()
		settings.SetDefaultModelAndProvider("alpha", "alpha-model")
		session := create(t, settings, &CreateAgentSessionOptions{
			ScopedModels: []ScopedModel{scopedModelFrom(t, runtime, "gamma", "gamma-model")},
		})
		if model := session.Model(); model == nil || model.ID != "gamma-model" {
			t.Errorf("model = %v, want gamma-model", model)
		}
	})

	t.Run("an in-scope default wins over the first entry", func(t *testing.T) {
		settings := newSettings()
		settings.SetDefaultModelAndProvider("alpha", "alpha-model")
		session := create(t, settings, &CreateAgentSessionOptions{
			ScopedModels: []ScopedModel{
				scopedModelFrom(t, runtime, "gamma", "gamma-model"),
				scopedModelFrom(t, runtime, "alpha", "alpha-model"),
			},
		})
		if model := session.Model(); model == nil || model.ID != "alpha-model" {
			t.Errorf("model = %v, want the in-scope default alpha-model", model)
		}
	})

	t.Run("an existing session is not reseeded by the scope", func(t *testing.T) {
		settings := newSettings()
		settings.SetDefaultModelAndProvider("alpha", "alpha-model")
		sessions := NewSessionManager(t.TempDir(), &SessionManagerOptions{})
		sessions.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "hello"}})
		session := create(t, settings, &CreateAgentSessionOptions{
			ScopedModels:   []ScopedModel{scopedModelFrom(t, runtime, "gamma", "gamma-model")},
			SessionManager: sessions,
		})
		if model := session.Model(); model != nil && model.ID == "gamma-model" {
			t.Error("the scope reseeded a session that already had messages")
		}
	})

	t.Run("the scope's thinking level applies unless one is pinned", func(t *testing.T) {
		thinkingRuntime := runtimeWithProviders(t, reasoningProvider("delta"))
		settings := newSettings()
		scoped := scopedModelFrom(t, thinkingRuntime, "delta", "delta-model")
		scoped.ThinkingLevel, scoped.HasThinking = ai.ThinkHigh, true

		createWith := func(t *testing.T, options *CreateAgentSessionOptions) *AgentSession {
			t.Helper()
			options.Cwd = t.TempDir()
			options.ModelRuntime = thinkingRuntime
			options.SettingsManager = settings
			created, err := CreateAgentSession(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			return created.Session
		}

		session := createWith(t, &CreateAgentSessionOptions{ScopedModels: []ScopedModel{scoped}})
		if got := session.ThinkingLevel(); got != ai.ThinkHigh {
			t.Errorf("thinking = %q, want the scope's high", got)
		}

		session = createWith(t, &CreateAgentSessionOptions{
			ScopedModels: []ScopedModel{scoped}, ThinkingLevel: ai.ThinkLow,
		})
		if got := session.ThinkingLevel(); got != ai.ThinkLow {
			t.Errorf("thinking = %q, want the pinned low", got)
		}
	})

	t.Run("a scope is what triggers the seeding", func(t *testing.T) {
		settings := newSettings()
		session := create(t, settings, &CreateAgentSessionOptions{})
		if model := session.Model(); model != nil && model.ID == "gamma-model" {
			t.Error("a scoped model was seeded without any scope")
		}
	})
}
