//go:build !js

package rt

// Provider.chatStream on a server slot (ai_stream.go): the pieces of an
// answer in order, the final body that equals them, the one-piece reading of
// a server that does not stream, the failures, and closing.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// streamAll reads a stream to its end through the kernels.
func streamAll(t *testing.T, id string) (deltas []string, body map[string]any) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		r := Ai_streamNext(id).(func() any)().(SkyResult[any, any])
		if r.Tag != 0 {
			t.Fatalf("next: %+v", r)
		}
		var ev struct {
			Delta *string        `json:"delta"`
			Done  bool           `json:"done"`
			Body  map[string]any `json:"body"`
		}
		if err := json.Unmarshal([]byte(r.OkValue.(string)), &ev); err != nil {
			t.Fatalf("event %q: %v", r.OkValue, err)
		}
		if ev.Done {
			return deltas, ev.Body
		}
		deltas = append(deltas, *ev.Delta)
	}
	t.Fatal("the stream never ended")
	return nil, nil
}

func bodyContent(b map[string]any) string {
	return b["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
}

func remoteStreamCfg(slot, url string, retries int) map[string]any {
	return map[string]any{
		"slot": slot, "mode": "", "url": url, "hasKey": false, "model": "m", "adapter": "",
		"timeoutMs": 5000, "maxInFlight": -1, "queue": -1, "queueWaitMs": -1, "retries": retries,
		"hasLocal": false, "hasResolver": false, "resolverName": "", "adapterRefreshS": -1,
	}
}

func TestAiStreamFromAManagedEngineArrivesInPieces(t *testing.T) {
	_, model := aiTestEnv(t, nil)
	cfg := map[string]any{
		"slot": "streamlocal", "mode": "", "url": "", "hasKey": false, "key": Secret{},
		"model": "", "adapter": "", "timeoutMs": -1, "maxInFlight": -1, "queue": -1,
		"queueWaitMs": -1, "retries": -1, "hasLocal": true,
		"local": map[string]any{"model": model, "context": -1, "gpu": "", "parallel": -1,
			"idleUnload": -1, "embeddings": false, "port": -1},
	}
	r := Ai_endpointStreamOpen(cfg, "/chat/completions", chatBody("one two three")).(func() any)().(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatalf("open: %+v", r)
	}
	deltas, body := streamAll(t, r.OkValue.(string))
	if len(deltas) < 3 {
		t.Fatalf("expected the answer in pieces, got %q", deltas)
	}
	if got, want := strings.Join(deltas, ""), "fake[local]: one two three"; got != want {
		t.Fatalf("joined %q, want %q", got, want)
	}
	if bodyContent(body) != strings.Join(deltas, "") {
		t.Fatalf("final body %v", body)
	}
	if u := body["usage"].(map[string]any); u["completion_tokens"].(float64) == 0 {
		t.Fatalf("usage %v", u)
	}
	// The stream is gone once read.
	if n := Ai_streamNext(r.OkValue.(string)).(func() any)().(SkyResult[any, any]); n.Tag == 0 {
		t.Fatal("a finished stream answered again")
	}
}

func TestAiStreamOfAServerThatDoesNotStreamIsOnePiece(t *testing.T) {
	aiTestEnv(t, nil)
	upstream := openAIStub(t)
	r := Ai_endpointStreamOpen(remoteStreamCfg("streamwhole", upstream.URL+"/v1", -1), "/chat/completions", chatBody("hi")).(func() any)().(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatalf("open: %+v", r)
	}
	deltas, body := streamAll(t, r.OkValue.(string))
	if len(deltas) != 1 || deltas[0] != "from the server" || bodyContent(body) != "from the server" {
		t.Fatalf("%q %v", deltas, body)
	}
}

func TestAiStreamRequestAsksForAStreamAndRetriesOnlyBeforeIt(t *testing.T) {
	aiTestEnv(t, nil)
	calls := 0
	var asked map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(503)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &asked)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hé\"}}]}\n\n")
		fmt.Fprint(w, ": a comment line\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"llo\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	r := Ai_endpointStreamOpen(remoteStreamCfg("streamretry", srv.URL+"/v1", 2), "/chat/completions", chatBody("hi")).(func() any)().(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatalf("open: %+v", r)
	}
	deltas, body := streamAll(t, r.OkValue.(string))
	if calls != 2 || strings.Join(deltas, "|") != "Hé|llo" || bodyContent(body) != "Héllo" {
		t.Fatalf("calls %d deltas %q body %v", calls, deltas, body)
	}
	if asked["stream"] != true || asked["model"] != "m" {
		t.Fatalf("request %v", asked)
	}
	if u := body["usage"].(map[string]any); u["prompt_tokens"].(float64) != 4 || u["completion_tokens"].(float64) != 2 {
		t.Fatalf("usage %v", u)
	}
}

func TestAiStreamFailuresAreTyped(t *testing.T) {
	aiTestEnv(t, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "reject") {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"bad"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"context full\"}}\n\n")
	}))
	t.Cleanup(srv.Close)
	r := Ai_endpointStreamOpen(remoteStreamCfg("streamreject", srv.URL+"/reject", -1), "/chat/completions", chatBody("hi")).(func() any)().(SkyResult[any, any])
	if r.Tag == 0 || !strings.Contains(fmt.Sprint(r.ErrValue), "RemoteRejected") {
		t.Fatalf("a 400 opened a stream: %+v", r)
	}
	r = Ai_endpointStreamOpen(remoteStreamCfg("streamerr", srv.URL+"/v1", -1), "/chat/completions", chatBody("hi")).(func() any)().(SkyResult[any, any])
	if r.Tag != 0 {
		t.Fatalf("open: %+v", r)
	}
	id := r.OkValue.(string)
	if n := Ai_streamNext(id).(func() any)().(SkyResult[any, any]); n.Tag != 0 {
		t.Fatalf("first piece: %+v", n)
	}
	n := Ai_streamNext(id).(func() any)().(SkyResult[any, any])
	if n.Tag == 0 || !strings.Contains(fmt.Sprint(n.ErrValue), "context full") {
		t.Fatalf("the error event was not a failure: %+v", n)
	}
	if n := Ai_streamNext(id).(func() any)().(SkyResult[any, any]); n.Tag == 0 {
		t.Fatal("a failed stream answered again")
	}
}

func TestAiStreamCloseEndsItAndReleasesTheQueue(t *testing.T) {
	aiTestEnv(t, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	cfg := remoteStreamCfg("streamclose", srv.URL+"/v1", -1)
	cfg["maxInFlight"] = 1
	cfg["queue"] = 0
	for i := 0; i < 3; i++ {
		r := Ai_endpointStreamOpen(cfg, "/chat/completions", chatBody("hi")).(func() any)().(SkyResult[any, any])
		if r.Tag != 0 {
			t.Fatalf("open %d (the queue place was not released): %+v", i, r)
		}
		id := r.OkValue.(string)
		if n := Ai_streamNext(id).(func() any)().(SkyResult[any, any]); n.Tag != 0 {
			t.Fatalf("piece: %+v", n)
		}
		Ai_streamClose(id).(func() any)()
		Ai_streamClose(id).(func() any)()
		if n := Ai_streamNext(id).(func() any)().(SkyResult[any, any]); n.Tag == 0 {
			t.Fatal("a closed stream answered")
		}
	}
}

func TestAiStreamBodyAsksForUsage(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(aiStreamBody(chatBody("x"))), &m); err != nil {
		t.Fatal(err)
	}
	if m["stream"] != true || m["stream_options"].(map[string]any)["include_usage"] != true {
		t.Fatalf("%v", m)
	}
}
