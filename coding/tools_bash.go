package coding

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/dat267/gpi/agent"
	"github.com/dat267/gpi/ai"
)

// Port of core/tools/output-accumulator.ts and core/tools/bash.ts (the
// shell tool). Extension/extension-context plumbing is out of scope.

// OutputAccumulator incrementally tracks streaming output with bounded
// memory: a decoded tail for display snapshots, a temp file for the full
// output when the limits are crossed.
type OutputAccumulator struct {
	maxLines        int
	maxBytes        int
	maxRollingBytes int
	tempFilePrefix  string

	mu                       sync.Mutex
	tailText                 string
	tailBytes                int
	tailStartsAtLineBoundary bool
	totalRawBytes            int
	totalDecodedBytes        int
	completedLines           int
	totalLines               int
	currentLineBytes         int
	hasOpenLine              bool
	finished                 bool
	rawChunks                [][]byte
	tempFilePath             string
	tempFile                 *os.File
}

// NewOutputAccumulator builds an accumulator.
func NewOutputAccumulator(maxLines, maxBytes int, tempFilePrefix string) *OutputAccumulator {
	if maxLines == 0 {
		maxLines = DefaultMaxLines
	}
	if maxBytes == 0 {
		maxBytes = DefaultMaxBytes
	}
	if tempFilePrefix == "" {
		tempFilePrefix = "pi-output"
	}
	return &OutputAccumulator{
		maxLines: maxLines, maxBytes: maxBytes,
		maxRollingBytes: max(maxBytes*2, 1),
		tempFilePrefix:  tempFilePrefix,
		// Upstream seeds the boundary flag true (a fresh accumulator starts
		// at a line boundary).
		tailStartsAtLineBoundary: true,
	}
}

// Append decodes one output chunk.
func (o *OutputAccumulator) Append(data []byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished {
		return fmt.Errorf("Cannot append to a finished output accumulator")
	}
	o.totalRawBytes += len(data)
	o.appendDecodedText(string(data))

	if o.tempFile != nil || o.shouldUseTempFile() {
		if err := o.ensureTempFileLocked(); err != nil {
			return err
		}
		if _, err := o.tempFile.Write(data); err != nil {
			return err
		}
	} else if len(data) > 0 {
		o.rawChunks = append(o.rawChunks, append([]byte{}, data...))
	}
	return nil
}

// Finish flushes the streaming decoder state.
func (o *OutputAccumulator) Finish() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished {
		return
	}
	o.finished = true
	if o.shouldUseTempFile() {
		_ = o.ensureTempFileLocked()
	}
}

// OutputSnapshot is the current display state.
type OutputSnapshot struct {
	Content        string
	Truncation     TruncationResult
	FullOutputPath string
}

// Snapshot renders the current tail truncated to the limits.
func (o *OutputAccumulator) Snapshot(persistIfTruncated bool) OutputSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	tailTruncation := TruncateTail(o.getSnapshotText(), TruncationOptions{MaxLines: o.maxLines, MaxBytes: o.maxBytes})
	truncated := o.totalLines > o.maxLines || o.totalDecodedBytes > o.maxBytes
	truncatedBy := TruncatedBy("")
	if truncated {
		if tailTruncation.TruncatedBy != "" {
			truncatedBy = tailTruncation.TruncatedBy
		} else if o.totalDecodedBytes > o.maxBytes {
			truncatedBy = TruncatedByBytes
		} else {
			truncatedBy = TruncatedByLines
		}
	}
	truncation := tailTruncation
	truncation.Truncated = truncated
	truncation.TruncatedBy = truncatedBy
	truncation.TotalLines = o.totalLines
	truncation.TotalBytes = o.totalDecodedBytes
	truncation.MaxLines = o.maxLines
	truncation.MaxBytes = o.maxBytes

	if persistIfTruncated && truncation.Truncated {
		_ = o.ensureTempFileLocked()
	}
	return OutputSnapshot{
		Content: truncation.Content, Truncation: truncation, FullOutputPath: o.tempFilePath,
	}
}

// CloseTempFile closes the persisted temp file.
func (o *OutputAccumulator) CloseTempFile() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.tempFile == nil {
		return nil
	}
	file := o.tempFile
	o.tempFile = nil
	return file.Close()
}

// GetLastLineBytes reports the current (possibly open) line's size.
func (o *OutputAccumulator) GetLastLineBytes() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.currentLineBytes
}

func (o *OutputAccumulator) appendDecodedText(text string) {
	if len(text) == 0 {
		return
	}
	bytesCount := len(text)
	o.totalDecodedBytes += bytesCount
	o.tailText += text
	o.tailBytes += bytesCount
	if o.tailBytes > o.maxRollingBytes*2 {
		o.trimTailLocked()
	}

	newlines := 0
	lastNewline := -1
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			newlines++
			lastNewline = i
		}
	}
	if newlines == 0 {
		o.currentLineBytes += bytesCount
		o.hasOpenLine = true
	} else {
		o.completedLines += newlines
		tail := text[lastNewline+1:]
		o.currentLineBytes = len(tail)
		o.hasOpenLine = len(tail) > 0
	}
	o.totalLines = o.completedLines + boolToInt(o.hasOpenLine)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (o *OutputAccumulator) trimTailLocked() {
	if len(o.tailText) <= o.maxRollingBytes {
		o.tailBytes = len(o.tailText)
		return
	}
	start := len(o.tailText) - o.maxRollingBytes
	for start < len(o.tailText) && (o.tailText[start]&0xc0) == 0x80 {
		start++
	}
	o.tailStartsAtLineBoundary = start == 0 && o.tailStartsAtLineBoundary || (start > 0 && o.tailText[start-1] == '\n')
	o.tailText = o.tailText[start:]
	o.tailBytes = len(o.tailText)
}

func (o *OutputAccumulator) getSnapshotText() string {
	if o.tailStartsAtLineBoundary {
		return o.tailText
	}
	firstNewline := strings.Index(o.tailText, "\n")
	if firstNewline == -1 {
		return o.tailText
	}
	return o.tailText[firstNewline+1:]
}

func (o *OutputAccumulator) shouldUseTempFile() bool {
	return o.totalRawBytes > o.maxBytes || o.totalDecodedBytes > o.maxBytes || o.totalLines > o.maxLines
}

func (o *OutputAccumulator) ensureTempFileLocked() error {
	if o.tempFilePath != "" {
		return nil
	}
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		return err
	}
	path := filepath.Join(os.TempDir(), fmt.Sprintf("%s-%s.log", o.tempFilePrefix, hex.EncodeToString(id)))
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	o.tempFilePath = path
	o.tempFile = file
	for _, chunk := range o.rawChunks {
		if _, err := file.Write(chunk); err != nil {
			return err
		}
	}
	o.rawChunks = nil
	return nil
}

// atomicBool is a sized-down atomic.Bool alias for readability.
type atomicBool = atomic.Bool

// ShellToolConfig names a shell tool variant.
type ShellToolConfig struct {
	Name             string
	Label            string
	ShellName        string
	Prompt           string
	PromptSnippet    string
	PromptGuidelines []string
	TempFilePrefix   string
}

// BashToolOptions tune the shell tool.
type BashToolOptions struct {
	// CommandPrefix is prepended to every command.
	CommandPrefix string
	// ShellPath overrides the shell binary.
	ShellPath string
	// ExposeSessionEnvironment sets PI_* env vars. Nil/true = exposed
	// (upstream default); false strips them.
	ExposeSessionEnvironment *bool
	// SessionEnv carries the PI_* values to expose (host-provided).
	SessionEnv map[string]string
}

// BashToolDetails carries bash output details.
type BashToolDetails struct {
	Truncation     *TruncationResult `json:"truncation,omitempty"`
	FullOutputPath *string           `json:"fullOutputPath,omitempty"`
}

// BashToolDescription renders the shell tool description.
func BashToolDescription(shellName string) string {
	return fmt.Sprintf("Execute a %s command in the current working directory. Returns stdout and stderr. Output is truncated to last %d lines or %dKB (whichever is hit first). If truncated, full output is saved to a temp file. Optionally provide a timeout in seconds.",
		shellName, DefaultMaxLines, DefaultMaxBytes/1024)
}

var bashSchemaJSON = mustSchemaJSON(map[string]any{
	"type": "object",
	"properties": map[string]any{
		"command": map[string]any{"type": "string", "description": "Shell command to execute"},
		"timeout": numSchema("Timeout in seconds (optional, no default timeout)"),
	},
	"required": []string{"command"},
})

const (
	maxTimeoutMS      = 2_147_483_647
	maxTimeoutSeconds = maxTimeoutMS / 1000
)

// ResolveTimeoutSeconds validates a timeout in seconds (port of
// resolveTimeoutMs).
func ResolveTimeoutSeconds(timeout float64) (time.Duration, error) {
	if timeout <= 0 {
		return 0, fmt.Errorf("Invalid timeout: must be a finite number of seconds")
	}
	timeoutMS := timeout * 1000
	if timeoutMS > maxTimeoutMS {
		return 0, fmt.Errorf("Invalid timeout: maximum is %d seconds", maxTimeoutSeconds)
	}
	return time.Duration(timeoutMS * float64(time.Millisecond)), nil
}

// KillProcessTree kills the process group (SIGKILL), falling back to the
// single process (port of killProcessTree).
func KillProcessTree(pid int) {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// CreateBashTool builds the bash tool (port of createShellToolDefinition
// with the bash config).
func CreateBashTool(cwd string, options *BashToolOptions) agent.AgentTool {
	commandPrefix := ""
	exposeSessionEnv := true
	shellPath := ""
	var sessionEnv map[string]string
	if options != nil {
		commandPrefix = options.CommandPrefix
		if options.ExposeSessionEnvironment != nil {
			exposeSessionEnv = *options.ExposeSessionEnvironment
		}
		shellPath = options.ShellPath
		sessionEnv = options.SessionEnv
	}

	return agent.AgentTool{
		Name:        "bash",
		Description: BashToolDescription("bash"),
		Parameters:  bashSchemaJSON,
		Label:       "bash",
		Execute: func(toolCallID string, params json.RawMessage, ctx context.Context, onUpdate func(agent.AgentToolResult)) (agent.AgentToolResult, error) {
			var input struct {
				Command string   `json:"command"`
				Timeout *float64 `json:"timeout"`
			}
			if err := jsonUnmarshalStrictTool(params, &input); err != nil {
				return agent.AgentToolResult{}, err
			}
			command := input.Command
			if commandPrefix != "" {
				command = commandPrefix + "\n" + command
			}

			// Shell config: bash -c (stdin transport is legacy-WSL-only).
			shell := shellPath
			if shell == "" {
				resolved, err := exec.LookPath("bash")
				if err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("bash is not available on PATH")
				}
				shell = resolved
			}

			// Working directory must exist.
			if _, err := os.Stat(cwd); err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("Working directory does not exist: %s\nCannot execute bash commands.", cwd)
			}

			output := NewOutputAccumulator(0, 0, "pi-bash")
			var updateWG sync.WaitGroup

			emitOutputUpdate := func() {
				if onUpdate == nil {
					return
				}
				snapshot := output.Snapshot(true)
				details := map[string]any{}
				if snapshot.Truncation.Truncated {
					details["truncation"] = snapshot.Truncation
				}
				if snapshot.FullOutputPath != "" {
					details["fullOutputPath"] = snapshot.FullOutputPath
				}
				enc, _ := ai.MarshalJSON(details)
				update := agent.AgentToolResult{
					Content: []ai.Content{ai.TextContent{Text: snapshot.Content}},
					Details: enc,
				}
				updateWG.Add(1)
				go func() {
					defer updateWG.Done()
					onUpdate(update)
				}()
			}

			if onUpdate != nil {
				onUpdate(agent.AgentToolResult{Content: nil, Details: nil})
			}

			cmd := exec.Command(shell, "-c", command)
			cmd.Dir = cwd
			cmd.Env = shellEnv(sessionEnv, exposeSessionEnv)
			// Detached process group for tree kills.
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

			stdoutPipe, _ := cmd.StdoutPipe()
			stderrPipe, _ := cmd.StderrPipe()

			if err := cmd.Start(); err != nil {
				return agent.AgentToolResult{}, err
			}
			pgid := cmd.Process.Pid
			procDone := make(chan struct{})
			defer close(procDone)

			// Abort and timeout kill the whole tree (upstream killProcessTree).
			if ctx != nil {
				go func() {
					select {
					case <-ctx.Done():
						KillProcessTree(pgid)
					case <-procDone:
					}
				}()
			}
			var timedOut atomicBool
			if input.Timeout != nil {
				timeoutDur, terr := ResolveTimeoutSeconds(*input.Timeout)
				if terr != nil {
					KillProcessTree(pgid)
					return agent.AgentToolResult{}, terr
				}
				timed := time.AfterFunc(timeoutDur, func() {
					timedOut.Store(true)
					KillProcessTree(pgid)
				})
				defer timed.Stop()
			}

			drain := func(reader interface{ Read([]byte) (int, error) }) {
				buf := make([]byte, 64*1024)
				for {
					n, err := reader.Read(buf)
					if n > 0 {
						chunk := make([]byte, n)
						copy(chunk, buf[:n])
						_ = output.Append(chunk)
						emitOutputUpdate()
					}
					if err != nil {
						return
					}
				}
			}
			drainDone := make(chan struct{}, 2)
			go func() { drain(stdoutPipe); drainDone <- struct{}{} }()
			go func() { drain(stderrPipe); drainDone <- struct{}{} }()
			<-drainDone
			<-drainDone

			waitErr := cmd.Wait()
			updateWG.Wait()

			output.Finish()
			snapshot := output.Snapshot(true)
			defer output.CloseTempFile()

			formatOutput := func(emptyText string) (string, *BashToolDetails) {
				truncation := snapshot.Truncation
				text := snapshot.Content
				if text == "" {
					text = emptyText
				}
				var details *BashToolDetails
				if truncation.Truncated {
					fullPath := snapshot.FullOutputPath
					details = &BashToolDetails{Truncation: &truncation, FullOutputPath: &fullPath}
					startLine := truncation.TotalLines - truncation.OutputLines + 1
					endLine := truncation.TotalLines
					if truncation.LastLinePartial {
						lastLineSize := FormatSize(int64(output.GetLastLineBytes()))
						text += fmt.Sprintf("\n\n[Showing last %s of line %d (line is %s). Full output: %s]",
							FormatSize(int64(truncation.OutputBytes)), endLine, lastLineSize, snapshot.FullOutputPath)
					} else if truncation.TruncatedBy == TruncatedByLines {
						text += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Full output: %s]",
							startLine, endLine, truncation.TotalLines, snapshot.FullOutputPath)
					} else {
						text += fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%s limit). Full output: %s]",
							startLine, endLine, truncation.TotalLines, FormatSize(DefaultMaxBytes), snapshot.FullOutputPath)
					}
				}
				return text, details
			}

			appendStatus := func(text, status string) string {
				if text != "" {
					return text + "\n\n" + status
				}
				return status
			}

			if ctxErrOf(ctx) != nil {
				outputText, _ := formatOutput("")
				return agent.AgentToolResult{}, fmt.Errorf("%s", appendStatus(outputText, "Command aborted"))
			}
			if waitErr != nil {
				// Timed-out kills and signal kills surface as failures with the
				// collected output. Signal terminations use the 128+N shell
				// convention so callers never mistake them for success (the
				// upstream D68 note covers signals Node cannot name).
				outputText, _ := formatOutput("")
				if ws, ok := waitErr.(*exec.ExitError); ok {
					if status, ok := ws.Sys().(syscall.WaitStatus); ok && status.Signaled() {
						if timedOut.Load() {
							return agent.AgentToolResult{}, fmt.Errorf("%s", appendStatus(outputText, fmt.Sprintf("Command timed out after %g seconds", *input.Timeout)))
						}
						return agent.AgentToolResult{}, fmt.Errorf("%s", appendStatus(outputText, fmt.Sprintf("Command exited with code %d", 128+int(status.Signal()))))
					}
					return agent.AgentToolResult{}, fmt.Errorf("%s", appendStatus(outputText, fmt.Sprintf("Command exited with code %d", ws.ExitCode())))
				}
				return agent.AgentToolResult{}, fmt.Errorf("%s", appendStatus(outputText, waitErr.Error()))
			}

			outputText, details := formatOutput("(no output)")
			detailsJSON, _ := ai.MarshalJSON(details)
			_ = detailsJSON
			var resultDetails json.RawMessage
			if details != nil {
				resultDetails, _ = ai.MarshalJSON(*details)
			} else {
				resultDetails = json.RawMessage(`{}`)
			}
			return agent.AgentToolResult{
				Content: []ai.Content{ai.TextContent{Text: outputText}},
				Details: resultDetails,
			}, nil
		},
	}
}

// shellEnv assembles the child environment: the process env with PI_*
// session vars stripped, then optionally re-exposed (port of getShellEnv +
// resolveSpawnContext; pi's bin dir prepending is host-specific and unported).
func shellEnv(sessionEnv map[string]string, expose bool) []string {
	env := os.Environ()
	filtered := env[:0]
	for _, entry := range env {
		switch {
		case strings.HasPrefix(entry, "PI_SESSION_ID="),
			strings.HasPrefix(entry, "PI_SESSION_FILE="),
			strings.HasPrefix(entry, "PI_PROVIDER="),
			strings.HasPrefix(entry, "PI_MODEL="),
			strings.HasPrefix(entry, "PI_REASONING_LEVEL="):
			continue
		}
		filtered = append(filtered, entry)
	}
	if expose {
		for key, value := range sessionEnv {
			filtered = append(filtered, key+"="+value)
		}
	}
	return filtered
}
