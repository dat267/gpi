package interactive

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dat267/gpi/coding"
	"github.com/dat267/gpi/tui"
)

// Port of src/modes/interactive/session-share.ts and the orchestration of
// src/modes/interactive/bug-report.ts.
//
// Divergences: the upload/gist/dialog collaborators are injected (D128); the
// extension dialogs are the ported components (D41 seams for the extension
// runner itself).

// ShareSession is the session surface the share flow needs.
type ShareSession interface {
	GetState() ShareState
	ExportToHTML(outputPath string) (string, error)
	ModelRuntime() *coding.ModelRuntime
}

// ShareState is the session state the share entry needs.
type ShareState struct {
	SystemPrompt string
	Tools        []ShareTool
}

// ShareTool is a tool schema for the share entry.
type ShareTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ShareContext carries the share collaborators.
type ShareContext struct {
	Session         ShareSession
	UI              tui.TUI
	EditorContainer *tui.Container
	Editor          tui.Component
	ShowStatus      func(message string)
	ShowError       func(message string)

	// RadiusToken resolves the Radius credential ("" = unavailable).
	RadiusToken func(ctx context.Context) string
	// RadiusGateway is the Radius gateway URL.
	RadiusGateway string
	// UploadArtifact uploads the JSONL and returns (url, error).
	UploadArtifact func(ctx context.Context, body []byte, token string, gateway string) (string, error)
	// CheckGitHubAuth reports whether `gh auth status` succeeds.
	CheckGitHubAuth func() bool
	// CreateGist creates a private gist and returns (gistURL, error).
	CreateGist func(ctx context.Context, filePath string) (string, error)
	// ShareViewerURL builds the viewer URL for a gist id.
	ShareViewerURL func(gistID string) string
	// TempDir overrides the temporary directory (test seam).
	TempDir string
}

// CreateShareTrailingEntries builds the `pi.share` trailing entry.
func CreateShareTrailingEntries(session ShareSession, parentID *string, timestamp string) []*coding.SessionEntry {
	state := session.GetState()
	tools := make([]map[string]any, 0, len(state.Tools))
	for _, tool := range state.Tools {
		entry := map[string]any{"name": tool.Name, "description": tool.Description}
		if len(tool.Parameters) > 0 {
			var parameters any
			if err := json.Unmarshal(tool.Parameters, &parameters); err == nil {
				entry["parameters"] = parameters
			}
		}
		tools = append(tools, entry)
	}
	data, _ := json.Marshal(map[string]any{"systemPrompt": state.SystemPrompt, "tools": tools})
	entry := &coding.SessionEntry{Type: "custom", CustomType: "pi.share"}
	entry.ID = randomShortID()
	entry.ParentID = parentID
	entry.Timestamp = timestamp
	entry.Data = data
	return []*coding.SessionEntry{entry}
}

// ExportSessionForShare exports the branch with the share metadata.
func ExportSessionForShare(filePath string, session ShareSession, sessionManager *coding.SessionManager) error {
	content := coding.SerializeSessionBranch(sessionManager, func(parentID *string, timestamp string) []*coding.SessionEntry {
		return CreateShareTrailingEntries(session, parentID, timestamp)
	})
	return os.WriteFile(filePath, []byte(content), 0o644)
}

// ShareSessionThroughRadius shares the session through Radius, falling back to
// a private gist.
func ShareSessionThroughRadius(ctx context.Context, share ShareContext) {
	tempDir := share.TempDir
	if tempDir == "" {
		created, err := os.MkdirTemp("", "pi-share-")
		if err != nil {
			share.ShowError("Failed to export session: " + err.Error())
			return
		}
		tempDir = created
	}
	defer os.RemoveAll(tempDir)

	jsonlFile := filepath.Join(tempDir, "session.jsonl")
	htmlFile := filepath.Join(tempDir, "session.html")

	if share.Session == nil {
		return
	}
	if sessionManager := shareSessionManager(share.Session); sessionManager != nil {
		if err := ExportSessionForShare(jsonlFile, share.Session, sessionManager); err != nil {
			share.ShowError("Failed to export session: " + err.Error())
			return
		}
	}
	if tryShareViaRadius(ctx, jsonlFile, share) {
		return
	}

	if share.CheckGitHubAuth != nil && !share.CheckGitHubAuth() {
		share.ShowError("GitHub CLI is not logged in. Run 'gh auth login' first.")
		return
	}
	if _, err := share.Session.ExportToHTML(htmlFile); err != nil {
		share.ShowError("Failed to export session: " + err.Error())
		return
	}
	shareViaGist(ctx, htmlFile, share)
}

func tryShareViaRadius(ctx context.Context, tmpFile string, share ShareContext) bool {
	if share.RadiusToken == nil {
		return false
	}
	token := share.RadiusToken(ctx)
	if token == "" {
		return false
	}
	loader := NewBorderedLoader(share.UI, ActiveTheme(), "Uploading to Radius...", nil)
	showShareLoader(share, loader)
	loader.SetOnAbort(func() {
		restoreShareEditor(loader, share)
		share.ShowStatus("Share cancelled")
	})

	body, err := os.ReadFile(tmpFile)
	if err != nil {
		restoreShareEditor(loader, share)
		share.ShowError("Failed to upload Radius artifact: " + err.Error())
		return true
	}
	if share.UploadArtifact == nil {
		return false
	}
	url, err := share.UploadArtifact(ctx, body, token, share.RadiusGateway)
	if loaderAborted(loader) {
		return true
	}
	restoreShareEditor(loader, share)
	if err != nil {
		share.ShowError("Failed to upload Radius artifact: " + err.Error())
		return true
	}
	share.ShowStatus("Share URL: " + tui.Hyperlink(url, url))
	return true
}

func shareViaGist(ctx context.Context, tmpFile string, share ShareContext) {
	loader := NewBorderedLoader(share.UI, ActiveTheme(), "Creating gist...", nil)
	showShareLoader(share, loader)
	loader.SetOnAbort(func() {
		restoreShareEditor(loader, share)
		share.ShowStatus("Share cancelled")
	})

	if share.CreateGist == nil {
		restoreShareEditor(loader, share)
		return
	}
	gistURL, err := share.CreateGist(ctx, tmpFile)
	if loaderAborted(loader) {
		return
	}
	restoreShareEditor(loader, share)
	if err != nil {
		share.ShowError("Failed to create gist: " + err.Error())
		return
	}
	gistID := gistURL
	if index := strings.LastIndex(gistURL, "/"); index >= 0 {
		gistID = gistURL[index+1:]
	}
	if gistID == "" {
		share.ShowError("Failed to parse gist ID from gh output")
		return
	}
	previewURL := gistURL
	if share.ShareViewerURL != nil {
		previewURL = share.ShareViewerURL(gistID)
	}
	share.ShowStatus("Share URL: " + tui.Hyperlink(previewURL, previewURL) +
		"\nGist: " + tui.Hyperlink(gistURL, gistURL))
}

func showShareLoader(share ShareContext, loader *BorderedLoader) {
	if share.EditorContainer == nil {
		return
	}
	share.EditorContainer.Clear()
	share.EditorContainer.AddChild(loader)
	if share.UI != nil {
		share.UI.SetFocus(loader)
		share.UI.RequestRender(false)
	}
}

func restoreShareEditor(loader *BorderedLoader, share ShareContext) {
	loader.Dispose()
	if share.EditorContainer == nil {
		return
	}
	share.EditorContainer.Clear()
	if share.Editor != nil {
		share.EditorContainer.AddChild(share.Editor)
	}
	if share.UI != nil && share.Editor != nil {
		share.UI.SetFocus(share.Editor)
	}
}

// BugReportOptions are the /bug flow choices.
type BugReportOptions struct {
	Hint           string
	IncludeSession bool
	IncludeSummary bool
	Delivery       string // "upload" | "zip"
}

// BugReportWiring orchestrates the /bug flow.
type BugReportWiring struct {
	Session         BugReportSession
	UI              tui.TUI
	EditorContainer *tui.Container
	Editor          tui.Component
	Keybindings     *tui.KeybindingsManager

	ShowStatus    func(message string)
	ShowError     func(message string)
	RequestRender func()

	// PromptForOptions collects the consent choices.
	PromptForOptions func(ctx context.Context, hint string) (*BugReportOptions, bool)
	// Summarize writes the report summary.
	Summarize func(ctx context.Context, hint string) (string, error)
	// Upload uploads the bundle ("" error = success).
	Upload func(ctx context.Context, bundle coding.BugReportBundle) (string, bool)
	// Choose asks the user to pick an option.
	Choose func(ctx context.Context, title string, options []string, message string) (string, bool)
	// ExportZip writes the bundle archive.
	ExportZip func(ctx context.Context, bundle coding.BugReportBundle)
	// BuildBundle assembles the bundle.
	BuildBundle func(options BugReportOptions, summary string) coding.BugReportBundle
}

// BugReportSession is the session surface the bug report needs.
type BugReportSession interface {
	SummarizeForBugReport(ctx context.Context, hint string) (string, error)
	ModelName() string
}

// ReportBug runs the /bug flow.
func (w *BugReportWiring) ReportBug(ctx context.Context, initialHint string) {
	if w.PromptForOptions == nil {
		return
	}
	options, ok := w.PromptForOptions(ctx, initialHint)
	if !ok || options == nil {
		w.ShowStatus("Bug report cancelled")
		return
	}

	summary := ""
	if options.IncludeSummary {
		loader := NewBorderedLoader(w.UI, ActiveTheme(),
			"Writing summary with "+w.modelName()+"...", nil)
		w.showLoader(loader)
		if w.Summarize != nil {
			written, err := w.Summarize(ctx, options.Hint)
			if err != nil {
				w.restoreEditor(loader)
				if loaderAborted(loader) {
					w.ShowStatus("Bug report cancelled")
				} else {
					w.ShowError("Failed to write bug report summary: " + err.Error())
				}
				return
			}
			summary = written
		}
		w.restoreEditor(loader)
		if loaderAborted(loader) {
			w.ShowStatus("Bug report cancelled")
			return
		}
	}

	bundle := coding.BugReportBundle{}
	if w.BuildBundle != nil {
		bundle = w.BuildBundle(*options, summary)
	}

	if options.Delivery == "upload" {
		failure, uploaded := "", false
		if w.Upload != nil {
			failure, uploaded = w.Upload(ctx, bundle)
		}
		if uploaded {
			return
		}
		fallback := ""
		if w.Choose != nil {
			choice, ok := w.Choose(ctx, "Upload failed",
				[]string{"Export as Zip", "Cancel"},
				failure+"\n\nExport the report as a zip archive instead?")
			if ok {
				fallback = choice
			}
		}
		if fallback != "Export as Zip" {
			w.ShowStatus("Bug report cancelled")
			return
		}
	}
	if w.ExportZip != nil {
		w.ExportZip(ctx, bundle)
	}
}

func (w *BugReportWiring) modelName() string {
	if w.Session == nil {
		return "the current model"
	}
	if name := w.Session.ModelName(); name != "" {
		return name
	}
	return "the current model"
}

func (w *BugReportWiring) showLoader(loader *BorderedLoader) {
	if w.EditorContainer == nil {
		return
	}
	w.EditorContainer.Clear()
	w.EditorContainer.AddChild(loader)
	if w.UI != nil {
		w.UI.SetFocus(loader)
		w.UI.RequestRender(false)
	}
}

func (w *BugReportWiring) restoreEditor(loader *BorderedLoader) {
	loader.Dispose()
	if w.EditorContainer == nil {
		return
	}
	w.EditorContainer.Clear()
	if w.Editor != nil {
		w.EditorContainer.AddChild(w.Editor)
	}
	if w.UI != nil && w.Editor != nil {
		w.UI.SetFocus(w.Editor)
	}
}

// loaderAborted reports whether the loader's abort channel is closed.
func loaderAborted(loader *BorderedLoader) bool {
	if loader == nil {
		return false
	}
	done := loader.Done()
	if done == nil {
		return false
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// randomShortID returns an 8-character random identifier.
func randomShortID() string {
	const alphabet = "0123456789abcdef"
	var builder strings.Builder
	seed := time.Now().UnixNano()
	for i := 0; i < 8; i++ {
		seed = seed*6364136223846793005 + 1442695040888963407
		builder.WriteByte(alphabet[(seed>>33)&0xf])
	}
	return builder.String()
}

// shareSessionManager extracts the session manager from a ShareSession when it
// exposes one.
func shareSessionManager(session ShareSession) *coding.SessionManager {
	if provider, ok := session.(interface{ SessionManager() *coding.SessionManager }); ok {
		return provider.SessionManager()
	}
	return nil
}
