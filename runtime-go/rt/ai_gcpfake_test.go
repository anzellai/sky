//go:build !js

package rt

// Tests of the Google Cloud emulator (ai_gcpfake.go): the object semantics
// Std.Ai.Store relies on (preconditions, listing from an offset), and a
// custom job that runs the Fake protocol into the bucket the way the real
// container's shim does.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func newTestFakeGCP(t *testing.T) (*aiFakeGCP, *fakeClock, *httptest.Server) {
	t.Helper()
	f := newAiFakeGCP()
	c := &fakeClock{t: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	f.now = c.now
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, c, srv
}

func fakeDo(t *testing.T, method, url, body string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if strings.Contains(url, "/metadata/") {
		req.Header.Set("Metadata-Flavor", "Google")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestFakeGCPObjectsHonourPreconditionsAndOffsets(t *testing.T) {
	_, _, srv := newTestFakeGCP(t)
	up := srv.URL + "/storage/upload/storage/v1/b/bkt/o?uploadType=media&name="
	if st, _, _ := fakeDo(t, "POST", up+"a/x.json&ifGenerationMatch=0", `{"n":1}`); st != 200 {
		t.Fatalf("create-once: %d", st)
	}
	if st, _, _ := fakeDo(t, "POST", up+"a/x.json&ifGenerationMatch=0", `{"n":2}`); st != 412 {
		t.Fatalf("second create-once must be 412, got %d", st)
	}
	st, body, hdr := fakeDo(t, "GET", srv.URL+"/storage/storage/v1/b/bkt/o/a%2Fx.json?alt=media", "")
	if st != 200 || body != `{"n":1}` || hdr.Get("X-Goog-Generation") == "" {
		t.Fatalf("get: %d %q gen %q", st, body, hdr.Get("X-Goog-Generation"))
	}
	gen := hdr.Get("X-Goog-Generation")
	if st, _, _ := fakeDo(t, "POST", up+"a/x.json&ifGenerationMatch=999", `{"n":3}`); st != 412 {
		t.Fatalf("a stale generation must be 412, got %d", st)
	}
	if st, _, _ := fakeDo(t, "POST", up+"a/x.json&ifGenerationMatch="+gen, `{"n":3}`); st != 200 {
		t.Fatalf("the current generation must win, got %d", st)
	}
	for _, n := range []string{"a/e/00000000.jsonl", "a/e/00000001.jsonl", "a/e/00000002.jsonl"} {
		fakeDo(t, "POST", up+n, "x\n")
	}
	_, list, _ := fakeDo(t, "GET", srv.URL+"/storage/storage/v1/b/bkt/o?prefix=a/e/&startOffset=a/e/00000001.jsonl", "")
	if strings.Contains(list, "00000000") || !strings.Contains(list, "00000001") || !strings.Contains(list, "00000002") {
		t.Fatalf("startOffset is inclusive and skips earlier names: %s", list)
	}
	if st, _, _ := fakeDo(t, "DELETE", srv.URL+"/storage/storage/v1/b/bkt/o/a%2Fx.json", ""); st != 204 {
		t.Fatalf("delete: %d", st)
	}
	if st, _, _ := fakeDo(t, "DELETE", srv.URL+"/storage/storage/v1/b/bkt/o/a%2Fx.json", ""); st != 404 {
		t.Fatalf("a second delete is 404, got %d", st)
	}
	_, loc, _ := fakeDo(t, "GET", srv.URL+"/storage/storage/v1/b/bkt?fields=location", "")
	if !strings.Contains(loc, "US-CENTRAL1") {
		t.Fatalf("location: %s", loc)
	}
}

func TestFakeGCPTokensNeverEchoTheAssertion(t *testing.T) {
	_, _, srv := newTestFakeGCP(t)
	jwt := "eyJhbGciOiJub25lIn0." + "eyJpc3MiOiJzYUBwLmlhbS5nc2VydmljZWFjY291bnQuY29tIiwic2NvcGUiOiJ4In0" + ".sig"
	st, body, _ := fakeDo(t, "POST", srv.URL+"/oauth2/token", "grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Ajwt-bearer&assertion="+jwt)
	if st != 200 || !strings.Contains(body, "access_token") || strings.Contains(body, jwt) {
		t.Fatalf("access token: %d %s", st, body)
	}
	st, body, _ = fakeDo(t, "GET", srv.URL+"/metadata/computeMetadata/v1/instance/service-accounts/default/identity?audience=https://svc.run.app&format=full", "")
	if st != 200 || strings.Count(body, ".") != 2 {
		t.Fatalf("identity: %d %s", st, body)
	}
}

// fakeStageRun writes what Std.Ai.Train's stage step uploads: the train
// blob and inputs.json.
func fakeStageRun(t *testing.T, f *aiFakeGCP, crashAt int) {
	t.Helper()
	train := []byte("{\"messages\":[]}\n{\"messages\":[]}\n{\"messages\":[]}\n{\"messages\":[]}\n")
	f.put("bkt", "pre/blobs/sha256/"+aiSha256Hex(train), train)
	in, _ := json.Marshal(map[string]any{
		"v": 1, "store": "gs://bkt/pre", "run": "runs/sky-k",
		"data": map[string]any{"train": map[string]any{"sha256": aiSha256Hex(train)}},
		"driver": map[string]any{"maxSteps": 6, "fakeStepMs": 10, "checkpointEvery": 2, "fakeCrashAt": crashAt,
			"rank": 2, "layers": 1, "targets": []any{"self_attn.q_proj"},
			"shapes": map[string]any{"layers": 2, "hidden": 8, "intermediate": 16, "qDim": 8, "kvDim": 8},
			"batch":  map[string]any{"perDevice": 1, "accumulate": 1}},
	})
	f.put("bkt", "pre/runs/sky-k/inputs.json", in)
}

func fakeJobBody(attempt, resume string) string {
	return `{"displayName":"sky-k-a` + attempt + `","labels":{"sky-managed":"true","sky-run":"sky-k","sky-attempt":"` + attempt + `"},
"jobSpec":{"workerPoolSpecs":[{"machineSpec":{"machineType":"n2-standard-4"},"replicaCount":"1",
"containerSpec":{"imageUri":"r/i@sha256:00","args":["--inputs","gs://bkt/pre/runs/sky-k/inputs.json","--run","gs://bkt/pre/runs/sky-k","--attempt","` + attempt + `","--resume-from","` + resume + `"]}}],
"scheduling":{"timeout":"900s"}}}`
}

func TestFakeGCPJobRunsTheFakeProtocolIntoTheBucket(t *testing.T) {
	f, clock, srv := newTestFakeGCP(t)
	fakeStageRun(t, f, 0)
	jobs := srv.URL + "/aiplatform/v1/projects/p/locations/us-central1/customJobs"
	st, body, _ := fakeDo(t, "POST", jobs, fakeJobBody("0", "0"))
	if st != 200 {
		t.Fatalf("create: %d %s", st, body)
	}
	var job struct{ Name, State string }
	json.Unmarshal([]byte(body), &job)
	if job.State != "JOB_STATE_QUEUED" {
		t.Fatalf("a new job is queued: %s", job.State)
	}
	for i := 0; i < 10; i++ {
		clock.t = clock.t.Add(25 * time.Millisecond)
		fakeDo(t, "GET", srv.URL+"/aiplatform/v1/"+job.Name, "")
	}
	_, body, _ = fakeDo(t, "GET", srv.URL+"/aiplatform/v1/"+job.Name, "")
	if !strings.Contains(body, "JOB_STATE_SUCCEEDED") || !strings.Contains(body, "startTime") || !strings.Contains(body, "endTime") {
		t.Fatalf("the job ends: %s", body)
	}
	for _, key := range []string{"pre/runs/sky-k/attempt-0/hello.json", "pre/runs/sky-k/checkpoints/0000002/manifest.json",
		"pre/runs/sky-k/adapter/adapter_model.safetensors", "pre/runs/sky-k/adapter/manifest.json", "pre/runs/sky-k/done.json"} {
		if _, ok := f.get("bkt", key); !ok {
			t.Errorf("missing %s", key)
		}
	}
	var all strings.Builder
	for i := 0; ; i++ {
		seg, ok := f.get("bkt", fmt.Sprintf("pre/runs/sky-k/attempt-0/events/%08d.jsonl", i))
		if !ok {
			break
		}
		all.Write(seg)
	}
	text := all.String()
	for _, want := range []string{`"type":"hello"`, `"type":"checkpoint"`, `"type":"artefact"`, `"type":"done"`} {
		if !strings.Contains(text, want) {
			t.Errorf("the protocol has no %s line:\n%s", want, text)
		}
	}
	if strings.Index(text, `"type":"artefact"`) > strings.Index(text, `"type":"done"`) {
		t.Errorf("artefact comes before done")
	}
	// The list filter finds it by its labels, and only it.
	_, list, _ := fakeDo(t, "GET", jobs+"?filter="+urlQuery(`labels.sky-run="sky-k" AND labels.sky-attempt="0"`), "")
	if !strings.Contains(list, job.Name) {
		t.Fatalf("filter: %s", list)
	}
	_, list, _ = fakeDo(t, "GET", jobs+"?filter="+urlQuery(`labels.sky-run="other"`), "")
	if strings.Contains(list, job.Name) {
		t.Fatalf("filter matched another run: %s", list)
	}
}

func urlQuery(s string) string {
	r := strings.NewReplacer(" ", "%20", `"`, "%22", "=", "%3D")
	return r.Replace(s)
}

func TestFakeGCPCrashCancelAndStockout(t *testing.T) {
	f, clock, srv := newTestFakeGCP(t)
	fakeStageRun(t, f, 3)
	jobs := srv.URL + "/aiplatform/v1/projects/p/locations/us-central1/customJobs"
	_, body, _ := fakeDo(t, "POST", jobs, fakeJobBody("0", "0"))
	var job struct{ Name string }
	json.Unmarshal([]byte(body), &job)
	for i := 0; i < 10; i++ {
		clock.t = clock.t.Add(25 * time.Millisecond)
		fakeDo(t, "GET", srv.URL+"/aiplatform/v1/"+job.Name, "")
	}
	_, body, _ = fakeDo(t, "GET", srv.URL+"/aiplatform/v1/"+job.Name, "")
	if !strings.Contains(body, "JOB_STATE_FAILED") || !strings.Contains(body, "low on memory") {
		t.Fatalf("fakeCrashAt fails the first attempt with no terminal line: %s", body)
	}
	// The relaunch resumes from the newest checkpoint (step 2) and finishes.
	_, body, _ = fakeDo(t, "POST", jobs, fakeJobBody("1", "2"))
	json.Unmarshal([]byte(body), &job)
	clock.t = clock.t.Add(25 * time.Millisecond)
	fakeDo(t, "GET", srv.URL+"/aiplatform/v1/"+job.Name, "")
	// Cancel it mid-run: a checkpoint, a cancelled line, JOB_STATE_CANCELLED.
	if st, _, _ := fakeDo(t, "POST", srv.URL+"/aiplatform/v1/"+job.Name+":cancel", "{}"); st != 200 {
		t.Fatalf("cancel: %d", st)
	}
	clock.t = clock.t.Add(25 * time.Millisecond)
	_, body, _ = fakeDo(t, "GET", srv.URL+"/aiplatform/v1/"+job.Name, "")
	if !strings.Contains(body, "JOB_STATE_CANCELLED") {
		t.Fatalf("cancelled: %s", body)
	}
	if st, _, _ := fakeDo(t, "DELETE", srv.URL+"/aiplatform/v1/"+job.Name, ""); st != 200 {
		t.Fatalf("an ended job can be deleted: %d", st)
	}
	// A stockout: the job is created and fails with RESOURCE_EXHAUSTED (8).
	fakeDo(t, "POST", srv.URL+"/_fake/stockout", "")
	_, body, _ = fakeDo(t, "POST", jobs, fakeJobBody("2", "0"))
	json.Unmarshal([]byte(body), &job)
	clock.t = clock.t.Add(25 * time.Millisecond)
	_, body, _ = fakeDo(t, "GET", srv.URL+"/aiplatform/v1/"+job.Name, "")
	if !strings.Contains(body, `"code":8`) || !strings.Contains(body, "stockout") {
		t.Fatalf("stockout: %s", body)
	}
	// A quota of 0 refuses the create itself.
	fakeDo(t, "POST", srv.URL+"/_fake/quota?region=us-central1&limit=0", "")
	g2 := strings.Replace(fakeJobBody("3", "0"), `"machineType":"n2-standard-4"`, `"machineType":"g2-standard-4","acceleratorType":"NVIDIA_L4","acceleratorCount":1`, 1)
	if st, body, _ := fakeDo(t, "POST", jobs, g2); st != 429 || !strings.Contains(body, "quota") {
		t.Fatalf("quota 0: %d %s", st, body)
	}
}

func TestFakeGCPCloudRunServiceLifecycle(t *testing.T) {
	_, clock, srv := newTestFakeGCP(t)
	base := srv.URL + "/run/v2/projects/p/locations/us-central1/services"
	spec := `{"labels":{"sky-managed":"true"},"template":{"nodeSelector":{"accelerator":"nvidia-l4"},"containers":[
{"name":"tier","image":"r/t@sha256:01","ports":[{"containerPort":8080}]},
{"name":"engine","image":"r/v@sha256:02","resources":{"limits":{"cpu":"4","memory":"16Gi","nvidia.com/gpu":"1"}}}]}}`
	if st, body, _ := fakeDo(t, "POST", base+"?serviceId=tier", spec); st != 200 || !strings.Contains(body, "operations/") {
		t.Fatalf("create: %d %s", st, body)
	}
	if st, _, _ := fakeDo(t, "POST", base+"?serviceId=tier", spec); st != 409 {
		t.Fatalf("a second create is 409, got %d", st)
	}
	_, body, _ := fakeDo(t, "GET", base+"/tier", "")
	if !strings.Contains(body, "CONDITION_RECONCILING") {
		t.Fatalf("a new service reconciles: %s", body)
	}
	clock.t = clock.t.Add(40 * time.Millisecond)
	_, body, _ = fakeDo(t, "GET", base+"/tier", "")
	if !strings.Contains(body, "CONDITION_SUCCEEDED") || !strings.Contains(body, "latestReadyRevision") {
		t.Fatalf("ready: %s", body)
	}
	var svc struct{ Etag string }
	json.Unmarshal([]byte(body), &svc)
	stale := strings.Replace(spec, `{"labels"`, `{"etag":"\"old\"","labels"`, 1)
	if st, _, _ := fakeDo(t, "PATCH", base+"/tier", stale); st != 409 {
		t.Fatalf("a stale etag is refused, got %d", st)
	}
	fresh := strings.Replace(spec, `{"labels"`, `{"etag":`+jsonString(svc.Etag)+`,"labels"`, 1)
	if st, body, _ := fakeDo(t, "PATCH", base+"/tier", fresh); st != 200 {
		t.Fatalf("patch: %d %s", st, body)
	}
	// Cloud Run v2 serves getIamPolicy with GET; Google answers a POST with an
	// HTML 404 (a live deploy failed on it, 2026-10-14).
	if st, _, _ := fakeDo(t, "POST", base+"/tier:getIamPolicy", "{}"); st != 404 {
		t.Fatalf("POST getIamPolicy answered %d, not 404 as Cloud Run does", st)
	}
	_, pol, _ := fakeDo(t, "GET", base+"/tier:getIamPolicy", "")
	var p struct{ Etag string }
	json.Unmarshal([]byte(pol), &p)
	set := `{"policy":{"etag":"` + p.Etag + `","bindings":[{"role":"roles/run.invoker","members":["serviceAccount:a@p.iam.gserviceaccount.com"]}]}}`
	if st, body, _ := fakeDo(t, "POST", base+"/tier:setIamPolicy", set); st != 200 || !strings.Contains(body, "run.invoker") {
		t.Fatalf("set policy: %d %s", st, body)
	}
	if st, _, _ := fakeDo(t, "POST", base+"/tier:setIamPolicy", set); st != 409 {
		t.Fatalf("a stale policy etag is refused, got %d", st)
	}
	bad := strings.Replace(spec, `"cpu":"4","memory":"16Gi",`, ``, 1)
	if st, body, _ := fakeDo(t, "POST", base+"?serviceId=bad", bad); st != 400 || !strings.Contains(body, "cpu and memory") {
		t.Fatalf("a GPU container needs limits: %d %s", st, body)
	}
	if st, _, _ := fakeDo(t, "DELETE", base+"/tier", ""); st != 200 {
		t.Fatalf("delete: %d", st)
	}
	if st, _, _ := fakeDo(t, "GET", base+"/tier", ""); st != 404 {
		t.Fatalf("deleted: %d", st)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// Test mode keeps every outbound request mocked, except the loopback emulator
// that SKY_AI_GCP_ENDPOINT names (the fakegcp process the cargo flows share
// between several Sky processes). Before the passthrough, `sky ai train
// --backend vertex` under SKY_TEST_MODE could not reach it and failed
// NotConfigured at the metadata server.
func TestTestModeReachesTheLoopbackEmulatorOnly(t *testing.T) {
	emu := httptest.NewServer(FakeGCPHandler())
	defer emu.Close()
	other := httptest.NewServer(FakeGCPHandler())
	defer other.Close()
	t.Setenv("SKY_TEST_MODE", "1")
	t.Setenv("SKY_AI_GCP_ENDPOINT", emu.URL)
	resetTestModeForTest()
	defer resetTestModeForTest()
	c := &http.Client{Transport: &testHttpTransport{}}
	get := func(base string) (*http.Response, error) {
		req, _ := http.NewRequest("GET", base+"/metadata/computeMetadata/v1/instance/service-accounts/default/token", nil)
		req.Header.Set("Metadata-Flavor", "Google")
		return c.Do(req)
	}
	resp, err := get(emu.URL)
	if err != nil {
		t.Fatalf("the emulator SKY_AI_GCP_ENDPOINT names is refused in test mode: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("emulator status %d", resp.StatusCode)
	}
	if _, err := get(other.URL); err == nil {
		t.Fatalf("a loopback host the endpoint does not name passed through test mode")
	}
	t.Setenv("SKY_AI_GCP_ENDPOINT", "http://example.com:80")
	if aiGcpLoopbackEmulator("example.com:80") {
		t.Fatalf("a non-loopback endpoint passed through test mode")
	}
}

// A stockout Vertex waits out: the job is PENDING, with its start time set,
// until the wait ends, then it runs. Steps and the scheduling timeout count
// from RUNNING, not from that start.
func TestFakeGCPPendingThenRunning(t *testing.T) {
	f, clock, srv := newTestFakeGCP(t)
	fakeStageRun(t, f, 0)
	if st, _, _ := fakeDo(t, "POST", srv.URL+"/_fake/pending?ms=1000&run=sky-k", ""); st != 200 {
		t.Fatalf("pending control: %d", st)
	}
	jobs := srv.URL + "/aiplatform/v1/projects/p/locations/us-central1/customJobs"
	_, body, _ := fakeDo(t, "POST", jobs, fakeJobBody("0", "0"))
	var job struct{ Name string }
	json.Unmarshal([]byte(body), &job)
	clock.t = clock.t.Add(25 * time.Millisecond)
	_, body, _ = fakeDo(t, "GET", srv.URL+"/aiplatform/v1/"+job.Name, "")
	if !strings.Contains(body, "JOB_STATE_PENDING") || !strings.Contains(body, "startTime") {
		t.Fatalf("held PENDING with a start time: %s", body)
	}
	clock.t = clock.t.Add(1100 * time.Millisecond)
	_, body, _ = fakeDo(t, "GET", srv.URL+"/aiplatform/v1/"+job.Name, "")
	if !strings.Contains(body, "JOB_STATE_RUNNING") {
		t.Fatalf("runs after the wait: %s", body)
	}
}
