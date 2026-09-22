package interactive

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

type editRenderArgs struct {
	Path    string
	HasPath bool
	Edits   []coding.Edit
}

// getRenderablePreviewInput extracts the path/edits pair that can be previewed.
func getRenderablePreviewInput(args any) *editRenderArgs {
	decoded := toolArgs(args)
	if decoded == nil {
		return nil
	}
	path, hasPath := argString(decoded, "path", "file_path")
	if !hasPath || path == "" {
		return nil
	}
	edits := parseEditArgs(decoded)
	if len(edits) == 0 {
		return nil
	}
	return &editRenderArgs{Path: path, HasPath: hasPath, Edits: edits}
}

func parseEditArgs(decoded map[string]any) []coding.Edit {
	var edits []coding.Edit
	if rawList, ok := decoded["edits"].([]any); ok && len(rawList) > 0 {
		for _, entry := range rawList {
			editMap, ok := entry.(map[string]any)
			if !ok {
				return nil
			}
			oldText, oldOK := editMap["oldText"].(string)
			newText, newOK := editMap["newText"].(string)
			if !oldOK || !newOK {
				return nil
			}
			edits = append(edits, coding.Edit{OldText: oldText, NewText: newText})
		}
		return edits
	}
	oldText, oldOK := decoded["oldText"].(string)
	newText, newOK := decoded["newText"].(string)
	if oldOK && newOK {
		return []coding.Edit{{OldText: oldText, NewText: newText}}
	}
	return nil
}

func formatEditCall(args *editRenderArgs, theme *Theme, cwd string) string {
	path := ""
	if args != nil {
		path = args.Path
	}
	pathDisplay := RenderToolPath(&path, theme, cwd, "")
	return theme.Fg("toolTitle", theme.Bold("edit")) + " " + pathDisplay
}

func getEditHeaderBg(preview *editPreview, settledError bool, theme *Theme) func(string) string {
	if preview != nil {
		if preview.Error != "" {
			return func(text string) string { return theme.Bg("toolErrorBg", text) }
		}
		return func(text string) string { return theme.Bg("toolSuccessBg", text) }
	}
	if settledError {
		return func(text string) string { return theme.Bg("toolErrorBg", text) }
	}
	return func(text string) string { return theme.Bg("toolPendingBg", text) }
}

type editPreview struct {
	Diff             string
	FirstChangedLine *int
	Error            string
}

// editPreviewState carries the async diff preview together with the args key
// it belongs to, so one atomic swap publishes both (stage 4: the worker and the
// renderer share state without a lock).
type editPreviewState struct {
	argsKey string
	preview *editPreview
}

type editCallComponent struct {
	*tui.Box
	// preview is swapped atomically by the preview worker; previewPending is the
	// claim flag that stops a second worker from starting.
	preview        atomic.Pointer[editPreviewState]
	previewPending atomic.Bool
	settledError   bool
	builtArgsKey   string
}

// setPreviewResult stores the async diff preview (worker side).
func (c *editCallComponent) setPreviewResult(requestKey string, preview *editPreview) {
	current := c.preview.Load()
	if current == nil || current.argsKey != requestKey {
		return
	}
	c.preview.Store(&editPreviewState{argsKey: requestKey, preview: preview})
	c.previewPending.Store(false)
}

// snapshotPreview returns a copy of the published preview, or nil.
func (c *editCallComponent) snapshotPreview() *editPreview {
	state := c.preview.Load()
	if state == nil || state.preview == nil {
		return nil
	}
	snapshot := *state.preview
	return &snapshot
}

func (c *editCallComponent) build(args *editRenderArgs, theme *Theme, cwd string) {
	c.SetBgFn(getEditHeaderBg(c.snapshotPreview(), c.settledError, theme))
	c.Clear()
	c.AddChild(tui.NewText(formatEditCall(args, theme, cwd), 0, 0, nil))
	preview := c.snapshotPreview()
	if preview == nil {
		return
	}
	body := ""
	if preview.Error != "" {
		body = theme.Fg("error", preview.Error)
	} else {
		body = RenderDiff(preview.Diff, RenderDiffOptions{})
	}
	c.AddChild(tui.NewSpacer(1))
	c.AddChild(tui.NewText(body, 0, 0, nil))
}

var editRenderers = ToolRenderers{
	// Upstream 0.86.1 renders the edit block through the self shell: the
	// block is a single Box(1,1) (one space horizontal padding, one blank
	// line vertical padding), not nested inside the tool content box.
	RenderShell: "self",
	RenderCall: func(args any, theme *Theme, context *ToolRenderContext) tui.Component {
		var component *editCallComponent
		if box, ok := context.LastComponent.(*editCallComponent); ok {
			component = box
		} else if box, ok := context.LastComponent.(*tui.Box); ok {
			component = &editCallComponent{Box: box}
		} else if state, ok := context.State.(*editCallComponent); ok {
			component = state
		} else {
			component = &editCallComponent{Box: tui.NewBox(1, 1, nil)}
			context.State = component
		}
		previewInput := getRenderablePreviewInput(args)
		argsKey := argsKeyFor(previewInput)
		current := component.preview.Load()
		if current == nil || current.argsKey != argsKey {
			component.preview.Store(nil)
			component.preview.Store(&editPreviewState{argsKey: argsKey})
			component.previewPending.Store(false)
			component.settledError = false
		}
		startPreview := context.ArgsComplete && previewInput != nil && component.preview.Load().preview == nil &&
			!component.previewPending.Load()
		if startPreview {
			component.previewPending.Store(true)
		}
		if startPreview {
			request := previewInput
			requestKey := argsKey
			cwd := context.Cwd
			invalidate := context.Invalidate
			go func() {
				preview := computeEditsPreview(request.Path, request.Edits, cwd)
				component.setPreviewResult(requestKey, preview)
				if invalidate != nil {
					invalidate()
				}
			}()
		}
		component.build(previewInput, theme, context.Cwd)
		return component
	},
	RenderResult: func(result *SortToolResultContent, options ToolRenderResultOptions, theme *Theme, context *ToolRenderContext) tui.Component {
		callComponent, _ := context.State.(*editCallComponent)
		previewInput := getRenderablePreviewInput(context.Args)
		argsKey := argsKeyFor(previewInput)
		var resultDiff string
		var firstChangedLine *int
		if !context.IsError {
			if details := toolDetailsFrom[coding.EditToolDetails](result.Details); details != nil {
				resultDiff = details.Diff
				firstChangedLine = details.FirstChangedLine
			}
		}
		if callComponent != nil {
			changed := false
			if resultDiff != "" {
				callComponent.preview.Store(&editPreviewState{
					argsKey: argsKey,
					preview: &editPreview{Diff: resultDiff, FirstChangedLine: firstChangedLine},
				})
				callComponent.previewPending.Store(false)
				changed = true
			}
			if callComponent.settledError != context.IsError {
				callComponent.settledError = context.IsError
				changed = true
			}
			if changed {
				callComponent.build(previewInput, theme, context.Cwd)
			}
		}
		output := formatEditResult(previewInput, callComponent, result, theme, context.IsError)
		component := toolContainerComponent(context)
		component.Clear()
		if output == "" {
			return component
		}
		component.AddChild(tui.NewSpacer(1))
		component.AddChild(tui.NewText(output, 1, 0, nil))
		return component
	},
}

func formatEditResult(args *editRenderArgs, callComponent *editCallComponent, result *SortToolResultContent,
	theme *Theme, isError bool) string {
	rawPath := ""
	if args != nil {
		rawPath = args.Path
	}
	var previewDiff string
	var previewError string
	if callComponent != nil {
		if preview := callComponent.snapshotPreview(); preview != nil {
			previewDiff = preview.Diff
			previewError = preview.Error
		}
	}
	if isError {
		var blocks []string
		for _, block := range result.Content {
			if block.Type == "text" && block.Text != "" {
				blocks = append(blocks, block.Text)
			}
		}
		errorText := strings.Join(blocks, "\n")
		if errorText == "" || errorText == previewError {
			return ""
		}
		return theme.Fg("error", errorText)
	}
	var resultDiff string
	if details := toolDetailsFrom[coding.EditToolDetails](result.Details); details != nil {
		resultDiff = details.Diff
	}
	if resultDiff != "" && resultDiff != previewDiff {
		return RenderDiff(resultDiff, RenderDiffOptions{FilePath: rawPath})
	}
	return ""
}

// computeEditsPreview mirrors computeEditsDiff: read the file, apply the edits
// to the normalized content, and diff (Go is synchronous).
func computeEditsPreview(path string, edits []coding.Edit, cwd string) *editPreview {
	absolutePath := coding.ResolveToCwd(path, cwd)
	rawContent, err := os.ReadFile(absolutePath)
	if err != nil {
		return &editPreview{Error: fmt.Sprintf("Could not edit file: %s. %v.", path, err)}
	}
	_, content := coding.SplitBom(string(rawContent))
	normalizedContent := coding.NormalizeToLF(content)
	result, err := coding.ApplyEditsToNormalizedContent(normalizedContent, edits, path)
	if err != nil {
		return &editPreview{Error: err.Error()}
	}
	diff, firstChangedLine, ok := coding.GenerateDiffString(result.BaseContent, result.NewContent, 3)
	if !ok {
		return &editPreview{Diff: "", FirstChangedLine: nil}
	}
	return &editPreview{Diff: diff, FirstChangedLine: &firstChangedLine}
}

// --- registry ---------------------------------------------------------------

func argsKeyFor(args *editRenderArgs) string {
	if args == nil {
		return ""
	}
	encoded, err := json.Marshal(map[string]any{"path": args.Path, "edits": args.Edits})
	if err != nil {
		return ""
	}
	return string(encoded)
}
