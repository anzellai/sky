//go:build !js

package rt

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func runCliCapture(t *testing.T, cfg map[string]any) (string, any) {
	t.Helper()
	var buf bytes.Buffer
	old := cliOut
	cliOut = &buf
	defer func() { cliOut = old }()
	done := make(chan any, 1)
	go func() { done <- cliProgramRun(cfg) }()
	select {
	case res := <-done:
		return buf.String(), res
	case <-time.After(5 * time.Second):
		t.Fatalf("Cli program did not exit; output so far:\n%s", buf.String())
	}
	return "", nil
}

func cliCfg(init, update, view, subs any) map[string]any {
	return map[string]any{"Init": init, "Update": update, "View": view, "Subscriptions": subs}
}

// terminal:cli without withInput must not fail at start: it runs init,
// its Cmds and its timers, then exits 0 (T16).
func TestCli_NoInputRunsToCompletion(t *testing.T) {
	cfg := cliCfg(
		func(_ any) any { return SkyTuple2{V0: 0, V1: Cmd_none()} },
		func(msg, m any) any { return SkyTuple2{V0: m.(int) + 1, V1: Cmd_none()} },
		func(m any) any { return fmt.Sprintf("n=%d\n", m) },
		func(m any) any {
			if m.(int) < 3 {
				return Sub_every(10, "tick")
			}
			return Sub_none()
		},
	)
	out, res := runCliCapture(t, cfg)
	if isErrResult(res) {
		t.Fatalf("Cli without an input handler failed: %v", extractErrResultValue(res))
	}
	if !strings.Contains(out, "n=3") {
		t.Fatalf("timer-driven run did not reach n=3:\n%s", out)
	}
}

// An in-flight Cmd.perform still lands and renders before the program
// exits on stdin EOF (T17 / SA-11).
func TestCli_EOFWaitsForInFlightPerform(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldIn := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = oldIn }()
	slow := func() any {
		time.Sleep(150 * time.Millisecond)
		return Ok[any, any]("fetched")
	}
	cfg := cliCfg(
		func(_ any) any { return SkyTuple2{V0: "idle", V1: Cmd_none()} },
		func(msg, m any) any {
			if msg == "go" {
				return SkyTuple2{V0: "loading", V1: Cmd_perform(slow, func(res any) any { return "done" })}
			}
			return SkyTuple2{V0: fmt.Sprint(msg), V1: Cmd_none()}
		},
		func(m any) any { return "state=" + m.(string) + "\n" },
		nil,
	)
	cfg["OnLine"] = func(line any) any { return line }
	w.Write([]byte("go\n"))
	w.Close()
	out, _ := runCliCapture(t, cfg)
	if !strings.Contains(out, "state=done") {
		t.Fatalf("in-flight Cmd.perform dropped at EOF; output:\n%s", out)
	}
}

// Cmd.publish reaches the app's own subscribeTopic subscriber, and a
// guard rejection skips update (T11, T12).
func TestCli_PublishAndGuard(t *testing.T) {
	type got struct{ v any }
	cfg := cliCfg(
		func(_ any) any { return SkyTuple2{V0: "start", V1: Cmd_publish("room", "hello")} },
		func(msg, m any) any {
			switch x := msg.(type) {
			case got:
				return SkyTuple2{V0: "got:" + fmt.Sprint(x.v), V1: Cmd_publish("blocked", 1)}
			case string:
				return SkyTuple2{V0: "UPDATED-" + x, V1: Cmd_none()}
			}
			return SkyTuple2{V0: m, V1: Cmd_none()}
		},
		func(m any) any { return "m=" + m.(string) + "\n" },
		func(_ any) any {
			return Sub_batch([]any{
				Sub_subscribeTopic("room", func(p any) any { return got{p} }),
				Sub_subscribeTopic("blocked", func(p any) any { return "forbidden" }),
			})
		},
	)
	cfg["Guard"] = func(msg, m any) any {
		if msg == "forbidden" {
			return Err[any, any](ErrPermissionDenied("no"))
		}
		return Ok[any, any](struct{}{})
	}
	out, _ := runCliCapture(t, cfg)
	if !strings.Contains(out, "m=got:hello") {
		t.Fatalf("published payload not delivered to the subscriber:\n%s", out)
	}
	if strings.Contains(out, "UPDATED-forbidden") {
		t.Fatalf("guard rejection still ran update:\n%s", out)
	}
}
