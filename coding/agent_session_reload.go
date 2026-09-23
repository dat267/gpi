package coding

import (
	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
)

// Reload re-reads the settings and the ported resource files the session was
// built from, then rebuilds the system prompt (port of AgentSession.reload
// without the extension runner, the package manager, and their resource pass —
// D41/D140).
//
// The tool registry is unchanged: the built-in tools capture no
// settings-dependent state (the bash tool reads the shell settings per call),
// and without extension tools the active selection cannot change. Prompt
// templates and theme files are not loaded by the port at boot, so they are not
// re-read here either.
func (s *AgentSession) Reload() {
	if s.control != nil && s.control.Settings != nil {
		s.control.Settings.Reload()
	}
	s.SyncQueueModesFromSettings()
	ai.ResetAPIProviders()
	s.reloadResources()
	s.RebuildSystemPrompt(s.ActiveToolNames())
}

// reloadResources re-reads the context files, skills, and system/append prompt
// files, and refreshes the system-prompt options (upstream
// ResourceLoader.reload's non-extension half).
func (s *AgentSession) reloadResources() {
	trusted := false
	skillPaths := []string(nil)
	if s.control != nil && s.control.Settings != nil {
		trusted = s.control.Settings.IsProjectTrusted()
		skillPaths = s.control.Settings.GetSkillPaths()
	}
	contextFiles := LoadProjectContextFiles(s.Cwd, s.agentDir)
	skills := LoadSkills(LoadSkillsOptions{
		Cwd: s.Cwd, AgentDir: s.agentDir, SkillPaths: skillPaths, IncludeDefaults: true,
	}, trusted)
	overrides := LoadPromptOverrides(PromptFileSources{
		Cwd: s.Cwd, AgentDir: s.agentDir, ProjectTrusted: trusted,
		SystemPrompt: s.promptSources.SystemPrompt, AppendSystemPrompt: s.promptSources.AppendSystemPrompt,
	})

	if s.SystemPromptOptions != nil {
		options := *s.SystemPromptOptions
		options.CustomPrompt = overrides.SystemPrompt
		options.AppendSystemPrompt = overrides.AppendSystemPrompt
		options.PromptSourcePaths = append([]string{}, overrides.SourcePaths...)
		options.Skills = skills.Skills
		options.ContextFiles = contextFiles
		s.SystemPromptOptions = &options
	}
	s.skillDiagnostics = skills.Diagnostics
}

// ActiveToolNames returns the active tools' names in order (upstream
// AgentSession.getActiveToolNames).
func (s *AgentSession) ActiveToolNames() []string {
	tools := s.Agent.State().Tools
	return agentToolNames(tools)
}

func agentToolNames(tools []agent.AgentTool) []string {
	names := make([]string, 0, len(tools))
	for i := range tools {
		names = append(names, tools[i].Name)
	}
	return names
}
