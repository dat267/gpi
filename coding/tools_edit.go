package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/dat267/gpi/agent"
	"github.com/dat267/gpi/ai"
)

// Port of core/tools/edit.ts.

// EditToolDetails carries the edit's diff and patch.
type EditToolDetails struct {
	// Diff is the display-oriented diff of the changes made.
	Diff string `json:"diff"`
	// Patch is the standard unified patch.
	Patch string `json:"patch"`
	// FirstChangedLine is the first change's line number in the new file.
	FirstChangedLine *int `json:"firstChangedLine,omitempty"`
}

// EditToolDescription is upstream's edit description.
const EditToolDescription = "Edit a single file using exact text replacement. Every edits[].oldText must match a unique, non-overlapping region of the original file. If two changes affect the same block or nearby lines, merge them into one edit instead of emitting overlapping edits. Do not include large unchanged regions just to connect distant changes."

// EditToolSystemPromptContribution is the edit prompt contribution.
const EditToolSystemPromptContribution = "Make precise file edits with exact text replacement, including multiple disjoint edits in one call"

// EditToolSystemPromptGuidelines are the edit prompt guidelines.
var EditToolSystemPromptGuidelines = []string{
	"Use edit for precise changes (edits[].oldText must match exactly)",
	"When changing multiple separate locations in one file, use one edit call with multiple entries in edits[] instead of multiple edit calls",
	"Each edits[].oldText is matched against the original file, not after earlier edits are applied. Do not emit overlapping or nested edits. Merge nearby changes into one edit.",
	"Keep edits[].oldText as small as possible while still being unique in the file. Do not pad with large unchanged regions.",
}

// actual edit schema built once with the nested replaceEditSchema.
var editSchema = func() json.RawMessage {
	schema, err := ai.MarshalJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "Path to the file to edit (relative or absolute)"},
			"edits": map[string]any{
				"type":        "array",
				"description": "One or more targeted replacements. Each edit is matched against the original file, not incrementally. Do not include overlapping or nested edits. If two changes touch the same block or nearby lines, merge them into one edit instead.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"oldText": map[string]any{"type": "string", "description": "Exact text for one targeted replacement. It must be unique in the original file and must not overlap with any other edits[].oldText in the same call."},
						"newText": map[string]any{"type": "string", "description": "Replacement text for this targeted edit."},
					},
					"required": []string{"oldText", "newText"},
				},
			},
		},
		"required": []string{"path", "edits"},
	})
	if err != nil {
		panic(err)
	}
	return schema
}()

// PrepareEditArguments ports the edit tool's prepareArguments shim: JSON-
// string edits arrays, single-edit objects, and the legacy oldText/newText
// shape all normalize to the edits[] array.
func PrepareEditArguments(input json.RawMessage) json.RawMessage {
	var args map[string]json.RawMessage
	if err := jsonUnmarshalStrictTool(input, &args); err != nil {
		return input
	}

	// Some models (Opus 4.6, GLM-5.1) send edits as a JSON string instead of
	// an array; others send a single edit object instead of a one-element
	// array.
	if raw, ok := args["edits"]; ok {
		var asString string
		if jsonUnmarshalStrictTool(raw, &asString) == nil {
			var parsed []json.RawMessage
			if err := jsonUnmarshalStrictTool(json.RawMessage(asString), &parsed); err != nil {
				var single struct {
					OldText string `json:"oldText"`
					NewText string `json:"newText"`
				}
				if jsonUnmarshalStrictTool(json.RawMessage(asString), &single) == nil && single.OldText != "" || (single.NewText != "" && single.OldText != "") {
					args["edits"] = mustMarshalJSON([]json.RawMessage{json.RawMessage(asString)})
				}
			} else {
				args["edits"] = json.RawMessage(asString)
			}
		} else {
			// Single edit object?
			var single struct {
				OldText *string `json:"oldText"`
				NewText *string `json:"newText"`
			}
			if jsonUnmarshalStrictTool(raw, &single) == nil && single.OldText != nil && single.NewText != nil {
				args["edits"] = mustMarshalJSON([]json.RawMessage{raw})
			}
		}
	}

	// Legacy flat shape: oldText/newText at the top level.
	var legacy struct {
		OldText *string `json:"oldText"`
		NewText *string `json:"newText"`
	}
	if err := jsonUnmarshalStrictTool(input, &legacy); err != nil || legacy.OldText == nil || legacy.NewText == nil {
		return mustMarshalJSON(args)
	}
	edits := []json.RawMessage{}
	if raw, ok := args["edits"]; ok {
		var list []json.RawMessage
		if jsonUnmarshalStrictTool(raw, &list) == nil {
			edits = append(edits, list...)
		}
	}
	edits = append(edits, mustMarshalJSON(map[string]string{
		"oldText": *legacy.OldText, "newText": *legacy.NewText,
	}))
	args["edits"] = mustMarshalJSON(edits)
	delete(args, "oldText")
	delete(args, "newText")
	return mustMarshalJSON(args)
}

// CreateEditTool builds the edit tool.
func CreateEditTool(cwd string) agent.AgentTool {
	return agent.AgentTool{
		Name:        "edit",
		Description: EditToolDescription,
		Parameters:  editSchema,
		Label:       "edit",
		PrepareArguments: func(args json.RawMessage) json.RawMessage {
			return PrepareEditArguments(args)
		},
		Execute: func(toolCallID string, params json.RawMessage, ctx context.Context, onUpdate func(agent.AgentToolResult)) (agent.AgentToolResult, error) {
			var input struct {
				Path  string `json:"path"`
				Edits []Edit `json:"edits"`
			}
			if err := jsonUnmarshalStrictTool(params, &input); err != nil {
				return agent.AgentToolResult{}, err
			}
			if len(input.Edits) == 0 {
				return agent.AgentToolResult{}, fmt.Errorf("Edit tool input is invalid. edits must contain at least one replacement.")
			}
			absolutePath := ResolveToCwd(input.Path, cwd)

			var resultValue agent.AgentToolResult
			var resultErr error
			err := WithFileMutationQueue(absolutePath, func() error {
				if ctxErrOf(ctx) != nil {
					resultErr = fmt.Errorf("Operation aborted")
					return resultErr
				}

				// Check the file exists (access check).
				if _, err := os.Stat(absolutePath); err != nil {
					resultErr = fmt.Errorf("Could not edit file: %s. Error code: ENOENT.", input.Path)
					return resultErr
				}
				if ctxErrOf(ctx) != nil {
					resultErr = fmt.Errorf("Operation aborted")
					return resultErr
				}

				data, err := os.ReadFile(absolutePath)
				if err != nil {
					resultErr = err
					return err
				}
				rawContent := string(data)
				// Strip the BOM before matching: the model will not include an
				// invisible BOM in oldText.
				bom, content := SplitBom(rawContent)
				originalEnding := DetectLineEnding(content)
				normalizedContent := NormalizeToLF(content)
				applied, err := ApplyEditsToNormalizedContent(normalizedContent, input.Edits, input.Path)
				if err != nil {
					resultErr = err
					return err
				}
				if ctxErrOf(ctx) != nil {
					resultErr = fmt.Errorf("Operation aborted")
					return resultErr
				}

				finalContent := bom + RestoreLineEndings(applied.NewContent, originalEnding)
				if err := os.WriteFile(absolutePath, []byte(finalContent), 0o644); err != nil {
					resultErr = err
					return err
				}
				if ctxErrOf(ctx) != nil {
					resultErr = fmt.Errorf("Operation aborted")
					return resultErr
				}

				diff, firstChangedLine, _ := GenerateDiffString(applied.BaseContent, applied.NewContent, 4)
				patch := GenerateUnifiedPatch(input.Path, applied.BaseContent, applied.NewContent, 4)
				details := EditToolDetails{Diff: diff, Patch: patch}
				if firstChangedLine >= 0 {
					details.FirstChangedLine = &firstChangedLine
				}
				detailsJSON, _ := ai.MarshalJSON(details)
				resultValue = agent.AgentToolResult{
					Content: []ai.Content{ai.TextContent{Text: fmt.Sprintf("Successfully replaced %d block(s) in %s.", len(input.Edits), input.Path)}},
					Details: detailsJSON,
				}
				return nil
			})
			if resultErr != nil {
				return agent.AgentToolResult{}, resultErr
			}
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			return resultValue, nil
		},
	}
}
