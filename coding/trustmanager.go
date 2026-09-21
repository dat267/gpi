package coding

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dat267/pier/ai"
)

// Port of core/trust-manager.ts: the project-trust store, trust options, and
// the check for project resources that require trust.

// ProjectTrustDecision is true, false, or null (unknown → Go nil).
type ProjectTrustDecision = *bool

// ProjectTrustStoreEntry is one resolved trust decision.
type ProjectTrustStoreEntry struct {
	Path     string
	Decision bool
}

// ProjectTrustUpdate is one pending trust change.
type ProjectTrustUpdate struct {
	Path     string
	Decision ProjectTrustDecision
}

// ProjectTrustOption is one selectable trust option.
type ProjectTrustOption struct {
	Label     string
	Trusted   bool
	Updates   []ProjectTrustUpdate
	SavedPath string
}

// trustRequiringProjectConfigResources are the .pi entries that gate trust.
var trustRequiringProjectConfigResources = []string{
	"settings.json",
	"extensions",
	"skills",
	"prompts",
	"themes",
	"SYSTEM.md",
	"APPEND_SYSTEM.md",
}

// trustFile is the trust.json content: path to decision (nil = null entry).
type trustFile map[string]ProjectTrustDecision

func normalizeTrustCwd(cwd string) string {
	return CanonicalizePath(ResolvePath(cwd, "", PathInputOptions{}))
}

// findNearestTrustEntry walks up from cwd looking for a decision.
func findNearestTrustEntry(data trustFile, cwd string) *ProjectTrustStoreEntry {
	currentDir := normalizeTrustCwd(cwd)
	for {
		if value, ok := data[currentDir]; ok && value != nil {
			return &ProjectTrustStoreEntry{Path: currentDir, Decision: *value}
		}
		parentDir := filepath.Dir(currentDir)
		if parentDir == currentDir {
			return nil
		}
		currentDir = parentDir
	}
}

// GetProjectTrustParentPath returns the parent of the trust path, if any.
func GetProjectTrustParentPath(cwd string) (string, bool) {
	trustPath := normalizeTrustCwd(cwd)
	parentDir := filepath.Dir(trustPath)
	if parentDir == trustPath {
		return "", false
	}
	return parentDir, true
}

// GetProjectTrustOptions lists the trust choices for a cwd.
func GetProjectTrustOptions(cwd string, includeSessionOnly bool) []ProjectTrustOption {
	trustPath := normalizeTrustCwd(cwd)
	options := []ProjectTrustOption{{
		Label:     "Trust",
		Trusted:   true,
		Updates:   []ProjectTrustUpdate{{Path: trustPath, Decision: boolPointer(true)}},
		SavedPath: trustPath,
	}}
	if parentPath, ok := GetProjectTrustParentPath(cwd); ok {
		options = append(options, ProjectTrustOption{
			Label:   fmt.Sprintf("Trust parent folder (%s)", parentPath),
			Trusted: true,
			Updates: []ProjectTrustUpdate{
				{Path: parentPath, Decision: boolPointer(true)},
				{Path: trustPath, Decision: nil},
			},
			SavedPath: parentPath,
		})
	}
	if includeSessionOnly {
		options = append(options, ProjectTrustOption{Label: "Trust (this session only)", Trusted: true})
	}
	options = append(options, ProjectTrustOption{
		Label:     "Do not trust",
		Trusted:   false,
		Updates:   []ProjectTrustUpdate{{Path: trustPath, Decision: boolPointer(false)}},
		SavedPath: trustPath,
	})
	if includeSessionOnly {
		options = append(options, ProjectTrustOption{Label: "Do not trust (this session only)", Trusted: false})
	}
	return options
}

func boolPointer(value bool) *bool { return &value }

// sleepMilliseconds sleeps for the trust-store retry delay.
func sleepMilliseconds(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

// readTrustFile parses and validates the trust store.
func readTrustFile(path string) (trustFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return trustFile{}, nil
		}
		return nil, fmt.Errorf("Failed to read trust store %s: %v", path, err)
	}
	var parsed any
	if err := json.Unmarshal([]byte(StripBom(string(raw))), &parsed); err != nil {
		return nil, fmt.Errorf("Failed to read trust store %s: %v", path, err)
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Invalid trust store %s: expected an object", path)
	}
	data := trustFile{}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		switch value := object[key].(type) {
		case bool:
			data[key] = boolPointer(value)
		case nil:
			data[key] = nil
		default:
			name, _ := json.Marshal(key)
			return nil, fmt.Errorf("Invalid trust store %s: value for %s must be true, false, or null", path, string(name))
		}
	}
	return data, nil
}

// writeTrustFile writes the store with sorted keys and a trailing newline.
func writeTrustFile(path string, data trustFile) error {
	keys := make([]string, 0, len(data))
	for key, value := range data {
		if value != nil {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var builder strings.Builder
	builder.WriteString("{")
	for index, key := range keys {
		if index > 0 {
			builder.WriteString(",")
		}
		builder.WriteString("\n  ")
		name, err := ai.MarshalJSON(key)
		if err != nil {
			return err
		}
		builder.Write(name)
		builder.WriteString(": ")
		value, err := ai.MarshalJSON(*data[key])
		if err != nil {
			return err
		}
		builder.Write(value)
	}
	if len(keys) > 0 {
		builder.WriteString("\n")
	}
	builder.WriteString("}\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(builder.String()), 0o644)
}

// acquireTrustLockSync locks the trust file with the shared retry policy.
func acquireTrustLockSync(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	const maxAttempts = 10
	const delayMs = 20
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		release, err := acquireAuthLockDir(path)
		if err == nil {
			return release, nil
		}
		lastErr = err
		if attempt == maxAttempts {
			break
		}
		sleepMilliseconds(delayMs)
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("Failed to acquire trust store lock")
}

// HasTrustRequiringProjectResources reports whether cwd has project-local
// resources that must be gated by project trust.
func HasTrustRequiringProjectResources(cwd string) bool {
	homeDir := homeDir()
	if home := os.Getenv("HOME"); home != "" {
		homeDir = home
	}
	userAgentsSkillsDir := filepath.Join(CanonicalizePath(ResolvePath(homeDir, "", PathInputOptions{})), ".agents", "skills")
	currentDir := CanonicalizePath(ResolvePath(cwd, "", PathInputOptions{}))

	configDir := filepath.Join(currentDir, ConfigDirName)
	for _, entry := range trustRequiringProjectConfigResources {
		if PathExists(filepath.Join(configDir, entry)) {
			return true
		}
	}
	for {
		agentsSkillsDir := filepath.Join(currentDir, ".agents", "skills")
		if agentsSkillsDir != userAgentsSkillsDir && PathExists(agentsSkillsDir) {
			return true
		}
		parentDir := filepath.Dir(currentDir)
		if parentDir == currentDir {
			return false
		}
		currentDir = parentDir
	}
}

// ProjectTrustStore is the trust.json-backed store.
type ProjectTrustStore struct {
	trustPath string
}

// NewProjectTrustStore opens the store under the agent dir.
func NewProjectTrustStore(agentDir string) *ProjectTrustStore {
	return &ProjectTrustStore{trustPath: filepath.Join(ResolvePath(agentDir, "", PathInputOptions{}), "trust.json")}
}

// TrustPath is the store's file path.
func (s *ProjectTrustStore) TrustPath() string { return s.trustPath }

// Get returns the trust decision for a cwd (nil when unknown).
func (s *ProjectTrustStore) Get(cwd string) ProjectTrustDecision {
	entry := s.GetEntry(cwd)
	if entry == nil {
		return nil
	}
	return boolPointer(entry.Decision)
}

// GetEntry returns the nearest trust entry for a cwd.
func (s *ProjectTrustStore) GetEntry(cwd string) *ProjectTrustStoreEntry {
	release, err := acquireTrustLockSync(s.trustPath)
	if err != nil {
		return nil
	}
	defer release()
	data, err := readTrustFile(s.trustPath)
	if err != nil {
		return nil
	}
	return findNearestTrustEntry(data, cwd)
}

// Set records one decision.
func (s *ProjectTrustStore) Set(cwd string, decision ProjectTrustDecision) error {
	return s.SetMany([]ProjectTrustUpdate{{Path: cwd, Decision: decision}})
}

// SetMany records several decisions atomically.
func (s *ProjectTrustStore) SetMany(decisions []ProjectTrustUpdate) error {
	release, err := acquireTrustLockSync(s.trustPath)
	if err != nil {
		return err
	}
	defer release()
	data, err := readTrustFile(s.trustPath)
	if err != nil {
		return err
	}
	for _, update := range decisions {
		key := normalizeTrustCwd(update.Path)
		if update.Decision == nil {
			delete(data, key)
			continue
		}
		data[key] = boolPointer(*update.Decision)
	}
	return writeTrustFile(s.trustPath, data)
}
