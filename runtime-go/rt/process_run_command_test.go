//go:build unix

package rt

import (
	"strings"
	"testing"
)

func runCommandSpec(fields map[string]any) any {
	spec := map[string]any{
		"program": "", "args": []any{}, "env": []any{}, "clearEnv": false, "cwd": "",
		"pty": false, "ptyCols": 0, "ptyRows": 0, "bufferSize": 0, "stdin": "", "stdinSet": false,
	}
	for k, v := range fields {
		spec[k] = v
	}
	return spec
}

func runCommandResult(t *testing.T, spec any) (map[string]any, any) {
	t.Helper()
	r := Subprocess_runCommand(spec).(func() any)()
	switch v := r.(type) {
	case SkyResult[any, any]:
		if v.Tag == 0 {
			return v.OkValue.(map[string]any), nil
		}
		return nil, v.ErrValue
	}
	t.Fatalf("unexpected result %#v", r)
	return nil, nil
}

func TestRunCommandStdinCodeAndStreams(t *testing.T) {
	out, err := runCommandResult(t, runCommandSpec(map[string]any{"program": "cat", "stdin": "a£─\n", "stdinSet": true}))
	if err != nil || out["stdout"] != "a£─\n" || out["code"] != 0 || out["stderr"] != "" {
		t.Fatalf("cat: %v %v", out, err)
	}
	out, err = runCommandResult(t, runCommandSpec(map[string]any{
		"program": "/bin/sh", "args": []any{"-c", "echo o; echo e >&2; exit 3"},
	}))
	if err != nil || out["code"] != 3 || out["stdout"] != "o\n" || out["stderr"] != "e\n" {
		t.Fatalf("exit 3: %v %v", out, err)
	}
	// A signal is 128 + n.
	out, err = runCommandResult(t, runCommandSpec(map[string]any{"program": "/bin/sh", "args": []any{"-c", "kill -TERM $$"}}))
	if err != nil || out["code"] != 128+15 {
		t.Fatalf("signal: %v %v", out, err)
	}
	if _, err := runCommandResult(t, runCommandSpec(map[string]any{"program": "sky-no-such-program-xyz"})); err == nil ||
		!strings.Contains(errorMessageOf(err), "Process.runCommand") {
		t.Fatalf("missing program: %v", err)
	}
}

func TestSpawnRefusesStdinWithPty(t *testing.T) {
	_, e := spawnProcess(procSpec{program: "cat", pty: true, cols: 80, rows: 24, stdin: "x", stdinSet: true})
	if e == nil {
		t.Fatal("withStdin + withPty should be refused")
	}
}
