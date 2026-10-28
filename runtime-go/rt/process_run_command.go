package rt

import (
	"bytes"
	"os/exec"
	"strings"
)

// Subprocess_runCommand : CommandSpec -> Task Error Completed
//
// `Process.runCommand cmd`: run the command to completion and return
// `{ code, stdout, stderr }`. A non-zero exit is a RESULT, not an Error; the
// only errors are the ones that stop the child from starting (`NotFound`,
// `PermissionDenied`, `InvalidInput` for an empty program name or a PTY, `Io`
// otherwise). A child killed by a signal reports `code = 128 + signal`, the
// shell's convention.
//
// The child gets the command's arguments, environment (`withEnv`,
// `withClearEnv`), working directory (`withCwd`) and, with `withStdin`, that
// text on its standard input followed by end of file; without it, its
// standard input is empty. It runs like `Process.run`: in the parent's
// process group, so a Ctrl-C at the terminal reaches it too. Both outputs are
// held in memory in full (the cost is their size); for a long-running or
// very chatty child, use `spawn` and read its bounded output rings.
func Subprocess_runCommand(cfg any) any {
	spec := parseProcSpec(cfg)
	return func() any {
		if spec.program == "" {
			return Err[any, any](ErrInvalidInput("Process.runCommand: the program name is empty"))
		}
		if spec.pty {
			return Err[any, any](ErrInvalidInput("Process.runCommand: a PTY child is interactive; use Process.spawn"))
		}
		cmd := exec.Command(spec.program, spec.args...)
		cmd.Dir = spec.cwd
		cmd.Env = procEnviron(spec)
		cmd.Stdin = strings.NewReader(spec.stdin)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			return Err[any, any](procStartErrorFor("Process.runCommand", spec.program, err))
		}
		_ = cmd.Wait() // a non-zero exit is reported through ProcessState
		code, sig, signalled := procExitInfo(cmd.ProcessState)
		if signalled {
			code = 128 + sig
		}
		return Ok[any, any](map[string]any{
			"code":   code,
			"stdout": stdout.String(),
			"stderr": stderr.String(),
		})
	}
}
