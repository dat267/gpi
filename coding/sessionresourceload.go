package coding

// The CLI's resource switches have to survive a reload. Upstream re-resolves its
// resource loader with the same options, so /reload is a re-read, not a reset:
// without this, --no-skills would quietly come back the moment a user reloaded,
// and an explicit --skill path would disappear.

// resourceOptions are the CLI-derived resource switches.
type resourceOptions struct {
	// SkillPaths are the --skill paths. They load even when NoSkills is set:
	// refusing discovery is not refusing what was named.
	SkillPaths []string
	// NoSkills suppresses skill discovery and the settings' skill paths.
	NoSkills bool
	// NoContextFiles suppresses AGENTS.md/CLAUDE.md discovery.
	NoContextFiles bool
	// PromptTemplatePaths are the --prompt-template paths, which load even when
	// NoPromptTemplates is set, for the same reason as skills.
	PromptTemplatePaths []string
	// NoPromptTemplates suppresses prompt-template discovery and the settings'
	// prompt-template paths.
	NoPromptTemplates bool
}

// contextFiles returns the project context files.
func (o resourceOptions) contextFiles(cwd string, agentDir string) []ContextFile {
	if o.NoContextFiles {
		return nil
	}
	return LoadProjectContextFiles(cwd, agentDir)
}

// skills returns the skills, from the explicit paths and (unless discovery is
// off) the settings'.
func (o resourceOptions) skills(cwd string, agentDir string, settings *SettingsManager, trusted bool) LoadSkillsResult {
	skillPaths := append([]string{}, o.SkillPaths...)
	if !o.NoSkills && settings != nil {
		skillPaths = append(settings.GetSkillPaths(), skillPaths...)
	}
	return LoadSkills(LoadSkillsOptions{
		Cwd: cwd, AgentDir: agentDir, SkillPaths: skillPaths,
		IncludeDefaults: !o.NoSkills,
	}, trusted)
}

// promptTemplates returns the file-based prompt templates.
func (o resourceOptions) promptTemplates(cwd string, agentDir string, settings *SettingsManager) []PromptTemplate {
	promptPaths := append([]string{}, o.PromptTemplatePaths...)
	if !o.NoPromptTemplates && settings != nil {
		promptPaths = append(settings.GetPromptTemplatePaths(), promptPaths...)
	}
	return LoadPromptTemplates(LoadPromptTemplatesOptions{
		Cwd: cwd, AgentDir: agentDir, PromptPaths: promptPaths,
		IncludeDefaults: !o.NoPromptTemplates,
	})
}
