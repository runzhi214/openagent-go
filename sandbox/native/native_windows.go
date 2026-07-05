package native

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	openagent "github.com/yusheng-g/openagent-go"
)

// Windows sandbox is NYI. Commands run unconfined with a warning.
// Full implementation would use Restricted Token + Job Object.
// See: CreateRestrictedToken, CreateJobObject, AssignProcessToJobObject.

func (s *Sandbox) confineAndRun(ctx context.Context, cmd openagent.Command) (openagent.Result, error) {
	c := exec.CommandContext(ctx, cmd.Program, cmd.Args...)
	c.Dir = s.workDir
	for _, e := range cmd.Env {
		c.Env = append(c.Env, e)
	}
	if cmd.Stdin != "" {
		c.Stdin = strings.NewReader(cmd.Stdin)
	}

	var stdout, stderr strings.Builder
	c.Stdout = &stdout
	c.Stderr = &stderr

	err := c.Run()
	result := openagent.Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String() + "\n[warning: windows sandbox not yet implemented, running unconfined]",
		ExitCode: 0,
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, fmt.Errorf("native sandbox (windows): %w", err)
	}
	return result, nil
}

func (s *Sandbox) confineAndRunStream(ctx context.Context, cmd openagent.Command) <-chan openagent.ToolStreamChunk {
	ch := make(chan openagent.ToolStreamChunk, 1)
	go func() {
		defer close(ch)
		result, err := s.confineAndRun(ctx, cmd)
		if err != nil {
			ch <- openagent.ToolStreamChunk{Error: err}
		} else {
			ch <- openagent.ToolStreamChunk{Content: result.Stdout + result.Stderr}
		}
	}()
	return ch
}
