package coding

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/dat267/pier/ai"
)

// Port of core/settings-manager.ts (with the config constants it uses from
// config.ts and the HTTP timeout helpers from core/http-dispatcher.ts).

// CONFIG_DIR_NAME is the project config directory name.
const ConfigDirName = ".pi"

// DefaultHTTPIdleTimeoutMS matches DEFAULT_HTTP_IDLE_TIMEOUT_MS.
const DefaultHTTPIdleTimeoutMS = 300_000

// DefaultCompactionReserveTokens and DefaultCompactionKeepRecentTokens are the
// built-in compaction token defaults.
const (
	DefaultCompactionReserveTokens    = 16384
	DefaultCompactionKeepRecentTokens = 20000
)

// GetAgentDir resolves the agent directory (upstream getAgentDir): the
// PI_CODING_AGENT_DIR override, else ~/.pi/agent.
func GetAgentDir() string {
	if envDir := os.Getenv("PI_CODING_AGENT_DIR"); envDir != "" {
		return NormalizePath(envDir, PathInputOptions{})
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(ConfigDirName, "agent")
	}
	return filepath.Join(home, ConfigDirName, "agent")
}

// ParseHTTPIdleTimeoutMS parses a timeout setting (upstream
// parseHttpIdleTimeoutMs): "disabled" -> 0, numeric strings and numbers floor,
// invalid -> absent.
func ParseHTTPIdleTimeoutMS(value any) (int64, bool) {
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if strings.EqualFold(trimmed, "disabled") {
			return 0, true
		}
		if trimmed == "" {
			return 0, false
		}
		parsed, err := parseJSONNumber(trimmed)
		if err != nil {
			return 0, false
		}
		return ParseHTTPIdleTimeoutMS(parsed)
	case json.Number:
		number, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return ParseHTTPIdleTimeoutMS(number)
	case float64:
		if typed < 0 {
			return 0, false
		}
		return int64(typed), true
	case int:
		if typed < 0 {
			return 0, false
		}
		return int64(typed), true
	case int64:
		if typed < 0 {
			return 0, false
		}
		return typed, true
	default:
		return 0, false
	}
}

func parseJSONNumber(input string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

// SettingsCompactionOverride are per-model compaction token overrides.
type SettingsCompactionOverride struct {
	ReserveTokens    *int64 `json:"reserveTokens,omitempty"`
	KeepRecentTokens *int64 `json:"keepRecentTokens,omitempty"`
}

// SettingsCompaction are the compaction settings.
type SettingsCompaction struct {
	Enabled          *bool                                 `json:"enabled,omitempty"`
	ReserveTokens    *int64                                `json:"reserveTokens,omitempty"`
	KeepRecentTokens *int64                                `json:"keepRecentTokens,omitempty"`
	ModelOverrides   map[string]SettingsCompactionOverride `json:"modelOverrides,omitempty"`
}

// SettingsBranchSummary are the branch-summary settings.
type SettingsBranchSummary struct {
	ReserveTokens *int64 `json:"reserveTokens,omitempty"`
	SkipPrompt    *bool  `json:"skipPrompt,omitempty"`
}

// SettingsProviderRetry are the provider-level retry settings.
type SettingsProviderRetry struct {
	TimeoutMS       *int64 `json:"timeoutMs,omitempty"`
	MaxRetries      *int   `json:"maxRetries,omitempty"`
	MaxRetryDelayMS *int64 `json:"maxRetryDelayMs,omitempty"`
}

// SettingsRetry are the agent retry settings.
type SettingsRetry struct {
	Enabled         *bool                  `json:"enabled,omitempty"`
	MaxRetries      *int                   `json:"maxRetries,omitempty"`
	BaseDelayMS     *int64                 `json:"baseDelayMs,omitempty"`
	MaxAgentDelayMS *int64                 `json:"maxAgentDelayMs,omitempty"`
	Provider        *SettingsProviderRetry `json:"provider,omitempty"`
}

// SettingsTerminal are the terminal/TUI settings.
type SettingsTerminal struct {
	ShowImages           *bool `json:"showImages,omitempty"`
	ImageWidthCells      *int  `json:"imageWidthCells,omitempty"`
	ClearOnShrink        *bool `json:"clearOnShrink,omitempty"`
	ShowTerminalProgress *bool `json:"showTerminalProgress,omitempty"`
	// Hyperlinks is a bool or the string "auto".
	Hyperlinks any `json:"hyperlinks,omitempty"`
	// Images is "kitty" | "iterm2" | "auto" | false.
	Images any `json:"images,omitempty"`
	// TrueColor is a bool or the string "auto".
	TrueColor any `json:"trueColor,omitempty"`
}

// SettingsImages are the image settings.
type SettingsImages struct {
	AutoResize  *bool `json:"autoResize,omitempty"`
	BlockImages *bool `json:"blockImages,omitempty"`
}

// SettingsThinkingBudgets are per-level thinking token budgets.
type SettingsThinkingBudgets struct {
	Minimal *int64 `json:"minimal,omitempty"`
	Low     *int64 `json:"low,omitempty"`
	Medium  *int64 `json:"medium,omitempty"`
	High    *int64 `json:"high,omitempty"`
}

// SettingsMarkdown are the markdown rendering settings.
type SettingsMarkdown struct {
	CodeBlockIndent *string `json:"codeBlockIndent,omitempty"`
	Mermaid         *string `json:"mermaid,omitempty"`
}

// SettingsWarnings are the warning toggles.
type SettingsWarnings struct {
	AnthropicExtraUsage *bool `json:"anthropicExtraUsage,omitempty"`
}

// SettingsPackageSource is the object form of a package source.
type SettingsPackageSource struct {
	Source     string   `json:"source"`
	Autoload   *bool    `json:"autoload,omitempty"`
	Extensions []string `json:"extensions,omitempty"`
	Skills     []string `json:"skills,omitempty"`
	Prompts    []string `json:"prompts,omitempty"`
	Themes     []string `json:"themes,omitempty"`
}

// Settings is the settings document (upstream Settings). Optional fields are
// pointers so absent settings stay absent in the persisted JSON.
type Settings struct {
	LastChangelogVersion *string `json:"lastChangelogVersion,omitempty"`
	// CacheWarming is the prompt-cache warming mode; global only because each
	// refresh costs money (default "streaming").
	CacheWarming         *string `json:"cacheWarming,omitempty"`
	DefaultProvider      *string `json:"defaultProvider,omitempty"`
	DefaultModel         *string `json:"defaultModel,omitempty"`
	DefaultThinkingLevel *string `json:"defaultThinkingLevel,omitempty"`
	// ModelThinkingLevels maps "provider/modelId" to a level.
	ModelThinkingLevels    map[string]string      `json:"modelThinkingLevels,omitempty"`
	Transport              *string                `json:"transport,omitempty"`
	SteeringMode           *string                `json:"steeringMode,omitempty"`
	FollowUpMode           *string                `json:"followUpMode,omitempty"`
	Theme                  *string                `json:"theme,omitempty"`
	Compaction             *SettingsCompaction    `json:"compaction,omitempty"`
	BranchSummary          *SettingsBranchSummary `json:"branchSummary,omitempty"`
	Retry                  *SettingsRetry         `json:"retry,omitempty"`
	HideThinkingBlock      *bool                  `json:"hideThinkingBlock,omitempty"`
	ShowCacheMissNotices   *bool                  `json:"showCacheMissNotices,omitempty"`
	ExternalEditor         *string                `json:"externalEditor,omitempty"`
	ShellPath              *string                `json:"shellPath,omitempty"`
	QuietStartup           *bool                  `json:"quietStartup,omitempty"`
	DefaultProjectTrust    *string                `json:"defaultProjectTrust,omitempty"`
	ShellCommandPrefix     *string                `json:"shellCommandPrefix,omitempty"`
	NpmCommand             []string               `json:"npmCommand,omitempty"`
	CollapseChangelog      *bool                  `json:"collapseChangelog,omitempty"`
	EnableInstallTelemetry *bool                  `json:"enableInstallTelemetry,omitempty"`
	EnableAnalytics        *bool                  `json:"enableAnalytics,omitempty"`
	TrackingID             *string                `json:"trackingId,omitempty"`
	// Packages is an array of strings or package source filter objects.
	Packages                  []any                    `json:"packages,omitempty"`
	Extensions                []string                 `json:"extensions,omitempty"`
	Skills                    []string                 `json:"skills,omitempty"`
	Prompts                   []string                 `json:"prompts,omitempty"`
	Themes                    []string                 `json:"themes,omitempty"`
	EnableSkillCommands       *bool                    `json:"enableSkillCommands,omitempty"`
	Terminal                  *SettingsTerminal        `json:"terminal,omitempty"`
	Images                    *SettingsImages          `json:"images,omitempty"`
	EnabledModels             []string                 `json:"enabledModels,omitempty"`
	DefaultTools              []string                 `json:"defaultTools,omitempty"`
	DoubleEscapeAction        *string                  `json:"doubleEscapeAction,omitempty"`
	TreeFilterMode            *string                  `json:"treeFilterMode,omitempty"`
	ThinkingBudgets           *SettingsThinkingBudgets `json:"thinkingBudgets,omitempty"`
	EditorPaddingX            *int                     `json:"editorPaddingX,omitempty"`
	OutputPad                 *int                     `json:"outputPad,omitempty"`
	AutocompleteMaxVisible    *int                     `json:"autocompleteMaxVisible,omitempty"`
	ShowHardwareCursor        *bool                    `json:"showHardwareCursor,omitempty"`
	Markdown                  *SettingsMarkdown        `json:"markdown,omitempty"`
	Warnings                  *SettingsWarnings        `json:"warnings,omitempty"`
	SessionDir                *string                  `json:"sessionDir,omitempty"`
	HTTPProxy                 *string                  `json:"httpProxy,omitempty"`
	HTTPIdleTimeoutMS         any                      `json:"httpIdleTimeoutMs,omitempty"`
	WebsocketConnectTimeoutMS any                      `json:"websocketConnectTimeoutMs,omitempty"`
	TuiMode                   *string                  `json:"tuiMode,omitempty"`
	FullscreenExitOutput      *string                  `json:"fullscreenExitOutput,omitempty"`
	FullscreenScrollbar       *string                  `json:"fullscreenScrollbar,omitempty"`
	FullscreenCopyOnSelect    *bool                    `json:"fullscreenCopyOnSelect,omitempty"`
}

// SettingsScope names one settings scope.
type SettingsScope = string

const (
	SettingsScopeGlobal  SettingsScope = "global"
	SettingsScopeProject SettingsScope = "project"
)

// SettingsStorage is the locked settings storage contract.
type SettingsStorage interface {
	// WithLock runs fn under the scope's lock; the returned string (when
	// non-nil) replaces the stored content.
	WithLock(scope SettingsScope, fn func(current *string) *string) error
}

// SettingsError is one recorded settings failure.
type SettingsError struct {
	Scope SettingsScope
	Path  string
	Error error
}

// FileSettingsStorage persists global and project settings files.
type FileSettingsStorage struct {
	GlobalSettingsPath  string
	ProjectSettingsPath string
}

// NewFileSettingsStorage builds file storage for a cwd and agent dir.
func NewFileSettingsStorage(cwd, agentDir string) *FileSettingsStorage {
	resolvedCwd := NormalizePath(cwd, PathInputOptions{})
	resolvedAgentDir := NormalizePath(agentDir, PathInputOptions{})
	return &FileSettingsStorage{
		GlobalSettingsPath:  filepath.Join(resolvedAgentDir, "settings.json"),
		ProjectSettingsPath: filepath.Join(resolvedCwd, ConfigDirName, "settings.json"),
	}
}

// WithLock reads, optionally writes, and unlocks one settings file.
func (s *FileSettingsStorage) WithLock(scope SettingsScope, fn func(current *string) *string) error {
	path := s.GlobalSettingsPath
	if scope == SettingsScopeProject {
		path = s.ProjectSettingsPath
	}
	dir := filepath.Dir(path)

	var release func()
	var err error
	exists := fileExists(path)
	if exists {
		release, err = acquireLockWithRetry(path)
		if err != nil {
			return err
		}
		defer release()
	}
	var current *string
	if exists {
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(content)
		current = &text
	}
	next := fn(current)
	if next == nil {
		return nil
	}
	if !fileExists(dir) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if release == nil {
		release, err = acquireLockWithRetry(path)
		if err != nil {
			return err
		}
		defer release()
	}
	return os.WriteFile(path, []byte(*next), 0o644)
}

// acquireLockWithRetry mirrors proper-lockfile's lockSync plus upstream's
// 10 attempts with 20ms delays.
func acquireLockWithRetry(path string) (func(), error) {
	const maxAttempts = 10
	const delay = 20 * time.Millisecond
	lockPath := path + ".lock"
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_ = file.Close()
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !errors.Is(err, os.ErrExist) || attempt == maxAttempts {
			return nil, err
		}
		lastErr = err
		time.Sleep(delay)
	}
	return nil, lastErr
}

// InMemorySettingsStorage is the process-local storage backend.
type InMemorySettingsStorage struct {
	mu      sync.Mutex
	global  *string
	project *string
}

// WithLock runs fn against the in-memory content.
func (s *InMemorySettingsStorage) WithLock(scope SettingsScope, fn func(current *string) *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.global
	if scope == SettingsScopeProject {
		current = s.project
	}
	next := fn(current)
	if next == nil {
		return nil
	}
	if scope == SettingsScopeProject {
		s.project = next
	} else {
		s.global = next
	}
	return nil
}

// SettingsManagerCreateOptions configure a manager.
type SettingsManagerCreateOptions struct {
	// ProjectTrusted defaults to true.
	ProjectTrusted *bool
}

// SettingsManager is the scoped settings facade (upstream SettingsManager).
//
// D23: upstream serializes writes through a promise queue because its storage
// is asynchronous; Go storage is synchronous, so writes run inline under the
// manager lock and failures are recorded (never thrown from save), matching the
// observable behavior (errors surface through DrainErrors).
type SettingsManager struct {
	mu sync.Mutex

	storage         SettingsStorage
	globalSettings  *Settings
	projectSettings *Settings
	settings        *Settings
	projectTrusted  bool

	modifiedFields        map[string]bool
	modifiedNestedFields  map[string]map[string]bool
	modifiedProjectFields map[string]bool
	modifiedProjectNested map[string]map[string]bool

	globalSettingsLoadError  error
	projectSettingsLoadError error
	errors                   []SettingsError
	settingsPaths            map[SettingsScope]string
}

// NewSettingsManagerFromFiles loads settings from the standard paths.
func NewSettingsManagerFromFiles(cwd, agentDir string, options SettingsManagerCreateOptions) *SettingsManager {
	if agentDir == "" {
		agentDir = GetAgentDir()
	}
	resolvedCwd := NormalizePath(cwd, PathInputOptions{})
	resolvedAgentDir := NormalizePath(agentDir, PathInputOptions{})
	storage := NewFileSettingsStorage(resolvedCwd, resolvedAgentDir)
	return newSettingsManagerFromStorage(storage, options, map[SettingsScope]string{
		SettingsScopeGlobal:  filepath.Join(resolvedAgentDir, "settings.json"),
		SettingsScopeProject: filepath.Join(resolvedCwd, ConfigDirName, "settings.json"),
	})
}

// NewSettingsManagerFromStorage builds a manager over an arbitrary storage.
func NewSettingsManagerFromStorage(storage SettingsStorage, options SettingsManagerCreateOptions) *SettingsManager {
	return newSettingsManagerFromStorage(storage, options, nil)
}

func newSettingsManagerFromStorage(storage SettingsStorage, options SettingsManagerCreateOptions, paths map[SettingsScope]string) *SettingsManager {
	projectTrusted := true
	if options.ProjectTrusted != nil {
		projectTrusted = *options.ProjectTrusted
	}
	globalLoad, globalErr := tryLoadFromStorage(storage, SettingsScopeGlobal, true)
	projectLoad, projectErr := tryLoadFromStorage(storage, SettingsScopeProject, projectTrusted)

	manager := &SettingsManager{
		storage:                  storage,
		globalSettings:           globalLoad,
		projectSettings:          projectLoad,
		projectTrusted:           projectTrusted,
		modifiedFields:           map[string]bool{},
		modifiedNestedFields:     map[string]map[string]bool{},
		modifiedProjectFields:    map[string]bool{},
		modifiedProjectNested:    map[string]map[string]bool{},
		globalSettingsLoadError:  globalErr,
		projectSettingsLoadError: projectErr,
		settingsPaths:            paths,
	}
	manager.settings = DeepMergeSettings(manager.globalSettings, manager.projectSettings)
	if globalErr != nil {
		manager.recordError(SettingsScopeGlobal, globalErr)
	}
	if projectErr != nil {
		manager.recordError(SettingsScopeProject, projectErr)
	}
	return manager
}

// NewInMemorySettingsManager builds a manager with in-memory settings.
func NewInMemorySettingsManager(settings *Settings, options SettingsManagerCreateOptions) *SettingsManager {
	storage := &InMemorySettingsStorage{}
	initial := MigrateSettings(settings)
	content := marshalSettingsIndent(initial)
	_ = storage.WithLock(SettingsScopeGlobal, func(*string) *string { return &content })
	return newSettingsManagerFromStorage(storage, options, nil)
}

func loadFromStorage(storage SettingsStorage, scope SettingsScope, projectTrusted bool) (*Settings, error) {
	if scope == SettingsScopeProject && !projectTrusted {
		return &Settings{}, nil
	}
	var content *string
	if err := storage.WithLock(scope, func(current *string) *string {
		content = current
		return nil
	}); err != nil {
		return nil, err
	}
	if content == nil || *content == "" {
		return &Settings{}, nil
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(StripBom(*content)), &raw); err != nil {
		return nil, err
	}
	return MigrateSettingsRaw(raw), nil
}

func tryLoadFromStorage(storage SettingsStorage, scope SettingsScope, projectTrusted bool) (*Settings, error) {
	settings, err := loadFromStorage(storage, scope, projectTrusted)
	if err != nil {
		return &Settings{}, err
	}
	return settings, nil
}

// StripBom removes a leading UTF-8 BOM.
func StripBom(content string) string {
	_, text := SplitBom(content)
	return text
}

// MigrateSettings applies the legacy-format migrations to a settings value.
//
// D24: upstream mutates a JS object; the Go port round-trips through a JSON map
// so unknown and future keys survive the migration untouched.
func MigrateSettings(settings *Settings) *Settings {
	if settings == nil {
		return &Settings{}
	}
	raw := settingsToMap(settings)
	return MigrateSettingsRaw(raw)
}

// MigrateSettingsRaw applies the migrations to a raw settings object.
func MigrateSettingsRaw(settings map[string]any) *Settings {
	if settings == nil {
		settings = map[string]any{}
	}
	// queueMode -> steeringMode
	if _, ok := settings["queueMode"]; ok {
		if _, hasSteering := settings["steeringMode"]; !hasSteering {
			settings["steeringMode"] = settings["queueMode"]
			delete(settings, "queueMode")
		}
	}
	// legacy websockets boolean -> transport enum
	if _, hasTransport := settings["transport"]; !hasTransport {
		if websockets, ok := settings["websockets"].(bool); ok {
			if websockets {
				settings["transport"] = "websocket"
			} else {
				settings["transport"] = "sse"
			}
			delete(settings, "websockets")
		}
	}
	// legacy skills object -> array
	if rawSkills, ok := settings["skills"]; ok {
		if skillsSettings, isObject := rawSkills.(map[string]any); isObject {
			if enable, hasEnable := skillsSettings["enableSkillCommands"]; hasEnable {
				if _, hasTopLevel := settings["enableSkillCommands"]; !hasTopLevel {
					settings["enableSkillCommands"] = enable
				}
			}
			if directories, isArray := skillsSettings["customDirectories"].([]any); isArray && len(directories) > 0 {
				settings["skills"] = directories
			} else {
				delete(settings, "skills")
			}
		}
	}
	// retry.maxDelayMs -> retry.provider.maxRetryDelayMs
	if rawRetry, ok := settings["retry"].(map[string]any); ok {
		provider, _ := rawRetry["provider"].(map[string]any)
		if maxDelay, isNumber := rawRetry["maxDelayMs"].(float64); isNumber {
			var hasProviderMax bool
			if provider != nil {
				if value, has := provider["maxRetryDelayMs"]; has && value != nil {
					hasProviderMax = true
				}
			}
			if !hasProviderMax {
				merged := map[string]any{}
				for key, value := range provider {
					merged[key] = value
				}
				merged["maxRetryDelayMs"] = maxDelay
				rawRetry["provider"] = merged
			}
		}
		delete(rawRetry, "maxDelayMs")
	}

	encoded, err := marshalJSONNoEscape(settings)
	if err != nil {
		return &Settings{}
	}
	var out Settings
	if err := json.Unmarshal(encoded, &out); err != nil {
		return &Settings{}
	}
	return &out
}

func settingsToMap(settings *Settings) map[string]any {
	encoded, err := settings.marshalOrdered()
	if err != nil {
		return map[string]any{}
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return map[string]any{}
	}
	return raw
}

func mapToSettings(raw map[string]any) *Settings {
	encoded, err := marshalJSONNoEscape(raw)
	if err != nil {
		return &Settings{}
	}
	var out Settings
	if err := json.Unmarshal(encoded, &out); err != nil {
		return &Settings{}
	}
	return &out
}

// DeepMergeSettings deep-merges overrides onto base; nested objects merge
// recursively and arrays/primitives replace (upstream deepMergeSettings).
func DeepMergeSettings(base, overrides *Settings) *Settings {
	if base == nil {
		base = &Settings{}
	}
	if overrides == nil {
		return cloneSettings(base)
	}
	merged := deepMergeObjects(settingsToMap(base), settingsToMap(overrides))
	return mapToSettings(merged)
}

func deepMergeObjects(base, overrides map[string]any) map[string]any {
	result := make(map[string]any, len(base)+len(overrides))
	for key, value := range base {
		result[key] = value
	}
	for key, overrideValue := range overrides {
		if overrideValue == nil {
			continue
		}
		baseValue, has := result[key]
		baseObject, baseIsObject := baseValue.(map[string]any)
		overrideObject, overrideIsObject := overrideValue.(map[string]any)
		if has && baseIsObject && overrideIsObject {
			result[key] = deepMergeObjects(baseObject, overrideObject)
			continue
		}
		result[key] = overrideValue
	}
	return result
}

func cloneSettings(settings *Settings) *Settings {
	return mapToSettings(settingsToMap(settings))
}

// marshalOrdered renders settings as a JSON object in declaration order,
// omitting unset optional fields but keeping explicitly false/0/"" values and
// empty arrays (upstream distinguishes undefined from falsy settings).
//
// D25: upstream preserves the existing file's key order for unchanged keys
// because it merges into the parsed file object; the Go port writes the
// declaration order instead. Values are identical.
func (s *Settings) marshalOrdered() ([]byte, error) {
	if s == nil {
		return []byte("{}"), nil
	}
	object := orderedObject{}
	object.setString("lastChangelogVersion", s.LastChangelogVersion)
	object.setString("defaultProvider", s.DefaultProvider)
	object.setString("defaultModel", s.DefaultModel)
	object.setString("defaultThinkingLevel", s.DefaultThinkingLevel)
	object.setMap("modelThinkingLevels", s.ModelThinkingLevels)
	object.setString("transport", s.Transport)
	object.setString("steeringMode", s.SteeringMode)
	object.setString("followUpMode", s.FollowUpMode)
	object.setString("theme", s.Theme)
	object.setAny("compaction", s.Compaction)
	object.setAny("branchSummary", s.BranchSummary)
	object.setAny("retry", s.Retry)
	object.setBool("hideThinkingBlock", s.HideThinkingBlock)
	object.setBool("showCacheMissNotices", s.ShowCacheMissNotices)
	object.setString("externalEditor", s.ExternalEditor)
	object.setString("shellPath", s.ShellPath)
	object.setBool("quietStartup", s.QuietStartup)
	object.setString("defaultProjectTrust", s.DefaultProjectTrust)
	object.setString("shellCommandPrefix", s.ShellCommandPrefix)
	object.setStrings("npmCommand", s.NpmCommand)
	object.setBool("collapseChangelog", s.CollapseChangelog)
	object.setBool("enableInstallTelemetry", s.EnableInstallTelemetry)
	object.setBool("enableAnalytics", s.EnableAnalytics)
	object.setString("trackingId", s.TrackingID)
	object.setAny("packages", s.Packages)
	object.setStrings("extensions", s.Extensions)
	object.setStrings("skills", s.Skills)
	object.setStrings("prompts", s.Prompts)
	object.setStrings("themes", s.Themes)
	object.setBool("enableSkillCommands", s.EnableSkillCommands)
	object.setAny("terminal", s.Terminal)
	object.setAny("images", s.Images)
	object.setStrings("enabledModels", s.EnabledModels)
	object.setStrings("defaultTools", s.DefaultTools)
	object.setString("doubleEscapeAction", s.DoubleEscapeAction)
	object.setString("treeFilterMode", s.TreeFilterMode)
	object.setAny("thinkingBudgets", s.ThinkingBudgets)
	object.setInt("editorPaddingX", s.EditorPaddingX)
	object.setInt("outputPad", s.OutputPad)
	object.setInt("autocompleteMaxVisible", s.AutocompleteMaxVisible)
	object.setBool("showHardwareCursor", s.ShowHardwareCursor)
	object.setAny("markdown", s.Markdown)
	object.setAny("warnings", s.Warnings)
	object.setString("sessionDir", s.SessionDir)
	object.setString("httpProxy", s.HTTPProxy)
	object.setAny("httpIdleTimeoutMs", s.HTTPIdleTimeoutMS)
	object.setAny("websocketConnectTimeoutMs", s.WebsocketConnectTimeoutMS)
	object.setString("tuiMode", s.TuiMode)
	object.setString("fullscreenExitOutput", s.FullscreenExitOutput)
	object.setString("fullscreenScrollbar", s.FullscreenScrollbar)
	object.setBool("fullscreenCopyOnSelect", s.FullscreenCopyOnSelect)
	return object.marshal()
}

// orderedObject builds a JSON object in insertion order.
type orderedObject struct {
	keys   []string
	values []any
}

func (o *orderedObject) set(key string, value any, set bool) {
	if !set {
		return
	}
	o.keys = append(o.keys, key)
	o.values = append(o.values, value)
}

func (o *orderedObject) setString(key string, value *string) {
	if value == nil {
		return
	}
	o.set(key, *value, true)
}

func (o *orderedObject) setBool(key string, value *bool) {
	if value == nil {
		return
	}
	o.set(key, *value, true)
}

func (o *orderedObject) setInt(key string, value *int) {
	if value == nil {
		return
	}
	o.set(key, *value, true)
}

func (o *orderedObject) setStrings(key string, value []string) {
	if value == nil {
		return
	}
	o.set(key, value, true)
}

func (o *orderedObject) setMap(key string, value map[string]string) {
	if value == nil {
		return
	}
	o.set(key, value, true)
}

func (o *orderedObject) setAny(key string, value any) {
	if value == nil {
		return
	}
	o.set(key, value, true)
}

func (o *orderedObject) marshal() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index, key := range o.keys {
		if index > 0 {
			buffer.WriteByte(',')
		}
		encodedKey, err := marshalJSONNoEscape(key)
		if err != nil {
			return nil, err
		}
		buffer.Write(encodedKey)
		buffer.WriteByte(':')
		encodedValue, err := marshalJSONNoEscape(o.values[index])
		if err != nil {
			return nil, err
		}
		buffer.Write(encodedValue)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

// marshalJSONNoEscape marshals without HTML escaping (JSON.stringify parity).
func marshalJSONNoEscape(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}

// marshalSettingsIndent renders settings like JSON.stringify(value, null, 2).
func marshalSettingsIndent(settings *Settings) string {
	ordered, err := settings.marshalOrdered()
	if err != nil {
		return "{}"
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(json.RawMessage(ordered)); err != nil {
		return "{}"
	}
	return strings.TrimRight(buffer.String(), "\n")
}

// GetGlobalSettings returns a copy of the global scope.
func (m *SettingsManager) GetGlobalSettings() *Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneSettings(m.globalSettings)
}

// GetProjectSettings returns a copy of the project scope.
func (m *SettingsManager) GetProjectSettings() *Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneSettings(m.projectSettings)
}

// IsProjectTrusted reports whether project settings may be loaded and written.
func (m *SettingsManager) IsProjectTrusted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.projectTrusted
}

// SetProjectTrusted switches project trust, reloading or dropping project
// settings.
func (m *SettingsManager) SetProjectTrusted(trusted bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.projectTrusted == trusted {
		return
	}
	m.projectTrusted = trusted
	m.modifiedProjectFields = map[string]bool{}
	m.modifiedProjectNested = map[string]map[string]bool{}

	if !trusted {
		m.projectSettings = &Settings{}
		m.projectSettingsLoadError = nil
		m.settings = DeepMergeSettings(m.globalSettings, m.projectSettings)
		return
	}
	projectLoad, projectErr := tryLoadFromStorage(m.storage, SettingsScopeProject, trusted)
	m.projectSettings = projectLoad
	m.projectSettingsLoadError = projectErr
	if projectErr != nil {
		m.recordError(SettingsScopeProject, projectErr)
	}
	m.settings = DeepMergeSettings(m.globalSettings, m.projectSettings)
}

// Reload re-reads both scopes from storage.
func (m *SettingsManager) Reload() {
	m.mu.Lock()
	defer m.mu.Unlock()
	globalLoad, globalErr := tryLoadFromStorage(m.storage, SettingsScopeGlobal, true)
	if globalErr == nil {
		m.globalSettings = globalLoad
		m.globalSettingsLoadError = nil
	} else {
		m.globalSettingsLoadError = globalErr
		m.recordError(SettingsScopeGlobal, globalErr)
	}
	m.modifiedFields = map[string]bool{}
	m.modifiedNestedFields = map[string]map[string]bool{}

	projectLoad, projectErr := tryLoadFromStorage(m.storage, SettingsScopeProject, m.projectTrusted)
	if projectErr == nil {
		m.projectSettings = projectLoad
		m.projectSettingsLoadError = nil
	} else {
		m.projectSettingsLoadError = projectErr
		m.recordError(SettingsScopeProject, projectErr)
	}
	m.settings = DeepMergeSettings(m.globalSettings, m.projectSettings)
}

// ApplyOverrides layers additional settings over the current view.
func (m *SettingsManager) ApplyOverrides(overrides *Settings) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings = DeepMergeSettings(m.settings, overrides)
}

// DrainErrors returns and clears the recorded settings errors.
func (m *SettingsManager) DrainErrors() []SettingsError {
	m.mu.Lock()
	defer m.mu.Unlock()
	drained := append([]SettingsError{}, m.errors...)
	m.errors = nil
	return drained
}

func (m *SettingsManager) recordError(scope SettingsScope, err error) {
	if err == nil {
		return
	}
	path := m.settingsPaths[scope]
	m.errors = append(m.errors, SettingsError{Scope: scope, Path: path, Error: err})
}

func (m *SettingsManager) markModified(field string, nestedKey string) {
	m.modifiedFields[field] = true
	if nestedKey != "" {
		if m.modifiedNestedFields[field] == nil {
			m.modifiedNestedFields[field] = map[string]bool{}
		}
		m.modifiedNestedFields[field][nestedKey] = true
	}
}

func (m *SettingsManager) markProjectModified(field string, nestedKey string) {
	m.modifiedProjectFields[field] = true
	if nestedKey != "" {
		if m.modifiedProjectNested[field] == nil {
			m.modifiedProjectNested[field] = map[string]bool{}
		}
		m.modifiedProjectNested[field][nestedKey] = true
	}
}

func (m *SettingsManager) clearModifiedScope(scope SettingsScope) {
	if scope == SettingsScopeGlobal {
		m.modifiedFields = map[string]bool{}
		m.modifiedNestedFields = map[string]map[string]bool{}
		return
	}
	m.modifiedProjectFields = map[string]bool{}
	m.modifiedProjectNested = map[string]map[string]bool{}
}

// save merges the scopes and persists the modified global fields.
func (m *SettingsManager) save() {
	m.settings = DeepMergeSettings(m.globalSettings, m.projectSettings)
	if m.globalSettingsLoadError != nil {
		return
	}
	snapshot := cloneSettings(m.globalSettings)
	modified := copyStringSet(m.modifiedFields)
	nested := copyNestedSet(m.modifiedNestedFields)
	if err := m.persistScopedSettings(SettingsScopeGlobal, snapshot, modified, nested); err != nil {
		m.recordError(SettingsScopeGlobal, err)
		return
	}
	m.clearModifiedScope(SettingsScopeGlobal)
}

func (m *SettingsManager) saveProjectSettings(settings *Settings) {
	if !m.projectTrusted {
		m.recordError(SettingsScopeProject, fmt.Errorf("Project is not trusted; refusing to write project settings"))
		return
	}
	m.projectSettings = cloneSettings(settings)
	m.settings = DeepMergeSettings(m.globalSettings, m.projectSettings)
	if m.projectSettingsLoadError != nil {
		return
	}
	snapshot := cloneSettings(m.projectSettings)
	modified := copyStringSet(m.modifiedProjectFields)
	nested := copyNestedSet(m.modifiedProjectNested)
	if err := m.persistScopedSettings(SettingsScopeProject, snapshot, modified, nested); err != nil {
		m.recordError(SettingsScopeProject, err)
		return
	}
	m.clearModifiedScope(SettingsScopeProject)
}

func (m *SettingsManager) updateProjectSettings(field string, update func(*Settings)) {
	if !m.projectTrusted {
		m.recordError(SettingsScopeProject, fmt.Errorf("Project is not trusted; refusing to write project settings"))
		return
	}
	projectSettings := cloneSettings(m.projectSettings)
	update(projectSettings)
	m.markProjectModified(field, "")
	m.saveProjectSettings(projectSettings)
}

func (m *SettingsManager) persistScopedSettings(
	scope SettingsScope,
	snapshot *Settings,
	modified map[string]bool,
	modifiedNested map[string]map[string]bool,
) error {
	return m.storage.WithLock(scope, func(current *string) *string {
		currentSettings := &Settings{}
		if current != nil && *current != "" {
			var raw map[string]any
			if err := json.Unmarshal([]byte(StripBom(*current)), &raw); err != nil {
				return nil
			}
			currentSettings = MigrateSettingsRaw(raw)
		}
		merged := settingsToMap(currentSettings)
		snapshotMap := settingsToMap(snapshot)
		for field := range modified {
			value, has := snapshotMap[field]
			if !has {
				continue
			}
			nestedKeys, hasNested := modifiedNested[field]
			valueObject, isObject := value.(map[string]any)
			baseObject, _ := merged[field].(map[string]any)
			if hasNested && isObject {
				mergedNested := map[string]any{}
				for key, entry := range baseObject {
					mergedNested[key] = entry
				}
				for nestedKey := range nestedKeys {
					mergedNested[nestedKey] = valueObject[nestedKey]
				}
				merged[field] = mergedNested
				continue
			}
			merged[field] = value
		}
		content := marshalSettingsIndent(mapToSettings(merged))
		return &content
	})
}

func copyStringSet(source map[string]bool) map[string]bool {
	out := make(map[string]bool, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func copyNestedSet(source map[string]map[string]bool) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(source))
	for key, value := range source {
		nested := make(map[string]bool, len(value))
		for nestedKey, nestedValue := range value {
			nested[nestedKey] = nestedValue
		}
		out[key] = nested
	}
	return out
}

// ─── Getters and setters ─────────────────────────────────────────────────────

// GetLastChangelogVersion returns the last seen changelog version.
func (m *SettingsManager) GetLastChangelogVersion() *string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.LastChangelogVersion
}

// SetLastChangelogVersion records the last seen changelog version.
// CacheWarmingMode selects how prompt caches are kept warm.
type CacheWarmingMode = string

const (
	CacheWarmingOff       CacheWarmingMode = "off"
	CacheWarmingStreaming CacheWarmingMode = "streaming"
	CacheWarmingIdle      CacheWarmingMode = "idle"
)

// CacheWarmingModes are the offered modes.
var CacheWarmingModes = []CacheWarmingMode{CacheWarmingOff, CacheWarmingStreaming, CacheWarmingIdle}

// GetCacheWarmingMode returns the warming mode (default "streaming").
func (m *SettingsManager) GetCacheWarmingMode() CacheWarmingMode {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.globalSettings.CacheWarming == nil {
		return CacheWarmingStreaming
	}
	return *m.globalSettings.CacheWarming
}

// SetCacheWarmingMode persists the warming mode (global scope only).
func (m *SettingsManager) SetCacheWarmingMode(mode CacheWarmingMode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.CacheWarming = &mode
	m.markModified("cacheWarming", "")
	m.save()
}

func (m *SettingsManager) SetLastChangelogVersion(version string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.LastChangelogVersion = &version
	m.markModified("lastChangelogVersion", "")
	m.save()
}

// GetSessionDir returns the custom session directory.
func (m *SettingsManager) GetSessionDir() *string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.SessionDir == nil || *m.settings.SessionDir == "" {
		return m.settings.SessionDir
	}
	normalized := NormalizePath(*m.settings.SessionDir, PathInputOptions{})
	return &normalized
}

// GetDefaultProvider returns the default provider.
func (m *SettingsManager) GetDefaultProvider() *string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.DefaultProvider
}

// GetDefaultModel returns the default model id.
func (m *SettingsManager) GetDefaultModel() *string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.DefaultModel
}

// SetDefaultProvider stores the default provider.
func (m *SettingsManager) SetDefaultProvider(provider string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.DefaultProvider = &provider
	m.markModified("defaultProvider", "")
	m.save()
}

// SetDefaultModel stores the default model id.
func (m *SettingsManager) SetDefaultModel(modelID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.DefaultModel = &modelID
	m.markModified("defaultModel", "")
	m.save()
}

// SetDefaultModelAndProvider stores both default fields.
func (m *SettingsManager) SetDefaultModelAndProvider(provider, modelID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.DefaultProvider = &provider
	m.globalSettings.DefaultModel = &modelID
	m.markModified("defaultProvider", "")
	m.markModified("defaultModel", "")
	m.save()
}

// GetSteeringMode returns the steering mode (default one-at-a-time).
func (m *SettingsManager) GetSteeringMode() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.SteeringMode == nil {
		return "one-at-a-time"
	}
	return *m.settings.SteeringMode
}

// SetSteeringMode stores the steering mode.
func (m *SettingsManager) SetSteeringMode(mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.SteeringMode = &mode
	m.markModified("steeringMode", "")
	m.save()
}

// GetFollowUpMode returns the follow-up mode (default one-at-a-time).
func (m *SettingsManager) GetFollowUpMode() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.FollowUpMode == nil {
		return "one-at-a-time"
	}
	return *m.settings.FollowUpMode
}

// SetFollowUpMode stores the follow-up mode.
func (m *SettingsManager) SetFollowUpMode(mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.FollowUpMode = &mode
	m.markModified("followUpMode", "")
	m.save()
}

// GetThemeSetting returns the raw theme setting.
func (m *SettingsManager) GetThemeSetting() *string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.Theme
}

// GetTheme returns the theme name unless it is a path.
func (m *SettingsManager) GetTheme() *string {
	theme := m.GetThemeSetting()
	if theme == nil || strings.Contains(*theme, "/") {
		return nil
	}
	return theme
}

// SetTheme stores the theme name.
func (m *SettingsManager) SetTheme(theme string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.Theme = &theme
	m.markModified("theme", "")
	m.save()
}

// GetDefaultThinkingLevel returns the default thinking level.
func (m *SettingsManager) GetDefaultThinkingLevel() *string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.DefaultThinkingLevel
}

// SetDefaultThinkingLevel stores the default thinking level.
func (m *SettingsManager) SetDefaultThinkingLevel(level string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.DefaultThinkingLevel = &level
	m.markModified("defaultThinkingLevel", "")
	m.save()
}

// GetModelThinkingLevel returns one model's thinking level override.
func (m *SettingsManager) GetModelThinkingLevel(provider, modelID string) *string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.ModelThinkingLevels == nil {
		return nil
	}
	if level, ok := m.settings.ModelThinkingLevels[provider+"/"+modelID]; ok {
		return &level
	}
	return nil
}

// GetAllModelThinkingLevels returns a copy of the model override map.
func (m *SettingsManager) GetAllModelThinkingLevels() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for key, value := range m.settings.ModelThinkingLevels {
		out[key] = value
	}
	return out
}

// SetModelThinkingLevel stores one model's thinking level override.
func (m *SettingsManager) SetModelThinkingLevel(provider, modelID, level string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.globalSettings.ModelThinkingLevels == nil {
		m.globalSettings.ModelThinkingLevels = map[string]string{}
	}
	m.globalSettings.ModelThinkingLevels[provider+"/"+modelID] = level
	m.markModified("modelThinkingLevels", "")
	m.save()
}

// RemoveModelThinkingLevel drops one model's override.
func (m *SettingsManager) RemoveModelThinkingLevel(provider, modelID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.globalSettings.ModelThinkingLevels == nil {
		return
	}
	delete(m.globalSettings.ModelThinkingLevels, provider+"/"+modelID)
	if len(m.globalSettings.ModelThinkingLevels) == 0 {
		m.globalSettings.ModelThinkingLevels = nil
	}
	m.markModified("modelThinkingLevels", "")
	m.save()
}

// GetTransport returns the transport setting (default auto).
func (m *SettingsManager) GetTransport() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.Transport == nil {
		return "auto"
	}
	return *m.settings.Transport
}

// SetTransport stores the transport setting.
func (m *SettingsManager) SetTransport(transport string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.Transport = &transport
	m.markModified("transport", "")
	m.save()
}

// GetCompactionEnabled reports whether compaction is enabled (default true).
func (m *SettingsManager) GetCompactionEnabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.Compaction == nil || m.settings.Compaction.Enabled == nil {
		return true
	}
	return *m.settings.Compaction.Enabled
}

// SetCompactionEnabled stores the compaction toggle.
func (m *SettingsManager) SetCompactionEnabled(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.globalSettings.Compaction == nil {
		m.globalSettings.Compaction = &SettingsCompaction{}
	}
	m.globalSettings.Compaction.Enabled = &enabled
	m.markModified("compaction", "enabled")
	m.save()
}

func (m *SettingsManager) compactionTokenSetting(field string, model *ai.Model) (int64, error) {
	compaction := m.settings.Compaction
	var ordinary *int64
	if compaction != nil {
		if field == "reserveTokens" {
			ordinary = compaction.ReserveTokens
		} else {
			ordinary = compaction.KeepRecentTokens
		}
	}
	if ordinary != nil && *ordinary < 0 {
		return 0, fmt.Errorf("Invalid compaction.%s setting: %d. Expected a non-negative safe integer.", field, *ordinary)
	}

	var override *int64
	if model != nil && compaction != nil && compaction.ModelOverrides != nil {
		modelKey := model.Provider + "/" + model.ID
		if entry, ok := compaction.ModelOverrides[modelKey]; ok {
			if field == "reserveTokens" {
				override = entry.ReserveTokens
			} else {
				override = entry.KeepRecentTokens
			}
			if override != nil && *override < 0 {
				return 0, fmt.Errorf(
					"Invalid compaction.modelOverrides[%q].%s setting: %d. Expected a non-negative safe integer.",
					modelKey, field, *override)
			}
		}
	}
	if override != nil {
		return *override, nil
	}
	if ordinary != nil {
		return *ordinary, nil
	}
	if field == "reserveTokens" {
		return DefaultCompactionReserveTokens, nil
	}
	return DefaultCompactionKeepRecentTokens, nil
}

// GetCompactionReserveTokens resolves the reserve-token setting.
func (m *SettingsManager) GetCompactionReserveTokens(model *ai.Model) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.compactionTokenSetting("reserveTokens", model)
}

// GetCompactionKeepRecentTokens resolves the keep-recent-token setting.
func (m *SettingsManager) GetCompactionKeepRecentTokens(model *ai.Model) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.compactionTokenSetting("keepRecentTokens", model)
}

// CompactionSettingsResult is the resolved compaction configuration.
type CompactionSettingsResult struct {
	Enabled          bool  `json:"enabled"`
	ReserveTokens    int64 `json:"reserveTokens"`
	KeepRecentTokens int64 `json:"keepRecentTokens"`
}

// GetCompactionSettings resolves the full compaction configuration.
func (m *SettingsManager) GetCompactionSettings(model *ai.Model) (CompactionSettingsResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	reserve, err := m.compactionTokenSetting("reserveTokens", model)
	if err != nil {
		return CompactionSettingsResult{}, err
	}
	keepRecent, err := m.compactionTokenSetting("keepRecentTokens", model)
	if err != nil {
		return CompactionSettingsResult{}, err
	}
	enabled := true
	if m.settings.Compaction != nil && m.settings.Compaction.Enabled != nil {
		enabled = *m.settings.Compaction.Enabled
	}
	return CompactionSettingsResult{Enabled: enabled, ReserveTokens: reserve, KeepRecentTokens: keepRecent}, nil
}

// BranchSummarySettingsResult is the resolved branch-summary configuration.
type BranchSummarySettingsResult struct {
	ReserveTokens int64 `json:"reserveTokens"`
	SkipPrompt    bool  `json:"skipPrompt"`
}

// GetBranchSummarySettings resolves the branch-summary configuration.
func (m *SettingsManager) GetBranchSummarySettings() BranchSummarySettingsResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	reserve := int64(16384)
	if m.settings.BranchSummary != nil && m.settings.BranchSummary.ReserveTokens != nil {
		reserve = *m.settings.BranchSummary.ReserveTokens
	}
	skip := false
	if m.settings.BranchSummary != nil && m.settings.BranchSummary.SkipPrompt != nil {
		skip = *m.settings.BranchSummary.SkipPrompt
	}
	return BranchSummarySettingsResult{ReserveTokens: reserve, SkipPrompt: skip}
}

// GetBranchSummarySkipPrompt reports the skip-prompt toggle.
func (m *SettingsManager) GetBranchSummarySkipPrompt() bool {
	return m.GetBranchSummarySettings().SkipPrompt
}

// GetRetryEnabled reports whether retries are enabled (default true).
func (m *SettingsManager) GetRetryEnabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.Retry == nil || m.settings.Retry.Enabled == nil {
		return true
	}
	return *m.settings.Retry.Enabled
}

// SetRetryEnabled stores the retry toggle.
func (m *SettingsManager) SetRetryEnabled(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.globalSettings.Retry == nil {
		m.globalSettings.Retry = &SettingsRetry{}
	}
	m.globalSettings.Retry.Enabled = &enabled
	m.markModified("retry", "enabled")
	m.save()
}

// RetrySettingsResult is the resolved retry configuration.
type RetrySettingsResult struct {
	Enabled         bool  `json:"enabled"`
	MaxRetries      int   `json:"maxRetries"`
	BaseDelayMS     int64 `json:"baseDelayMs"`
	MaxAgentDelayMS int64 `json:"maxAgentDelayMs"`
}

// GetRetrySettings resolves the retry configuration.
func (m *SettingsManager) GetRetrySettings() RetrySettingsResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	enabled := true
	maxRetries := 3
	baseDelay := int64(2000)
	maxAgentDelay := int64(ai.DefaultMaxAgentRetryDelayMS)
	if m.settings.Retry != nil {
		if m.settings.Retry.Enabled != nil {
			enabled = *m.settings.Retry.Enabled
		}
		if m.settings.Retry.MaxRetries != nil {
			maxRetries = *m.settings.Retry.MaxRetries
		}
		if m.settings.Retry.BaseDelayMS != nil {
			baseDelay = *m.settings.Retry.BaseDelayMS
		}
		if m.settings.Retry.MaxAgentDelayMS != nil {
			maxAgentDelay = *m.settings.Retry.MaxAgentDelayMS
		}
	}
	return RetrySettingsResult{Enabled: enabled, MaxRetries: maxRetries, BaseDelayMS: baseDelay, MaxAgentDelayMS: maxAgentDelay}
}

// GetHTTPIdleTimeoutMS resolves the HTTP idle timeout.
func (m *SettingsManager) GetHTTPIdleTimeoutMS() (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := ParseHTTPIdleTimeoutMS(m.settings.HTTPIdleTimeoutMS)
	if ok {
		return value, nil
	}
	if m.settings.HTTPIdleTimeoutMS != nil {
		return 0, fmt.Errorf("Invalid httpIdleTimeoutMs setting: %v", m.settings.HTTPIdleTimeoutMS)
	}
	return DefaultHTTPIdleTimeoutMS, nil
}

// SetHTTPIdleTimeoutMS stores the HTTP idle timeout.
func (m *SettingsManager) SetHTTPIdleTimeoutMS(timeoutMS int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if timeoutMS < 0 {
		return fmt.Errorf("Invalid httpIdleTimeoutMs setting: %d", timeoutMS)
	}
	m.globalSettings.HTTPIdleTimeoutMS = float64(timeoutMS)
	m.markModified("httpIdleTimeoutMs", "")
	m.save()
	return nil
}

// ProviderRetrySettingsResult is the resolved provider retry configuration.
type ProviderRetrySettingsResult struct {
	TimeoutMS       *int64 `json:"timeoutMs,omitempty"`
	MaxRetries      *int   `json:"maxRetries,omitempty"`
	MaxRetryDelayMS int64  `json:"maxRetryDelayMs"`
}

// GetProviderRetrySettings resolves the provider retry configuration.
func (m *SettingsManager) GetProviderRetrySettings() ProviderRetrySettingsResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := ProviderRetrySettingsResult{MaxRetryDelayMS: 60000}
	if m.settings.Retry != nil && m.settings.Retry.Provider != nil {
		result.TimeoutMS = m.settings.Retry.Provider.TimeoutMS
		result.MaxRetries = m.settings.Retry.Provider.MaxRetries
		if m.settings.Retry.Provider.MaxRetryDelayMS != nil {
			result.MaxRetryDelayMS = *m.settings.Retry.Provider.MaxRetryDelayMS
		}
	}
	return result
}

// GetWebSocketConnectTimeoutMS resolves the WebSocket connect timeout.
func (m *SettingsManager) GetWebSocketConnectTimeoutMS() (int64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return ParseHTTPIdleTimeoutMS(m.settings.WebsocketConnectTimeoutMS)
}

// GetHideThinkingBlock reports whether thinking blocks are hidden.
func (m *SettingsManager) GetHideThinkingBlock() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.HideThinkingBlock != nil && *m.settings.HideThinkingBlock
}

// GetShowCacheMissNotices reports whether cache notices are shown.
func (m *SettingsManager) GetShowCacheMissNotices() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.ShowCacheMissNotices != nil && *m.settings.ShowCacheMissNotices
}

// GetExternalEditorCommand resolves the external editor command.
func (m *SettingsManager) GetExternalEditorCommand() string {
	m.mu.Lock()
	configured := m.settings.ExternalEditor
	m.mu.Unlock()
	if configured != nil && strings.TrimSpace(*configured) != "" {
		return *configured
	}
	if env := os.Getenv("VISUAL"); env != "" {
		return env
	}
	if env := os.Getenv("EDITOR"); env != "" {
		return env
	}
	if runtime.GOOS == "windows" {
		return "notepad"
	}
	return "nano"
}

// SetHideThinkingBlock stores the thinking-block toggle.
func (m *SettingsManager) SetHideThinkingBlock(hide bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.HideThinkingBlock = &hide
	m.markModified("hideThinkingBlock", "")
	m.save()
}

// SetShowCacheMissNotices stores the cache-notice toggle.
func (m *SettingsManager) SetShowCacheMissNotices(show bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.ShowCacheMissNotices = &show
	m.markModified("showCacheMissNotices", "")
	m.save()
}

// GetShellPath returns the configured shell path.
func (m *SettingsManager) GetShellPath() *string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.ShellPath == nil || *m.settings.ShellPath == "" {
		return m.settings.ShellPath
	}
	normalized := NormalizePath(*m.settings.ShellPath, PathInputOptions{})
	return &normalized
}

// SetShellPath stores the shell path.
func (m *SettingsManager) SetShellPath(path *string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.ShellPath = path
	m.markModified("shellPath", "")
	m.save()
}

// GetQuietStartup reports the quiet-startup toggle.
func (m *SettingsManager) GetQuietStartup() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.QuietStartup != nil && *m.settings.QuietStartup
}

// SetQuietStartup stores the quiet-startup toggle.
func (m *SettingsManager) SetQuietStartup(quiet bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.QuietStartup = &quiet
	m.markModified("quietStartup", "")
	m.save()
}

// GetDefaultProjectTrust returns the default project trust policy.
func (m *SettingsManager) GetDefaultProjectTrust() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.globalSettings.DefaultProjectTrust != nil {
		value := *m.globalSettings.DefaultProjectTrust
		if value == "always" || value == "never" {
			return value
		}
	}
	return "ask"
}

// SetDefaultProjectTrust stores the default project trust policy.
func (m *SettingsManager) SetDefaultProjectTrust(policy string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.DefaultProjectTrust = &policy
	m.markModified("defaultProjectTrust", "")
	m.save()
}

// GetShellCommandPrefix returns the shell command prefix.
func (m *SettingsManager) GetShellCommandPrefix() *string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.ShellCommandPrefix
}

// SetShellCommandPrefix stores the shell command prefix.
func (m *SettingsManager) SetShellCommandPrefix(prefix *string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.ShellCommandPrefix = prefix
	m.markModified("shellCommandPrefix", "")
	m.save()
}

// GetNpmCommand returns a copy of the npm command argv.
func (m *SettingsManager) GetNpmCommand() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.NpmCommand == nil {
		return nil
	}
	return append([]string{}, m.settings.NpmCommand...)
}

// SetNpmCommand stores the npm command argv.
func (m *SettingsManager) SetNpmCommand(command []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if command == nil {
		m.globalSettings.NpmCommand = nil
	} else {
		m.globalSettings.NpmCommand = append([]string{}, command...)
	}
	m.markModified("npmCommand", "")
	m.save()
}

// GetCollapseChangelog reports the collapse-changelog toggle.
func (m *SettingsManager) GetCollapseChangelog() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.CollapseChangelog != nil && *m.settings.CollapseChangelog
}

// SetCollapseChangelog stores the collapse-changelog toggle.
func (m *SettingsManager) SetCollapseChangelog(collapse bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.CollapseChangelog = &collapse
	m.markModified("collapseChangelog", "")
	m.save()
}

// GetEnableInstallTelemetry reports the install telemetry toggle (default true).
func (m *SettingsManager) GetEnableInstallTelemetry() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.EnableInstallTelemetry == nil {
		return true
	}
	return *m.settings.EnableInstallTelemetry
}

// SetEnableInstallTelemetry stores the install telemetry toggle.
func (m *SettingsManager) SetEnableInstallTelemetry(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.EnableInstallTelemetry = &enabled
	m.markModified("enableInstallTelemetry", "")
	m.save()
}

// GetEnableAnalytics reports the analytics opt-in.
func (m *SettingsManager) GetEnableAnalytics() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.EnableAnalytics != nil && *m.settings.EnableAnalytics
}

// GetTrackingID returns the analytics tracking id.
func (m *SettingsManager) GetTrackingID() *string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.TrackingID
}

// SetEnableAnalytics stores the analytics opt-in, generating a tracking id on
// first opt-in.
func (m *SettingsManager) SetEnableAnalytics(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.EnableAnalytics = &enabled
	m.markModified("enableAnalytics", "")
	if enabled && (m.globalSettings.TrackingID == nil || *m.globalSettings.TrackingID == "") {
		id := newTrackingID()
		m.globalSettings.TrackingID = &id
		m.markModified("trackingId", "")
	}
	m.save()
}

func newTrackingID() string {
	// Upstream uses node:crypto randomUUID(); attachment ids reuse the same
	// v4 generator shape via the server package's helper equivalent.
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Sprintf("tracking-%d", time.Now().UnixNano())
	}
	random[6] = (random[6] & 0x0f) | 0x40
	random[8] = (random[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", random[0:4], random[4:6], random[6:8], random[8:10], random[10:16])
}

// GetPackages returns a copy of the package sources.
func (m *SettingsManager) GetPackages() []any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]any{}, m.settings.Packages...)
}

// SetPackages stores the global package sources.
func (m *SettingsManager) SetPackages(packages []any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.Packages = packages
	m.markModified("packages", "")
	m.save()
}

// SetProjectPackages stores the project package sources.
func (m *SettingsManager) SetProjectPackages(packages []any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateProjectSettings("packages", func(settings *Settings) { settings.Packages = packages })
}

// GetExtensionPaths returns the extension paths.
func (m *SettingsManager) GetExtensionPaths() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.settings.Extensions...)
}

// SetExtensionPaths stores the global extension paths.
func (m *SettingsManager) SetExtensionPaths(paths []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.Extensions = paths
	m.markModified("extensions", "")
	m.save()
}

// SetProjectExtensionPaths stores the project extension paths.
func (m *SettingsManager) SetProjectExtensionPaths(paths []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateProjectSettings("extensions", func(settings *Settings) { settings.Extensions = paths })
}

// GetSkillPaths returns the skill paths.
func (m *SettingsManager) GetSkillPaths() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.settings.Skills...)
}

// SetSkillPaths stores the global skill paths.
func (m *SettingsManager) SetSkillPaths(paths []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.Skills = paths
	m.markModified("skills", "")
	m.save()
}

// SetProjectSkillPaths stores the project skill paths.
func (m *SettingsManager) SetProjectSkillPaths(paths []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateProjectSettings("skills", func(settings *Settings) { settings.Skills = paths })
}

// GetPromptTemplatePaths returns the prompt template paths.
func (m *SettingsManager) GetPromptTemplatePaths() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.settings.Prompts...)
}

// SetPromptTemplatePaths stores the global prompt template paths.
func (m *SettingsManager) SetPromptTemplatePaths(paths []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.Prompts = paths
	m.markModified("prompts", "")
	m.save()
}

// SetProjectPromptTemplatePaths stores the project prompt template paths.
func (m *SettingsManager) SetProjectPromptTemplatePaths(paths []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateProjectSettings("prompts", func(settings *Settings) { settings.Prompts = paths })
}

// GetThemePaths returns the theme paths.
func (m *SettingsManager) GetThemePaths() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.settings.Themes...)
}

// SetThemePaths stores the global theme paths.
func (m *SettingsManager) SetThemePaths(paths []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.Themes = paths
	m.markModified("themes", "")
	m.save()
}

// SetProjectThemePaths stores the project theme paths.
func (m *SettingsManager) SetProjectThemePaths(paths []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateProjectSettings("themes", func(settings *Settings) { settings.Themes = paths })
}

// GetEnableSkillCommands reports whether skills are registered as commands.
func (m *SettingsManager) GetEnableSkillCommands() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.EnableSkillCommands == nil {
		return true
	}
	return *m.settings.EnableSkillCommands
}

// SetEnableSkillCommands stores the skill-command toggle.
func (m *SettingsManager) SetEnableSkillCommands(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.EnableSkillCommands = &enabled
	m.markModified("enableSkillCommands", "")
	m.save()
}

// GetThinkingBudgets returns the thinking budgets.
func (m *SettingsManager) GetThinkingBudgets() *SettingsThinkingBudgets {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.ThinkingBudgets
}

// GetShowImages reports the show-images toggle (default true).
func (m *SettingsManager) GetShowImages() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.Terminal == nil || m.settings.Terminal.ShowImages == nil {
		return true
	}
	return *m.settings.Terminal.ShowImages
}

// SetShowImages stores the show-images toggle.
func (m *SettingsManager) SetShowImages(show bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.globalSettings.Terminal == nil {
		m.globalSettings.Terminal = &SettingsTerminal{}
	}
	m.globalSettings.Terminal.ShowImages = &show
	m.markModified("terminal", "showImages")
	m.save()
}

// GetImageWidthCells returns the preferred inline image width (default 60).
func (m *SettingsManager) GetImageWidthCells() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.Terminal == nil || m.settings.Terminal.ImageWidthCells == nil {
		return 60
	}
	width := *m.settings.Terminal.ImageWidthCells
	if width < 1 {
		return 1
	}
	return width
}

// SetImageWidthCells stores the inline image width.
func (m *SettingsManager) SetImageWidthCells(width int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.globalSettings.Terminal == nil {
		m.globalSettings.Terminal = &SettingsTerminal{}
	}
	if width < 1 {
		width = 1
	}
	m.globalSettings.Terminal.ImageWidthCells = &width
	m.markModified("terminal", "imageWidthCells")
	m.save()
}

// GetClearOnShrink reports the clear-on-shrink toggle (settings, then
// PI_CLEAR_ON_SHRINK, then false).
func (m *SettingsManager) GetClearOnShrink() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.Terminal != nil && m.settings.Terminal.ClearOnShrink != nil {
		return *m.settings.Terminal.ClearOnShrink
	}
	return os.Getenv("PI_CLEAR_ON_SHRINK") == "1"
}

// SetClearOnShrink stores the clear-on-shrink toggle.
func (m *SettingsManager) SetClearOnShrink(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.globalSettings.Terminal == nil {
		m.globalSettings.Terminal = &SettingsTerminal{}
	}
	m.globalSettings.Terminal.ClearOnShrink = &enabled
	m.markModified("terminal", "clearOnShrink")
	m.save()
}

// GetShowTerminalProgress reports the terminal-progress toggle.
func (m *SettingsManager) GetShowTerminalProgress() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.Terminal != nil && m.settings.Terminal.ShowTerminalProgress != nil &&
		*m.settings.Terminal.ShowTerminalProgress
}

// SetShowTerminalProgress stores the terminal-progress toggle.
func (m *SettingsManager) SetShowTerminalProgress(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.globalSettings.Terminal == nil {
		m.globalSettings.Terminal = &SettingsTerminal{}
	}
	m.globalSettings.Terminal.ShowTerminalProgress = &enabled
	m.markModified("terminal", "showTerminalProgress")
	m.save()
}

// GetTuiMode returns the TUI mode (regular unless fullscreen).
func (m *SettingsManager) GetTuiMode() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.TuiMode != nil && *m.settings.TuiMode == "fullscreen" {
		return "fullscreen"
	}
	return "regular"
}

// SetTuiMode stores the TUI mode.
func (m *SettingsManager) SetTuiMode(mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.TuiMode = &mode
	m.markModified("tuiMode", "")
	m.save()
}

// GetFullscreenExitOutput returns the fullscreen exit output mode.
func (m *SettingsManager) GetFullscreenExitOutput() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.FullscreenExitOutput != nil && *m.settings.FullscreenExitOutput == "resume-hint" {
		return "resume-hint"
	}
	return "transcript"
}

// SetFullscreenExitOutput stores the fullscreen exit output mode.
func (m *SettingsManager) SetFullscreenExitOutput(output string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.FullscreenExitOutput = &output
	m.markModified("fullscreenExitOutput", "")
	m.save()
}

// GetFullscreenScrollbar returns the fullscreen scrollbar mode.
func (m *SettingsManager) GetFullscreenScrollbar() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.FullscreenScrollbar != nil {
		value := *m.settings.FullscreenScrollbar
		if value == "always" || value == "hidden" {
			return value
		}
	}
	return "auto"
}

// SetFullscreenScrollbar stores the fullscreen scrollbar mode.
func (m *SettingsManager) SetFullscreenScrollbar(mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.FullscreenScrollbar = &mode
	m.markModified("fullscreenScrollbar", "")
	m.save()
}

// GetFullscreenCopyOnSelect reports the copy-on-select toggle (default true).
func (m *SettingsManager) GetFullscreenCopyOnSelect() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.FullscreenCopyOnSelect == nil {
		return true
	}
	return *m.settings.FullscreenCopyOnSelect
}

// SetFullscreenCopyOnSelect stores the copy-on-select toggle.
func (m *SettingsManager) SetFullscreenCopyOnSelect(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.FullscreenCopyOnSelect = &enabled
	m.markModified("fullscreenCopyOnSelect", "")
	m.save()
}

// GetImageAutoResize reports the image auto-resize toggle (default true).
func (m *SettingsManager) GetImageAutoResize() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.Images == nil || m.settings.Images.AutoResize == nil {
		return true
	}
	return *m.settings.Images.AutoResize
}

// SetImageAutoResize stores the image auto-resize toggle.
func (m *SettingsManager) SetImageAutoResize(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.globalSettings.Images == nil {
		m.globalSettings.Images = &SettingsImages{}
	}
	m.globalSettings.Images.AutoResize = &enabled
	m.markModified("images", "autoResize")
	m.save()
}

// GetBlockImages reports the block-images toggle.
func (m *SettingsManager) GetBlockImages() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.Images != nil && m.settings.Images.BlockImages != nil && *m.settings.Images.BlockImages
}

// SetBlockImages stores the block-images toggle.
func (m *SettingsManager) SetBlockImages(blocked bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.globalSettings.Images == nil {
		m.globalSettings.Images = &SettingsImages{}
	}
	m.globalSettings.Images.BlockImages = &blocked
	m.markModified("images", "blockImages")
	m.save()
}

// GetEnabledModels returns the model patterns for cycling.
func (m *SettingsManager) GetEnabledModels() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.EnabledModels
}

// SetEnabledModels stores the model patterns for cycling.
func (m *SettingsManager) SetEnabledModels(patterns []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.EnabledModels = patterns
	m.markModified("enabledModels", "")
	m.save()
}

// GetDefaultTools returns the default tool selection.
func (m *SettingsManager) GetDefaultTools() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.DefaultTools == nil {
		return nil
	}
	return append([]string{}, m.settings.DefaultTools...)
}

// GetDoubleEscapeAction returns the double-escape action (default tree).
func (m *SettingsManager) GetDoubleEscapeAction() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.DoubleEscapeAction == nil {
		return "tree"
	}
	return *m.settings.DoubleEscapeAction
}

// SetDoubleEscapeAction stores the double-escape action.
func (m *SettingsManager) SetDoubleEscapeAction(action string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.DoubleEscapeAction = &action
	m.markModified("doubleEscapeAction", "")
	m.save()
}

// GetTreeFilterMode returns the tree filter mode (default default).
func (m *SettingsManager) GetTreeFilterMode() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.TreeFilterMode != nil {
		value := *m.settings.TreeFilterMode
		for _, valid := range []string{"default", "no-tools", "user-only", "labeled-only", "all"} {
			if value == valid {
				return value
			}
		}
	}
	return "default"
}

// SetTreeFilterMode stores the tree filter mode.
func (m *SettingsManager) SetTreeFilterMode(mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.TreeFilterMode = &mode
	m.markModified("treeFilterMode", "")
	m.save()
}

// GetShowHardwareCursor reports the hardware-cursor toggle (settings, then
// PI_HARDWARE_CURSOR).
func (m *SettingsManager) GetShowHardwareCursor() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.ShowHardwareCursor != nil {
		return *m.settings.ShowHardwareCursor
	}
	return os.Getenv("PI_HARDWARE_CURSOR") == "1"
}

// SetShowHardwareCursor stores the hardware-cursor toggle.
func (m *SettingsManager) SetShowHardwareCursor(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.ShowHardwareCursor = &enabled
	m.markModified("showHardwareCursor", "")
	m.save()
}

// GetEditorPaddingX returns the editor padding (clamped 0..3).
func (m *SettingsManager) GetEditorPaddingX() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.EditorPaddingX == nil {
		return 0
	}
	return *m.settings.EditorPaddingX
}

// SetEditorPaddingX stores the editor padding.
func (m *SettingsManager) SetEditorPaddingX(padding int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	padding = maxInt(0, minInt(3, padding))
	m.globalSettings.EditorPaddingX = &padding
	m.markModified("editorPaddingX", "")
	m.save()
}

// GetOutputPad returns the output padding (0 or 1).
func (m *SettingsManager) GetOutputPad() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.OutputPad != nil && *m.settings.OutputPad == 0 {
		return 0
	}
	return 1
}

// SetOutputPad stores the output padding.
func (m *SettingsManager) SetOutputPad(padding int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalSettings.OutputPad = &padding
	m.markModified("outputPad", "")
	m.save()
}

// GetAutocompleteMaxVisible returns the autocomplete item limit (default 5).
func (m *SettingsManager) GetAutocompleteMaxVisible() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.AutocompleteMaxVisible == nil {
		return 5
	}
	return *m.settings.AutocompleteMaxVisible
}

// SetAutocompleteMaxVisible stores the autocomplete item limit.
func (m *SettingsManager) SetAutocompleteMaxVisible(maxVisible int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	maxVisible = maxInt(3, minInt(20, maxVisible))
	m.globalSettings.AutocompleteMaxVisible = &maxVisible
	m.markModified("autocompleteMaxVisible", "")
	m.save()
}

// GetCodeBlockIndent returns the markdown code block indent (default two spaces).
func (m *SettingsManager) GetCodeBlockIndent() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.Markdown == nil || m.settings.Markdown.CodeBlockIndent == nil {
		return "  "
	}
	return *m.settings.Markdown.CodeBlockIndent
}

// GetMermaidRenderingMode returns the mermaid rendering mode.
func (m *SettingsManager) GetMermaidRenderingMode() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.Markdown != nil && m.settings.Markdown.Mermaid != nil {
		value := *m.settings.Markdown.Mermaid
		if value == "off" || value == "final" {
			return value
		}
	}
	return "streaming"
}

// SetMermaidRenderingMode stores the mermaid rendering mode.
func (m *SettingsManager) SetMermaidRenderingMode(mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.globalSettings.Markdown == nil {
		m.globalSettings.Markdown = &SettingsMarkdown{}
	}
	m.globalSettings.Markdown.Mermaid = &mode
	m.markModified("markdown", "mermaid")
	m.save()
}

// GetWarnings returns a copy of the warning toggles.
func (m *SettingsManager) GetWarnings() SettingsWarnings {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := SettingsWarnings{}
	if m.settings.Warnings != nil {
		out = *m.settings.Warnings
	}
	return out
}

// SetWarnings stores the warning toggles.
func (m *SettingsManager) SetWarnings(warnings SettingsWarnings) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := warnings
	m.globalSettings.Warnings = &copied
	m.markModified("warnings", "")
	m.save()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
