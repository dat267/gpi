package coding

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Port of core/bash-executor.ts: bash command execution with streaming output,
// cancellation, rolling buffers, and temp-file persistence for truncated
// output. Also ports the local shell operations from core/tools/bash.ts
// (createLocalShellOperations / createLocalBashOperations).

// BashOperations is the pluggable execution backend. Override it to delegate
// command execution to remote systems (for example SSH).
type BashOperations interface {
	// Exec runs command in cwd, streaming output through options.OnData.
	// Implementations should report signal terminations as 128+signal; a nil
	// exit code is treated as a failed command.
	Exec(ctx context.Context, command, cwd string, options BashExecOptions) (*int, error)
}

// BashExecOptions are per-call execution options.
type BashExecOptions struct {
	// OnData receives output chunks as they arrive.
	OnData func(data []byte)
	// Signal cancels the command when closed.
	Signal <-chan struct{}
	// Timeout is the optional command timeout.
	Timeout *time.Duration
	// Env overrides the child environment when non-nil.
	Env []string
}

// BashExecutorOptions configure ExecuteBashWithOperations.
type BashExecutorOptions struct {
	// OnChunk receives sanitized output chunks.
	OnChunk func(chunk string)
	// Signal cancels the command.
	Signal <-chan struct{}
}

// BashResult is the outcome of a bash execution.
type BashResult struct {
	// Output is the combined sanitized (possibly truncated) output.
	Output string
	// ExitCode is the process exit code; nil when killed or cancelled.
	ExitCode *int
	// Cancelled reports cancellation via the signal.
	Cancelled bool
	// Truncated reports whether the output exceeded the truncation threshold.
	Truncated bool
	// FullOutputPath is the temp file holding the full output, when written.
	FullOutputPath string
}

// ExecuteBashWithOperations runs a bash command using custom operations (used
// for remote execution: SSH, containers, and so on).
func ExecuteBashWithOperations(ctx context.Context, command, cwd string, operations BashOperations, options *BashExecutorOptions) (BashResult, error) {
	if operations == nil {
		return BashResult{}, errors.New("bash operations are required")
	}
	if options == nil {
		options = &BashExecutorOptions{}
	}
	var outputChunks []string
	outputBytes := 0
	maxOutputBytes := DefaultMaxBytes * 2

	var tempFilePath string
	var tempFile *os.File
	var tempErr error
	// pendingChunks holds the buffered chunks that still need to be flushed to
	// the temp file once it is created.
	flushedChunks := 0
	var mu sync.Mutex

	ensureTempFile := func() error {
		if tempFilePath != "" {
			return nil
		}
		id := tempFileID()
		tempFilePath = filepath.Join(os.TempDir(), fmt.Sprintf("pi-bash-%s.log", id))
		file, err := os.Create(tempFilePath)
		if err != nil {
			tempFilePath = ""
			return err
		}
		tempFile = file
		flushedChunks = 0
		return nil
	}

	onData := func(data []byte) {
		mu.Lock()
		defer mu.Unlock()
		// Sanitize: strip ANSI, replace binary garbage, normalize newlines.
		text := strings.ReplaceAll(SanitizeBinaryOutput(StripAnsi(string(data))), "\r", "")

		outputChunks = append(outputChunks, text)
		outputBytes += len(text)

		// Start writing to the temp file once the threshold is crossed.
		if tempFilePath == "" && outputBytes > DefaultMaxBytes {
			if err := ensureTempFile(); err != nil && tempErr == nil {
				tempErr = err
			}
		}
		if tempFile != nil {
			for _, chunk := range outputChunks[flushedChunks:] {
				if _, err := tempFile.WriteString(chunk); err != nil && tempErr == nil {
					tempErr = err
				}
				flushedChunks++
			}
		}

		// Keep a rolling buffer.
		for outputBytes > maxOutputBytes && len(outputChunks) > 1 {
			removed := outputChunks[0]
			outputChunks = outputChunks[1:]
			outputBytes -= len(removed)
			if flushedChunks > 0 {
				flushedChunks--
			}
		}

		// Stream to the callback.
		if options.OnChunk != nil {
			options.OnChunk(text)
		}
	}

	finishTempFile := func() {
		if tempFile != nil {
			_ = tempFile.Sync()
			_ = tempFile.Close()
		}
	}

	// joinOutput folds the rolling buffer.
	joinOutput := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(outputChunks, "")
	}

	finish := func(cancelled bool, exitCode *int) BashResult {
		fullOutput := joinOutput()
		truncation := TruncateTail(fullOutput, TruncationOptions{})
		if truncation.Truncated && tempFilePath == "" {
			_ = ensureTempFile()
			finishTempFile()
		} else {
			finishTempFile()
		}
		output := fullOutput
		if truncation.Truncated {
			output = truncation.Content
		}
		code := exitCode
		if cancelled {
			code = nil
		}
		return BashResult{
			Output:         output,
			ExitCode:       code,
			Cancelled:      cancelled,
			Truncated:      truncation.Truncated,
			FullOutputPath: tempFilePath,
		}
	}

	execOptions := BashExecOptions{OnData: onData, Signal: options.Signal}
	code, err := operations.Exec(ctx, command, cwd, execOptions)
	cancelled := signalClosed(options.Signal)
	if err != nil {
		if cancelled {
			return finish(true, nil), nil
		}
		finishTempFile()
		return BashResult{}, err
	}
	return finish(cancelled, code), nil
}

// ExecuteBash runs a bash command with the built-in local shell backend.
func ExecuteBash(ctx context.Context, command, cwd string, options *BashExecutorOptions) (BashResult, error) {
	return ExecuteBashWithOperations(ctx, command, cwd, CreateLocalBashOperations("", nil), options)
}

// LocalShellOptions configure the local shell operations.
type LocalShellOptions struct {
	// ShellName is used in the working-directory error message.
	ShellName string
	// ShellPath overrides the resolved shell.
	ShellPath string
	// Env overrides the child environment when non-nil.
	Env []string
	// Config, when non-nil, supplies the shell configuration directly.
	Config *ShellConfig
	// OnSpawn records the child pid for shutdown tracking.
	OnSpawn func(pid int)
	// OnExit releases the tracked child pid.
	OnExit func(pid int)
}

// CreateLocalBashOperations builds bash operations on pi's built-in local shell
// backend (upstream createLocalBashOperations).
func CreateLocalBashOperations(shellPath string, onSpawn func(pid int)) BashOperations {
	return CreateLocalShellOperations(LocalShellOptions{ShellName: "bash", ShellPath: shellPath, OnSpawn: onSpawn})
}

// CreateLocalShellOperations builds the shared local process execution used by
// the built-in shell tools (upstream createLocalShellOperations).
func CreateLocalShellOperations(options LocalShellOptions) BashOperations {
	return &localShellOperations{options: options}
}

type localShellOperations struct {
	options LocalShellOptions
}

func (o *localShellOperations) Exec(ctx context.Context, command, cwd string, execOptions BashExecOptions) (*int, error) {
	shellName := o.options.ShellName
	if shellName == "" {
		shellName = "bash"
	}
	if signalClosed(execOptions.Signal) {
		return nil, errors.New("aborted")
	}
	config := o.options.Config
	if config == nil {
		resolved, err := GetShellConfig(o.options.ShellPath)
		if err != nil {
			return nil, err
		}
		config = &resolved
	}
	if _, err := os.Stat(cwd); err != nil {
		return nil, fmt.Errorf("Working directory does not exist: %s\nCannot execute %s commands.", cwd, shellName)
	}

	commandFromStdin := config.CommandTransport == "stdin"
	args := append([]string{}, config.Args...)
	if !commandFromStdin {
		args = append(args, command)
	}
	cmd := exec.Command(config.Shell, args...)
	cmd.Dir = cwd
	if o.options.Env != nil {
		cmd.Env = o.options.Env
	} else {
		cmd.Env = shellEnv(nil, true)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if commandFromStdin {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		go func() {
			_, _ = stdin.Write([]byte(command))
			_ = stdin.Close()
		}()
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if cmd.Process != nil && o.options.OnSpawn != nil {
		o.options.OnSpawn(cmd.Process.Pid)
	}
	defer func() {
		if cmd.Process != nil && o.options.OnExit != nil {
			o.options.OnExit(cmd.Process.Pid)
		}
	}()

	var timedOut atomic.Bool
	var timeoutTimer *time.Timer
	var killOnce sync.Once
	killTree := func() {
		killOnce.Do(func() {
			if cmd.Process != nil {
				KillProcessTree(cmd.Process.Pid)
			}
		})
	}

	// Stream stdout and stderr. Activity is reported so the post-exit drain can
	// distinguish a quiet inherited handle from one still being written.
	streamWG := &sync.WaitGroup{}
	activity := make(chan struct{}, 1)
	stream := func(reader interface{ Read([]byte) (int, error) }) {
		streamWG.Add(1)
		go func() {
			defer streamWG.Done()
			buffer := make([]byte, 32*1024)
			for {
				read, err := reader.Read(buffer)
				if read > 0 {
					select {
					case activity <- struct{}{}:
					default:
					}
					if execOptions.OnData != nil {
						chunk := make([]byte, read)
						copy(chunk, buffer[:read])
						execOptions.OnData(chunk)
					}
				}
				if err != nil {
					return
				}
			}
		}()
	}
	stream(stdout)
	stream(stderr)

	// Cancel the command when the signal fires.
	stopAbort := make(chan struct{})
	if execOptions.Signal != nil || ctx != nil {
		go func() {
			select {
			case <-stopAbort:
			case <-execOptions.Signal:
				killTree()
			case <-ctx.Done():
				killTree()
			}
		}()
	}
	if execOptions.Timeout != nil && *execOptions.Timeout > 0 {
		timeoutTimer = time.AfterFunc(*execOptions.Timeout, func() {
			timedOut.Store(true)
			killTree()
		})
	}

	waitErr := cmd.Wait()
	// Wait for the pipes to fall idle rather than hanging on a detached
	// descendant that inherited them; the grace timer re-arms on every chunk
	// (port of waitForChildProcess).
	waitForPipeDrain(streamWG, activity)
	close(stopAbort)
	if timeoutTimer != nil {
		timeoutTimer.Stop()
	}

	if signalClosed(execOptions.Signal) || (ctx != nil && ctx.Err() != nil) {
		return nil, errors.New("aborted")
	}
	if timedOut.Load() {
		return nil, fmt.Errorf("timeout:%s", formatTimeout(execOptions.Timeout))
	}

	exitCode := 0
	if waitErr != nil {
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) {
			exitCode = exitError.ExitCode()
			if exitCode < 0 {
				// A signal-killed shell has no exit code; use the standard
				// shell convention so callers do not mistake the termination
				// for a successful command.
				exitCode = 128 + signalNumber(exitError)
			}
		} else {
			return nil, waitErr
		}
	}
	return &exitCode, nil
}

// signalNumber maps a wait status to the conventional shell signal number.
func signalNumber(exitError *exec.ExitError) int {
	if status, ok := exitError.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return int(status.Signal())
	}
	return 15
}

func formatTimeout(timeout *time.Duration) string {
	if timeout == nil {
		return ""
	}
	return fmt.Sprintf("%d", timeout.Milliseconds())
}

// signalClosed reports whether a cancel channel is already closed.
func signalClosed(signal <-chan struct{}) bool {
	if signal == nil {
		return false
	}
	select {
	case <-signal:
		return true
	default:
		return false
	}
}

// tempFileID returns a random hex identifier for temp file names.
func tempFileID() string {
	const hexDigits = "0123456789abcdef"
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		// Fall back to the clock; uniqueness matters more than secrecy here.
		value := uint64(time.Now().UnixNano())
		for index := range random {
			random[index] = byte(value >> (8 * index))
		}
	}
	out := make([]byte, len(random)*2)
	for index, value := range random {
		out[index*2] = hexDigits[value>>4]
		out[index*2+1] = hexDigits[value&0xf]
	}
	return string(out)
}

// exitStdioGraceMS is upstream's post-exit stdio grace window.
const exitStdioGraceMS = 100

// waitForPipeDrain waits for the output pipes to finish, but gives up after a
// short idle grace so an inherited handle held open by a detached descendant
// cannot hang the caller (port of waitForChildProcess).
func waitForPipeDrain(streams *sync.WaitGroup, activity <-chan struct{}) {
	done := make(chan struct{})
	go func() {
		streams.Wait()
		close(done)
	}()
	timer := time.NewTimer(exitStdioGraceMS * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-done:
			return
		case <-activity:
			// Output still arriving: defer finalizing so the tail is not lost.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(exitStdioGraceMS * time.Millisecond)
		case <-timer.C:
			return
		}
	}
}
