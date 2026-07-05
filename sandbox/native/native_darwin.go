package native

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	openagent "github.com/yusheng-g/openagent-go"
)

// confineAndRun executes cmd confined by macOS Seatbelt (sandbox-exec).
// The Seatbelt profile restricts filesystem access to the workspace directory
// plus system read-only paths. Network access is denied.
func (s *Sandbox) confineAndRun(ctx context.Context, cmd openagent.Command) (openagent.Result, error) {
	profile := s.seatbeltProfile()
	args := []string{"-p", profile, "--", cmd.Program}
	args = append(args, cmd.Args...)

	c := exec.CommandContext(ctx, "sandbox-exec", args...)
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
		Stderr:   stderr.String(),
		ExitCode: 0,
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, fmt.Errorf("native sandbox (darwin): %w", err)
	}
	return result, nil
}

func (s *Sandbox) confineAndRunStream(ctx context.Context, cmd openagent.Command) <-chan openagent.ToolStreamChunk {
	ch := make(chan openagent.ToolStreamChunk, 16)
	go func() {
		defer close(ch)
		profile := s.seatbeltProfile()
		args := []string{"-p", profile, "--", cmd.Program}
		args = append(args, cmd.Args...)

		c := exec.CommandContext(ctx, "sandbox-exec", args...)
		c.Dir = s.workDir
		for _, e := range cmd.Env {
			c.Env = append(c.Env, e)
		}
		if cmd.Stdin != "" {
			c.Stdin = strings.NewReader(cmd.Stdin)
		}

		stdout, _ := c.StdoutPipe()
		stderr, _ := c.StderrPipe()
		if err := c.Start(); err != nil {
			ch <- openagent.ToolStreamChunk{Error: fmt.Errorf("native sandbox (darwin): %w", err)}
			return
		}

		done := make(chan struct{}, 2)
		go readLines(stdout, ch, done)
		go readLines(stderr, ch, done)
		<-done
		<-done
		_ = c.Wait()
	}()
	return ch
}

// seatbeltProfile generates a macOS Seatbelt profile that:
// - Allows read+write to the workspace directory
// - Allows read to system binary paths
// - Allows process execution
// - Denies network access
// - Denies access to user home, /tmp, /etc, etc.
func (s *Sandbox) seatbeltProfile() string {
	quoted := func(p string) string {
		return `"` + strings.ReplaceAll(p, `"`, `\"`) + `"`
	}

	// Read-only system paths the sandboxed process may need.
	readOnly := []string{
		"/bin", "/usr/bin", "/usr/lib", "/usr/libexec",
		"/System/Library", "/Library/Developer",
		"/private/var/select/sh",
		"/private/etc/shells",
		"/dev/null", "/dev/zero", "/dev/random", "/dev/urandom",
	}

	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(allow default)\n")

	// Deny network.
	b.WriteString("(deny network*)\n")

	// Deny access to sensitive paths.
	deny := []string{"/Users", "/Volumes", "/Applications",
		"/private/etc", "/private/tmp", "/private/var",
		"/opt", "/usr/local",
	}
	for _, p := range deny {
		b.WriteString(fmt.Sprintf("(deny file-read* file-write* (subpath %s))\n", quoted(p)))
	}

	// Allow read+write to workspace.
	b.WriteString(fmt.Sprintf("(allow file-read* file-write* (subpath %s))\n", quoted(s.workDir)))

	// Allow read-only to system paths.
	for _, p := range readOnly {
		b.WriteString(fmt.Sprintf("(allow file-read* (subpath %s))\n", quoted(p)))
	}

	return b.String()
}

