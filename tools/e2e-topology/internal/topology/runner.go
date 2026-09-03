package topology

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

const commandOutputLimit = 4 << 20

// ErrOutputLimit reports that a child process exceeded the bounded output buffer.
var ErrOutputLimit = errors.New("topology: command output exceeded limit")

// Command is an explicit subprocess invocation with optional environment overrides.
type Command struct {
	Program string
	Args    []string
	Env     []string
	Dir     string
}

// Result contains bounded standard output and standard error from a command.
type Result struct {
	Stdout string
	Stderr string
}

// Runner executes a command under the caller-provided context.
type Runner interface {
	Run(ctx context.Context, command Command) (Result, error)
}

// ExecRunner runs subprocesses with a sanitized environment and bounded output.
type ExecRunner struct {
	logger *slog.Logger
}

// NewExecRunner constructs an operating-system subprocess runner.
func NewExecRunner(logger *slog.Logger) *ExecRunner {
	return &ExecRunner{logger: logger}
}

// Run executes one command and returns its bounded output.
func (r *ExecRunner) Run(ctx context.Context, command Command) (Result, error) {
	if command.Program == "" {
		return Result{}, errors.New("topology: command program is empty")
	}

	stdout := &limitedBuffer{remaining: commandOutputLimit}
	stderr := &limitedBuffer{remaining: commandOutputLimit}
	// #nosec G204 -- programs and arguments come only from the static topology plan.
	process := exec.CommandContext(ctx, command.Program, command.Args...)
	process.Dir = command.Dir
	process.Env = commandEnvironment(command.Env)
	process.Stdout = stdout
	process.Stderr = stderr

	r.logger.DebugContext(
		ctx,
		"starting command",
		"program",
		filepathBase(command.Program),
		"argument_count",
		len(command.Args),
	)
	err := process.Run()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if errors.Is(stdout.err, ErrOutputLimit) || errors.Is(stderr.err, ErrOutputLimit) {
		return result, ErrOutputLimit
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, fmt.Errorf("running %s: %w", filepathBase(command.Program), ctxErr)
		}
		return result, fmt.Errorf("running %s: %w", filepathBase(command.Program), err)
	}

	return result, nil
}

func commandEnvironment(overrides []string) []string {
	blocked := map[string]struct{}{
		"KUBECONFIG": {},
	}
	for _, value := range overrides {
		key, _, ok := strings.Cut(value, "=")
		if ok {
			blocked[key] = struct{}{}
		}

	}

	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, value := range os.Environ() {
		key, _, ok := strings.Cut(value, "=")
		if !ok {
			continue
		}
		if _, isBlocked := blocked[key]; isBlocked {
			continue
		}
		environment = append(environment, value)
	}
	environment = append(environment, overrides...)

	return environment
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	remaining int
	err       error
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	if len(p) > b.remaining {
		if b.remaining > 0 {
			_, _ = b.buffer.Write(p[:b.remaining])
			b.remaining = 0
		}
		b.err = ErrOutputLimit
		return 0, b.err
	}

	n, err := b.buffer.Write(p)
	b.remaining -= n
	if err != nil {
		b.err = err
	}
	return n, err
}

func (b *limitedBuffer) String() string {
	return b.buffer.String()
}

func filepathBase(path string) string {
	if index := strings.LastIndexAny(path, `/\\`); index >= 0 {
		return path[index+1:]
	}
	return path
}

var _ io.Writer = (*limitedBuffer)(nil)
