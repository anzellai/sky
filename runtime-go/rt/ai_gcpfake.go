//go:build !js

package rt

// ai_gcpfake.go — a stateful Google Cloud emulator for Std.Ai's cloud code.
//
// It answers the calls Std.Ai.Cloud.Google, Std.Ai.Store, Std.Ai.Train's
// Vertex backend and Std.Ai.Serve's Cloud Run target make, under the path
// prefixes the Sky side uses when `SKY_AI_GCP_ENDPOINT` is set:
//
//	/oauth2/token                      service-account JWT exchange (access and ID tokens)
//	/metadata/computeMetadata/v1/…     the metadata server (token, identity)
//	/storage/…                         the GCS JSON API: media upload with
//	                                   ifGenerationMatch, get, list with prefix
//	                                   and startOffset, delete, bucket location
//	/aiplatform/v1/…/customJobs        Vertex AI custom jobs: create, get, list
//	                                   with a label filter, cancel, delete
//	/run/v2/…/services, operations     Cloud Run Admin v2 and its IAM policy
//	/artifactregistry/v1/…             an image by digest
//	/cloudquotas/v1/…                  a quota
//	/logging/v2/entries:list           a job's log lines
//	/_fake/…                           control: inject a failure, a stockout,
//	                                   list lag; read counters
//
// A custom job runs the Fake trainer's protocol, driven by the clock: each
// request advances every job to "now". It reads the run's inputs.json from
// the emulated bucket and writes what the real container's shim
// (ai_train_assets/driver/sky_cloud.py) writes: hello.json, protocol
// segments under attempt-<a>/events/, checkpoints with their manifest last,
// the PEFT adapter with a manifest, and done.json. So the Sky workflow is
// tested against the same object layout the real job produces.
//
// Under test mode the host sky-gcp.invalid is answered in process
// (ai_gcp.go). FakeGCPHandler serves the same emulator from a binary
// (rt/testdata/fakegcp) for the cargo flows, where several processes share it.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FakeGCPHandler is a new, empty emulator.
func FakeGCPHandler() http.Handler { return newAiFakeGCP() }

type aiFakeObject struct {
	data       []byte
	generation int64
}

type aiFakeJob struct {
	name, id, project, region string
	displayName               string
	labels                    map[string]string
	state                     string
	create, start, end        time.Time
	timeout                   time.Duration // scheduling.timeout: Vertex cancels the job there
	pendingUntil              time.Time     // a stockout: PENDING (start set) until then
	runningAt                 time.Time     // when it began RUNNING: steps and the timeout count from here
	hello                     bool          // the attempt's hello is written
	errCode                   int
	errMsg                    string
	machine                   string
	args                      []string

	bucket, runPrefix, inputsKey string
	attempt, resumeFrom          int

	loaded                  bool
	cfg                     map[string]any
	total, step, lastCk     int
	stepMs                  int
	every, evalEvery, crash int
	seq                     int
	cancelAsked             bool
	forceFail               string
	nTrain                  int
	logs                    []string
	loss                    float64
}

type aiFakeService struct {
	name, id, project, region string
	spec                      map[string]any
	generation                int
	etag                      string
	readyAt                   time.Time
	failReason                string
	policy                    map[string]any
	policyEtag                int
}

type aiFakeGCP struct {
	mu  sync.Mutex
	now func() time.Time

	objects  map[string]map[string]*aiFakeObject // bucket → name → object
	gen      int64
	location map[string]string // bucket → location

	jobs   map[string]*aiFakeJob
	jobSeq int

	services map[string]*aiFakeService
	ops      map[string]time.Time // operation name → done at
	opSeq    int

	missingImages map[string]bool
	unready       map[string]string // service id → reason
	repos         map[string]bool
	builds        map[string]map[string]any // operation → its result
	quotas        map[string]int            // quotaId|region → limit
	stockouts     int
	unaskedCancel map[string]int           // run key -> running jobs Vertex cancels on its own
	pendingFor    map[string]time.Duration // run key -> how long its next job waits PENDING
	remoteRepos   map[string]bool          // repository id -> a remote repository (a Docker Hub cache)
	upstream      map[string]bool          // digests the remote's upstream holds
	pulled        map[string]bool          // digests a remote repository has fetched
	listLag       time.Duration
	// clockScale makes a job's reported times run faster than the wall
	// clock (a spend test reaches a budget in milliseconds): a reported time
	// is now - (now - t) × clockScale.
	clockScale float64

	creates, cancels, jobDeletes, serviceCreates, servicePatches, serviceDeletes int
	idTokens, accessTokens, federated                                            int
}

func newAiFakeGCP() *aiFakeGCP {
	return &aiFakeGCP{
		now:           time.Now,
		objects:       map[string]map[string]*aiFakeObject{},
		location:      map[string]string{},
		jobs:          map[string]*aiFakeJob{},
		services:      map[string]*aiFakeService{},
		ops:           map[string]time.Time{},
		missingImages: map[string]bool{},
		unready:       map[string]string{},
		repos:         map[string]bool{},
		builds:        map[string]map[string]any{},
		quotas:        map[string]int{},
	}
}

func (f *aiFakeGCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<30))
	f.mu.Lock()
	defer f.mu.Unlock()
	f.advanceAll()
	path := r.URL.Path
	switch {
	case strings.HasPrefix(path, "/_fake/"):
		f.control(w, r, strings.TrimPrefix(path, "/_fake/"))
	case path == "/oauth2/token":
		f.token(w, r, body)
	case path == "/sts/v1/token":
		f.sts(w, body)
	case strings.HasPrefix(path, "/iamcredentials/v1/"):
		f.iamCredentials(w, r, strings.TrimPrefix(path, "/iamcredentials/v1/"), body)
	case strings.HasPrefix(path, "/metadata/"):
		f.metadata(w, r, strings.TrimPrefix(path, "/metadata"))
	case strings.HasPrefix(path, "/storage/"):
		f.storage(w, r, strings.TrimPrefix(path, "/storage"), body)
	case strings.HasPrefix(path, "/aiplatform/v1/"):
		f.vertex(w, r, strings.TrimPrefix(path, "/aiplatform/v1/"), body)
	case strings.HasPrefix(path, "/run/v2/"):
		f.run(w, r, strings.TrimPrefix(path, "/run/v2/"), body)
	case strings.HasPrefix(path, "/dockerregistry/v2/"):
		f.registry(w, r, strings.TrimPrefix(path, "/dockerregistry/v2/"))
	case strings.HasPrefix(path, "/artifactregistry/v1/"):
		f.artifact(w, r, strings.TrimPrefix(path, "/artifactregistry/v1/"), body)
	case strings.HasPrefix(path, "/cloudbuild/v1/"):
		f.cloudBuild(w, r, strings.TrimPrefix(path, "/cloudbuild/v1/"), body)
	case strings.HasPrefix(path, "/cloudbilling/v1/"):
		f.billing(w, r, strings.TrimPrefix(path, "/cloudbilling/v1/"))
	case strings.HasPrefix(path, "/cloudquotas/v1/"):
		f.quota(w, r, strings.TrimPrefix(path, "/cloudquotas/v1/"))
	case path == "/logging/v2/entries:list":
		f.logging(w, body)
	default:
		aiFakeError(w, 404, "NOT_FOUND", "the emulator has no route "+r.Method+" "+path)
	}
}

func aiFakeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(v)
	w.Write(b)
}

func aiFakeError(w http.ResponseWriter, status int, code, msg string) {
	aiFakeJSON(w, status, map[string]any{"error": map[string]any{"code": status, "status": code, "message": msg}})
}

func aiFakeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// ─── credentials ────────────────────────────────────────────────────────

// aiFakeClaims reads a JWT's payload without checking its signature: the
// emulator stands in for Google's token endpoint, it is not one.
func aiFakeClaims(jwt string) map[string]any {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var m map[string]any
	json.Unmarshal(raw, &m)
	return m
}

func aiFakeIDToken(aud string, now time.Time) string {
	head := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{"aud": aud, "iss": "https://accounts.google.com", "exp": now.Add(time.Hour).Unix(), "iat": now.Unix()})
	return head + "." + base64.RawURLEncoding.EncodeToString(payload) + ".fake"
}

func (f *aiFakeGCP) token(w http.ResponseWriter, r *http.Request, body []byte) {
	form, _ := url.ParseQuery(string(body))
	if form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
		aiFakeJSON(w, 400, map[string]any{"error": "unsupported_grant_type", "error_description": "use the JWT bearer grant"})
		return
	}
	claims := aiFakeClaims(form.Get("assertion"))
	if claims == nil || claims["iss"] == nil {
		aiFakeJSON(w, 400, map[string]any{"error": "invalid_grant", "error_description": "the assertion is not a JWT"})
		return
	}
	if aud, ok := claims["target_audience"].(string); ok && aud != "" {
		f.idTokens++
		aiFakeJSON(w, 200, map[string]any{"id_token": aiFakeIDToken(aud, f.now())})
		return
	}
	f.accessTokens++
	aiFakeJSON(w, 200, map[string]any{"access_token": fmt.Sprintf("fake-access-%d", f.accessTokens), "expires_in": 3600, "token_type": "Bearer"})
}

// sts exchanges a workload identity federation subject token for a
// federated token (Google's Security Token Service).
func (f *aiFakeGCP) sts(w http.ResponseWriter, body []byte) {
	form, _ := url.ParseQuery(string(body))
	if form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:token-exchange" || form.Get("subject_token") == "" || form.Get("audience") == "" {
		aiFakeJSON(w, 400, map[string]any{"error": "invalid_request", "error_description": "a token exchange needs grant_type, audience and subject_token"})
		return
	}
	f.federated++
	aiFakeJSON(w, 200, map[string]any{"access_token": fmt.Sprintf("fake-federated-%d", f.federated), "issued_token_type": "urn:ietf:params:oauth:token-type:access_token", "token_type": "Bearer", "expires_in": 3600})
}

// iamCredentials impersonates a service account with a federated token.
func (f *aiFakeGCP) iamCredentials(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer fake-federated-") {
		aiFakeError(w, 401, "UNAUTHENTICATED", "impersonation needs a federated token")
		return
	}
	switch {
	case strings.HasSuffix(path, ":generateAccessToken"):
		f.accessTokens++
		aiFakeJSON(w, 200, map[string]any{"accessToken": fmt.Sprintf("fake-impersonated-%d", f.accessTokens), "expireTime": aiFakeTime(f.now().Add(time.Hour))})
	case strings.HasSuffix(path, ":generateIdToken"):
		var req struct {
			Audience string `json:"audience"`
		}
		json.Unmarshal(body, &req)
		f.idTokens++
		aiFakeJSON(w, 200, map[string]any{"token": aiFakeIDToken(req.Audience, f.now())})
	default:
		aiFakeError(w, 404, "NOT_FOUND", path)
	}
}

func (f *aiFakeGCP) metadata(w http.ResponseWriter, r *http.Request, path string) {
	if r.Header.Get("Metadata-Flavor") != "Google" {
		aiFakeError(w, 403, "FORBIDDEN", "missing Metadata-Flavor: Google")
		return
	}
	switch path {
	case "/computeMetadata/v1/instance/service-accounts/default/token":
		f.accessTokens++
		aiFakeJSON(w, 200, map[string]any{"access_token": fmt.Sprintf("fake-meta-%d", f.accessTokens), "expires_in": 3600, "token_type": "Bearer"})
	case "/computeMetadata/v1/instance/service-accounts/default/identity":
		f.idTokens++
		w.WriteHeader(200)
		io.WriteString(w, aiFakeIDToken(r.URL.Query().Get("audience"), f.now()))
	default:
		aiFakeError(w, 404, "NOT_FOUND", path)
	}
}

// ─── storage ────────────────────────────────────────────────────────────

func (f *aiFakeGCP) bucket(name string) map[string]*aiFakeObject {
	b := f.objects[name]
	if b == nil {
		b = map[string]*aiFakeObject{}
		f.objects[name] = b
	}
	return b
}

func (f *aiFakeGCP) put(bucket, name string, data []byte) int64 {
	f.gen++
	f.bucket(bucket)[name] = &aiFakeObject{data: data, generation: f.gen}
	return f.gen
}

func (f *aiFakeGCP) get(bucket, name string) ([]byte, bool) {
	o, ok := f.bucket(bucket)[name]
	if !ok {
		return nil, false
	}
	return o.data, true
}

func (f *aiFakeGCP) storage(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	q := r.URL.Query()
	if rest, ok := strings.CutPrefix(path, "/upload/storage/v1/b/"); ok {
		bucket, tail, _ := strings.Cut(rest, "/")
		if tail != "o" || r.Method != "POST" {
			aiFakeError(w, 400, "INVALID", "upload: POST /upload/storage/v1/b/<bucket>/o")
			return
		}
		name := q.Get("name")
		if want := q.Get("ifGenerationMatch"); want != "" {
			cur, exists := f.bucket(bucket)[name]
			g, _ := strconv.ParseInt(want, 10, 64)
			if (g == 0 && exists) || (g != 0 && (!exists || cur.generation != g)) {
				aiFakeError(w, 412, "FAILED_PRECONDITION", "conditionNotMet")
				return
			}
		}
		gen := f.put(bucket, name, body)
		aiFakeJSON(w, 200, map[string]any{"name": name, "bucket": bucket, "generation": strconv.FormatInt(gen, 10), "size": strconv.Itoa(len(body))})
		return
	}
	rest, ok := strings.CutPrefix(path, "/storage/v1/b/")
	if !ok {
		aiFakeError(w, 404, "NOT_FOUND", path)
		return
	}
	bucket, tail, hasTail := strings.Cut(rest, "/")
	if !hasTail {
		loc := f.location[bucket]
		if loc == "" {
			loc = "US-CENTRAL1"
		}
		aiFakeJSON(w, 200, map[string]any{"name": bucket, "location": loc})
		return
	}
	if tail == "o" {
		prefix, start := q.Get("prefix"), q.Get("startOffset")
		var names []string
		for n := range f.bucket(bucket) {
			if strings.HasPrefix(n, prefix) && (start == "" || n >= start) {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		page := 1000
		if p, err := strconv.Atoi(q.Get("maxResults")); err == nil && p > 0 {
			page = p
		}
		from := 0
		if t := q.Get("pageToken"); t != "" {
			from, _ = strconv.Atoi(t)
		}
		items := []any{}
		next := ""
		for i := from; i < len(names); i++ {
			if len(items) == page {
				next = strconv.Itoa(i)
				break
			}
			items = append(items, map[string]any{"name": names[i]})
		}
		out := map[string]any{"items": items}
		if next != "" {
			out["nextPageToken"] = next
		}
		aiFakeJSON(w, 200, out)
		return
	}
	name, ok := strings.CutPrefix(tail, "o/")
	if !ok {
		aiFakeError(w, 404, "NOT_FOUND", path)
		return
	}
	name, _ = url.PathUnescape(name)
	o, exists := f.bucket(bucket)[name]
	switch r.Method {
	case "GET":
		if !exists {
			aiFakeError(w, 404, "NOT_FOUND", "No such object: "+bucket+"/"+name)
			return
		}
		if q.Get("alt") == "media" {
			w.Header().Set("X-Goog-Generation", strconv.FormatInt(o.generation, 10))
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(200)
			w.Write(o.data)
			return
		}
		aiFakeJSON(w, 200, map[string]any{"name": name, "generation": strconv.FormatInt(o.generation, 10), "size": strconv.Itoa(len(o.data))})
	case "DELETE":
		if !exists {
			aiFakeError(w, 404, "NOT_FOUND", "No such object: "+bucket+"/"+name)
			return
		}
		delete(f.bucket(bucket), name)
		w.WriteHeader(204)
	default:
		aiFakeError(w, 405, "METHOD_NOT_ALLOWED", r.Method)
	}
}

// aiFakeGs splits gs://bucket/key.
func aiFakeGs(u string) (string, string) {
	rest := strings.TrimPrefix(u, "gs://")
	b, k, _ := strings.Cut(rest, "/")
	return b, strings.Trim(k, "/")
}

// ─── Vertex AI ──────────────────────────────────────────────────────────

// reported is a job time as the emulator reports it (see clockScale).
func (f *aiFakeGCP) reported(t time.Time) time.Time {
	if t.IsZero() || f.clockScale <= 1 {
		return t
	}
	now := f.now()
	return now.Add(-time.Duration(float64(now.Sub(t)) * f.clockScale))
}

func (f *aiFakeGCP) jobValue(j *aiFakeJob) map[string]any {
	v := map[string]any{"name": j.name, "displayName": j.displayName, "state": j.state,
		"createTime": aiFakeTime(f.reported(j.create)), "labels": j.labels,
		"jobSpec": map[string]any{"workerPoolSpecs": []any{map[string]any{"machineSpec": map[string]any{"machineType": j.machine},
			"containerSpec": map[string]any{"args": j.args}}}}}
	if !j.start.IsZero() {
		v["startTime"] = aiFakeTime(f.reported(j.start))
	}
	if !j.end.IsZero() {
		v["endTime"] = aiFakeTime(f.reported(j.end))
	}
	if j.errMsg != "" {
		v["error"] = map[string]any{"code": j.errCode, "message": j.errMsg}
	}
	return v
}

// aiFakeFilter matches `labels.k="v"` terms joined by AND (the only filter
// Std.Ai uses).
func aiFakeFilter(filter string, labels map[string]string) bool {
	for _, term := range strings.Split(filter, " AND ") {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		k, v, ok := strings.Cut(term, "=")
		if !ok || !strings.HasPrefix(k, "labels.") {
			return false
		}
		if labels[strings.TrimPrefix(k, "labels.")] != strings.Trim(v, `"`) {
			return false
		}
	}
	return true
}

func (f *aiFakeGCP) vertex(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	// projects/<p>/locations/<r>/customJobs[/<id>[:cancel]]
	parts := strings.Split(path, "/")
	if len(parts) < 5 || parts[0] != "projects" || parts[2] != "locations" || parts[4] != "customJobs" {
		aiFakeError(w, 404, "NOT_FOUND", path)
		return
	}
	project, region := parts[1], parts[3]
	if len(parts) == 5 {
		switch r.Method {
		case "POST":
			f.createJob(w, project, region, body)
		case "GET":
			var out []any
			names := make([]string, 0, len(f.jobs))
			for n := range f.jobs {
				names = append(names, n)
			}
			sort.Strings(names)
			for i := len(names) - 1; i >= 0; i-- {
				j := f.jobs[names[i]]
				if j.project != project || j.region != region {
					continue
				}
				if f.listLag > 0 && f.now().Sub(j.create) < f.listLag {
					continue // the list lags the create
				}
				if aiFakeFilter(r.URL.Query().Get("filter"), j.labels) {
					out = append(out, f.jobValue(j))
				}
			}
			if out == nil {
				aiFakeJSON(w, 200, map[string]any{})
				return
			}
			aiFakeJSON(w, 200, map[string]any{"customJobs": out})
		default:
			aiFakeError(w, 405, "METHOD_NOT_ALLOWED", r.Method)
		}
		return
	}
	id, verb, _ := strings.Cut(parts[5], ":")
	name := strings.Join(append(parts[:5], id), "/")
	j := f.jobs[name]
	if j == nil {
		aiFakeError(w, 404, "NOT_FOUND", "CustomJob "+name+" not found")
		return
	}
	switch {
	case verb == "cancel" && r.Method == "POST":
		f.cancels++
		if aiFakeTerminal(j.state) {
			aiFakeError(w, 400, "FAILED_PRECONDITION", "the job already ended")
			return
		}
		j.cancelAsked = true
		if j.state == "JOB_STATE_QUEUED" || j.state == "JOB_STATE_PENDING" {
			j.state, j.end = "JOB_STATE_CANCELLED", f.now()
		} else {
			j.state = "JOB_STATE_CANCELLING"
		}
		aiFakeJSON(w, 200, map[string]any{})
	case verb == "" && r.Method == "GET":
		aiFakeJSON(w, 200, f.jobValue(j))
	case verb == "" && r.Method == "DELETE":
		if !aiFakeTerminal(j.state) {
			aiFakeError(w, 400, "FAILED_PRECONDITION", "a running job cannot be deleted")
			return
		}
		f.jobDeletes++
		delete(f.jobs, name)
		aiFakeJSON(w, 200, map[string]any{"name": name + "/operations/delete", "done": true})
	default:
		aiFakeError(w, 405, "METHOD_NOT_ALLOWED", r.Method+" "+verb)
	}
}

func aiFakeTerminal(state string) bool {
	switch state {
	case "JOB_STATE_SUCCEEDED", "JOB_STATE_FAILED", "JOB_STATE_CANCELLED":
		return true
	}
	return false
}

func (f *aiFakeGCP) createJob(w http.ResponseWriter, project, region string, body []byte) {
	var spec struct {
		DisplayName string            `json:"displayName"`
		Labels      map[string]string `json:"labels"`
		JobSpec     struct {
			WorkerPoolSpecs []struct {
				MachineSpec struct {
					MachineType      string `json:"machineType"`
					AcceleratorType  string `json:"acceleratorType"`
					AcceleratorCount int    `json:"acceleratorCount"`
				} `json:"machineSpec"`
				ContainerSpec struct {
					ImageURI string   `json:"imageUri"`
					Args     []string `json:"args"`
				} `json:"containerSpec"`
			} `json:"workerPoolSpecs"`
			Scheduling struct {
				Timeout string `json:"timeout"`
			} `json:"scheduling"`
		} `json:"jobSpec"`
	}
	if err := json.Unmarshal(body, &spec); err != nil || len(spec.JobSpec.WorkerPoolSpecs) != 1 {
		aiFakeError(w, 400, "INVALID_ARGUMENT", "a custom job needs one worker pool")
		return
	}
	for k, v := range spec.Labels {
		if !aiFakeLabel(k) || !aiFakeLabel(v) {
			aiFakeError(w, 400, "INVALID_ARGUMENT", fmt.Sprintf("label %q=%q is not valid", k, v))
			return
		}
	}
	pool := spec.JobSpec.WorkerPoolSpecs[0]
	if !strings.Contains(pool.ContainerSpec.ImageURI, "@sha256:") {
		aiFakeError(w, 400, "INVALID_ARGUMENT", "the emulator wants an image pinned by digest")
		return
	}
	if spec.JobSpec.Scheduling.Timeout == "" {
		aiFakeError(w, 400, "INVALID_ARGUMENT", "the emulator wants scheduling.timeout")
		return
	}
	if limit, ok := f.quotas[aiFakeL4Quota+"|"+region]; ok && pool.MachineSpec.AcceleratorCount > limit {
		aiFakeError(w, 429, "RESOURCE_EXHAUSTED", fmt.Sprintf("The following quota metrics exceed quota limits: aiplatform.googleapis.com/custom_model_training_nvidia_l4_gpus (limit %d)", limit))
		return
	}
	f.creates++
	f.jobSeq++
	id := strconv.Itoa(1000000 + f.jobSeq)
	name := "projects/" + project + "/locations/" + region + "/customJobs/" + id
	j := &aiFakeJob{name: name, id: id, project: project, region: region, displayName: spec.DisplayName,
		labels: spec.Labels, state: "JOB_STATE_QUEUED", create: f.now(), machine: pool.MachineSpec.MachineType,
		args: pool.ContainerSpec.Args}
	if d, err := time.ParseDuration(spec.JobSpec.Scheduling.Timeout); err == nil {
		j.timeout = d
	}
	for i := 0; i+1 < len(j.args); i++ {
		switch j.args[i] {
		case "--inputs":
			b, k := aiFakeGs(j.args[i+1])
			j.bucket, j.inputsKey = b, k
		case "--run":
			_, k := aiFakeGs(j.args[i+1])
			j.runPrefix = k
		case "--attempt":
			j.attempt, _ = strconv.Atoi(j.args[i+1])
		case "--resume-from":
			j.resumeFrom, _ = strconv.Atoi(j.args[i+1])
		}
	}
	if f.stockouts > 0 {
		f.stockouts--
		j.forceFail = "stockout"
	}
	f.jobs[name] = j
	aiFakeJSON(w, 200, f.jobValue(j))
}

// aiFakeRunOf is the run key of a job's display name (`<key>-a<attempt>`).
func aiFakeRunOf(display string) string {
	if i := strings.LastIndex(display, "-a"); i > 0 {
		return display[:i]
	}
	return display
}

func aiFakeLabel(s string) bool {
	if len(s) < 1 || len(s) > 63 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (f *aiFakeGCP) advanceAll() {
	for _, j := range f.jobs {
		f.advance(j)
	}
}

// advance runs a job's Fake trainer up to now, as the real container would
// have: segments of protocol lines, checkpoints before the lines that name
// them, the adapter before `artefact` and `done`.
func (f *aiFakeGCP) advance(j *aiFakeJob) {
	now := f.now()
	if aiFakeTerminal(j.state) {
		return
	}
	var lines []string
	emit := func(typ string, fields map[string]any) {
		fields["v"] = 1
		fields["type"] = typ
		b, _ := json.Marshal(fields)
		lines = append(lines, string(b))
	}
	flush := func() {
		if len(lines) == 0 {
			return
		}
		key := fmt.Sprintf("%s/attempt-%d/events/%08d.jsonl", j.runPrefix, j.attempt, j.seq)
		j.seq++
		f.put(j.bucket, key, []byte(strings.Join(lines, "\n")+"\n"))
		lines = nil
	}
	end := func(state string, code int, msg string) {
		flush()
		j.state, j.end, j.errCode, j.errMsg = state, now, code, msg
		if msg != "" {
			j.logs = append(j.logs, msg)
		}
	}
	if j.state == "JOB_STATE_QUEUED" {
		if now.Sub(j.create) < 20*time.Millisecond {
			return
		}
		if j.forceFail == "stockout" {
			end("JOB_STATE_FAILED", 8, "Resources are insufficient in region: "+j.region+" (stockout). Please try a different region.")
			return
		}
		// A stockout that Vertex waits out (seen live, 2026-10-13): the job is
		// PENDING with its start time already set, and nothing is billed.
		if d := f.pendingFor[aiFakeRunOf(j.displayName)]; d > 0 {
			delete(f.pendingFor, aiFakeRunOf(j.displayName))
			j.state, j.start, j.pendingUntil = "JOB_STATE_PENDING", now, now.Add(d)
			return
		}
		j.state, j.start, j.runningAt = "JOB_STATE_RUNNING", now, now
	} else if j.state == "JOB_STATE_PENDING" {
		if now.Before(j.pendingUntil) {
			return
		}
		j.state, j.runningAt = "JOB_STATE_RUNNING", now
	}
	if j.state == "JOB_STATE_RUNNING" && !j.hello {
		j.hello = true
		if !f.load(j) {
			end("JOB_STATE_FAILED", 3, "sky_cloud: cannot read the run's inputs at gs://"+j.bucket+"/"+j.inputsKey)
			return
		}
		f.put(j.bucket, fmt.Sprintf("%s/attempt-%d/hello.json", j.runPrefix, j.attempt),
			[]byte(fmt.Sprintf(`{"job":%q,"attempt":%d}`, j.name, j.attempt)))
		emit("hello", map[string]any{"backend": "fake", "toolchain": "emulator", "versions": map[string]any{"simulated": "1"},
			"attempt": j.attempt, "resumeFrom": j.resumeFrom, "totalSteps": j.total, "pid": 0})
		j.logs = append(j.logs, fmt.Sprintf("sky_cloud: attempt %d from step %d", j.attempt, j.resumeFrom))
	}
	if j.forceFail != "" && j.forceFail != "stockout" {
		end("JOB_STATE_FAILED", 13, j.forceFail)
		return
	}
	// The scheduling timeout: Vertex AI cancels the job, as a live run showed
	// (2026-10-12: JOB_STATE_CANCELLED, no terminal line from the trainer).
	if j.timeout > 0 && j.state == "JOB_STATE_RUNNING" && f.reported(j.runningAt).Before(now.Add(-j.timeout)) {
		end("JOB_STATE_CANCELLED", 1, "")
		return
	}
	cancelKey := strings.TrimSuffix(j.runPrefix, "/") + "/cancel"
	if _, ok := f.get(j.bucket, cancelKey); ok {
		j.cancelAsked = true
	}
	due := j.resumeFrom + int(now.Sub(j.runningAt)/(time.Duration(j.stepMs)*time.Millisecond))
	if due > j.total {
		due = j.total
	}
	for j.step < due {
		if j.cancelAsked {
			if j.step > j.lastCk {
				f.checkpoint(j, j.step)
				emit("checkpoint", map[string]any{"step": j.step, "path": fmt.Sprintf("checkpoints/%07d", j.step)})
			}
			emit("cancelled", map[string]any{"step": j.step})
			end("JOB_STATE_CANCELLED", 1, "")
			return
		}
		j.step++
		j.loss = 0.45 + 1.6/float64(1+j.step)
		emit("progress", map[string]any{"step": j.step, "total": j.total, "loss": j.loss, "learningRate": j.cfg["learningRate"],
			"tokensPerSecond": 1000.0, "epoch": 0})
		if j.evalEvery > 0 && j.step%j.evalEvery == 0 && j.step < j.total {
			emit("metric", map[string]any{"step": j.step, "name": "val_loss", "value": j.loss + 0.08})
		}
		if j.every > 0 && j.step%j.every == 0 && j.step < j.total {
			f.checkpoint(j, j.step)
			emit("checkpoint", map[string]any{"step": j.step, "path": fmt.Sprintf("checkpoints/%07d", j.step)})
			j.lastCk = j.step
		}
		// A cancel nobody asked for (seen on a live run, 2026-10-13): the job
		// ends JOB_STATE_CANCELLED well before its timeout, with no cancel
		// object and no terminal line, after its first checkpoint.
		if run := aiFakeRunOf(j.displayName); f.unaskedCancel[run] > 0 && j.lastCk > 0 {
			f.unaskedCancel[run]--
			end("JOB_STATE_CANCELLED", 1, "")
			return
		}
		if j.crash > 0 && j.attempt == 0 && j.step == j.crash {
			// What Vertex AI reports for a container that exit 137 ended
			// (measured on a live run, 2026-10-12): code 8 and a memory
			// message, though the machine had memory to spare.
			j.logs = append(j.logs, "Killed")
			end("JOB_STATE_FAILED", 8, "Replicas low on memory: . Specify a machine with larger memory and try again.")
			return
		}
	}
	if j.cancelAsked && j.step < j.total {
		if j.step > j.lastCk {
			f.checkpoint(j, j.step)
			emit("checkpoint", map[string]any{"step": j.step, "path": fmt.Sprintf("checkpoints/%07d", j.step)})
		}
		emit("cancelled", map[string]any{"step": j.step})
		end("JOB_STATE_CANCELLED", 1, "")
		return
	}
	if j.step >= j.total {
		emit("metric", map[string]any{"step": j.total, "name": "val_loss", "value": j.loss + 0.08})
		weights, conf := aiTrainSimAdapterFiles(j.cfg)
		f.put(j.bucket, j.runPrefix+"/adapter/adapter_model.safetensors", weights)
		f.put(j.bucket, j.runPrefix+"/adapter/adapter_config.json", conf)
		manifest, _ := json.Marshal(map[string]any{"files": []any{
			map[string]any{"name": "adapter_config.json", "sha256": aiSha256Hex(conf)},
			map[string]any{"name": "adapter_model.safetensors", "sha256": aiSha256Hex(weights)}}})
		f.put(j.bucket, j.runPrefix+"/adapter/manifest.json", manifest)
		emit("artefact", map[string]any{"kind": "peft", "path": "adapter", "sha256": aiSha256Hex(weights)})
		emit("done", map[string]any{"step": j.total, "trainLoss": j.loss, "valLoss": j.loss + 0.08})
		flush()
		f.put(j.bucket, j.runPrefix+"/done.json", []byte(fmt.Sprintf(`{"attempt":%d,"step":%d}`, j.attempt, j.total)))
		end("JOB_STATE_SUCCEEDED", 0, "")
		return
	}
	flush()
}

// load reads the run's inputs.json (and counts the train rows) the way the
// shim does.
func (f *aiFakeGCP) load(j *aiFakeJob) bool {
	if j.loaded {
		return true
	}
	raw, ok := f.get(j.bucket, j.inputsKey)
	if !ok {
		return false
	}
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return false
	}
	cfg := aiJMap(in, "driver")
	j.cfg = cfg
	data := aiJMap(in, "data")
	store := aiJStr(in, "store")
	_, prefix := aiFakeGs(store)
	blob := func(sha string) string {
		if prefix == "" {
			return "blobs/sha256/" + sha
		}
		return prefix + "/blobs/sha256/" + sha
	}
	if train, ok := f.get(j.bucket, blob(aiJStr(aiJMap(data, "train"), "sha256"))); ok {
		for _, l := range strings.Split(string(train), "\n") {
			if strings.TrimSpace(l) != "" {
				j.nTrain++
			}
		}
	}
	if j.nTrain == 0 {
		return false
	}
	batch := aiJMap(cfg, "batch")
	eff := max(1, aiJNum(batch, "perDevice", 4)*aiJNum(batch, "accumulate", 1))
	j.total = aiJNum(cfg, "maxSteps", 0)
	if j.total <= 0 {
		j.total = max(1, (aiJNum(cfg, "epochs", 1)*j.nTrain+eff-1)/eff)
	}
	j.stepMs = max(1, aiJNum(cfg, "fakeStepMs", 20))
	j.every, j.evalEvery, j.crash = aiJNum(cfg, "checkpointEvery", 0), aiJNum(cfg, "evalEvery", 0), aiJNum(cfg, "fakeCrashAt", 0)
	j.step, j.lastCk = j.resumeFrom, j.resumeFrom
	j.loaded = true
	return true
}

func (f *aiFakeGCP) checkpoint(j *aiFakeJob, step int) {
	dir := fmt.Sprintf("%s/checkpoints/%07d/", j.runPrefix, step)
	files := aiTrainSimCheckpointFiles(step)
	for _, name := range []string{"adapter.safetensors", "optimizer.safetensors", "state.json"} {
		f.put(j.bucket, dir+name, files[name])
	}
	f.put(j.bucket, dir+"manifest.json", files["manifest.json"])
}

// ─── Cloud Run ──────────────────────────────────────────────────────────

func (f *aiFakeGCP) newOp(project, region string, after time.Duration) map[string]any {
	f.opSeq++
	name := fmt.Sprintf("projects/%s/locations/%s/operations/op-%d", project, region, f.opSeq)
	f.ops[name] = f.now().Add(after)
	return map[string]any{"name": name, "done": after <= 0}
}

func (f *aiFakeGCP) serviceValue(s *aiFakeService) map[string]any {
	v := map[string]any{}
	for k, x := range s.spec {
		v[k] = x
	}
	v["name"] = s.name
	v["uri"] = "https://" + s.id + "-fake.a.run.app"
	v["etag"] = s.etag
	v["generation"] = strconv.Itoa(s.generation)
	rev := fmt.Sprintf("%s-%05d-fak", s.id, s.generation)
	v["latestCreatedRevision"] = s.name + "/revisions/" + rev
	now := f.now()
	switch {
	case s.failReason != "" && !now.Before(s.readyAt):
		v["reconciling"] = false
		v["terminalCondition"] = map[string]any{"type": "Ready", "state": "CONDITION_FAILED", "message": s.failReason}
	case now.Before(s.readyAt):
		v["reconciling"] = true
		v["terminalCondition"] = map[string]any{"type": "Ready", "state": "CONDITION_RECONCILING"}
	default:
		v["reconciling"] = false
		v["latestReadyRevision"] = s.name + "/revisions/" + rev
		v["terminalCondition"] = map[string]any{"type": "Ready", "state": "CONDITION_SUCCEEDED"}
	}
	return v
}

func (f *aiFakeGCP) run(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	parts := strings.Split(path, "/")
	if len(parts) < 5 || parts[0] != "projects" || parts[2] != "locations" {
		aiFakeError(w, 404, "NOT_FOUND", path)
		return
	}
	project, region := parts[1], parts[3]
	if parts[4] == "operations" && len(parts) == 6 {
		name := strings.Join(parts, "/")
		at, ok := f.ops[name]
		if !ok {
			aiFakeError(w, 404, "NOT_FOUND", name)
			return
		}
		aiFakeJSON(w, 200, map[string]any{"name": name, "done": !f.now().Before(at)})
		return
	}
	if parts[4] != "services" {
		aiFakeError(w, 404, "NOT_FOUND", path)
		return
	}
	if len(parts) == 5 {
		switch r.Method {
		case "POST":
			id := r.URL.Query().Get("serviceId")
			name := "projects/" + project + "/locations/" + region + "/services/" + id
			if _, exists := f.services[name]; exists {
				aiFakeError(w, 409, "ALREADY_EXISTS", "Resource '"+id+"' already exists.")
				return
			}
			var spec map[string]any
			if json.Unmarshal(body, &spec) != nil {
				aiFakeError(w, 400, "INVALID_ARGUMENT", "the service is not JSON")
				return
			}
			if msg := aiFakeCheckService(spec); msg != "" {
				aiFakeError(w, 400, "INVALID_ARGUMENT", msg)
				return
			}
			f.serviceCreates++
			s := &aiFakeService{name: name, id: id, project: project, region: region, spec: spec, generation: 1,
				readyAt: f.now().Add(30 * time.Millisecond), failReason: f.unready[id]}
			s.etag = fmt.Sprintf(`"%s-%d"`, id, s.generation)
			f.services[name] = s
			aiFakeJSON(w, 200, f.newOp(project, region, 30*time.Millisecond))
		case "GET":
			var out []any
			names := make([]string, 0, len(f.services))
			for n := range f.services {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				if s := f.services[n]; s.project == project && s.region == region {
					out = append(out, f.serviceValue(s))
				}
			}
			if out == nil {
				aiFakeJSON(w, 200, map[string]any{})
				return
			}
			aiFakeJSON(w, 200, map[string]any{"services": out})
		default:
			aiFakeError(w, 405, "METHOD_NOT_ALLOWED", r.Method)
		}
		return
	}
	id, verb, _ := strings.Cut(parts[5], ":")
	name := "projects/" + project + "/locations/" + region + "/services/" + id
	s := f.services[name]
	if s == nil {
		aiFakeError(w, 404, "NOT_FOUND", "Resource '"+id+"' does not exist.")
		return
	}
	switch {
	case verb == "" && r.Method == "GET":
		aiFakeJSON(w, 200, f.serviceValue(s))
	case verb == "" && r.Method == "PATCH":
		var spec map[string]any
		if json.Unmarshal(body, &spec) != nil {
			aiFakeError(w, 400, "INVALID_ARGUMENT", "the service is not JSON")
			return
		}
		if et, ok := spec["etag"].(string); ok && et != "" && et != s.etag {
			aiFakeError(w, 409, "ABORTED", "the etag does not match")
			return
		}
		if msg := aiFakeCheckService(spec); msg != "" {
			aiFakeError(w, 400, "INVALID_ARGUMENT", msg)
			return
		}
		delete(spec, "etag")
		f.servicePatches++
		s.spec, s.generation = spec, s.generation+1
		s.etag = fmt.Sprintf(`"%s-%d"`, id, s.generation)
		s.readyAt, s.failReason = f.now().Add(30*time.Millisecond), f.unready[id]
		aiFakeJSON(w, 200, f.newOp(project, region, 30*time.Millisecond))
	case verb == "" && r.Method == "DELETE":
		f.serviceDeletes++
		delete(f.services, name)
		aiFakeJSON(w, 200, f.newOp(project, region, 0))
	case verb == "getIamPolicy" && r.Method == "POST":
		p := s.policy
		if p == nil {
			p = map[string]any{}
		}
		out := map[string]any{"etag": fmt.Sprintf("BwX%d", s.policyEtag)}
		if b, ok := p["bindings"]; ok {
			out["bindings"] = b
		}
		aiFakeJSON(w, 200, out)
	case verb == "setIamPolicy" && r.Method == "POST":
		var req struct {
			Policy map[string]any `json:"policy"`
		}
		json.Unmarshal(body, &req)
		if et, _ := req.Policy["etag"].(string); et != "" && et != fmt.Sprintf("BwX%d", s.policyEtag) {
			aiFakeError(w, 409, "ABORTED", "the policy changed")
			return
		}
		s.policyEtag++
		delete(req.Policy, "etag")
		s.policy = req.Policy
		out := map[string]any{"etag": fmt.Sprintf("BwX%d", s.policyEtag), "bindings": req.Policy["bindings"]}
		aiFakeJSON(w, 200, out)
	default:
		aiFakeError(w, 405, "METHOD_NOT_ALLOWED", r.Method+" "+verb)
	}
}

// aiFakeCheckService refuses the service specs Cloud Run refuses that Sky
// could get wrong: a GPU container without enough CPU and memory, a GPU
// without its node selector, an image not pinned by digest.
func aiFakeCheckService(spec map[string]any) string {
	tmpl := aiJMap(spec, "template")
	containers, _ := tmpl["containers"].([]any)
	if len(containers) == 0 {
		return "spec.template.containers: at least one container"
	}
	gpus := 0
	ingress := 0
	for _, c := range containers {
		cm, _ := c.(map[string]any)
		img := aiJStr(cm, "image")
		if !strings.Contains(img, "@sha256:") {
			return "the emulator wants every image pinned by digest: " + img
		}
		if ports, _ := cm["ports"].([]any); len(ports) > 0 {
			ingress++
		}
		limits := aiJMap(aiJMap(cm, "resources"), "limits")
		if limits["nvidia.com/gpu"] != nil {
			gpus++
			if aiJStr(limits, "cpu") == "" || aiJStr(limits, "memory") == "" {
				return "a GPU container needs cpu and memory limits"
			}
			if aiJStr(aiJMap(tmpl, "nodeSelector"), "accelerator") == "" {
				return "a GPU needs template.nodeSelector.accelerator"
			}
		}
	}
	if ingress != 1 {
		return "exactly one container serves the ingress port"
	}
	if gpus > 1 {
		return "one container may use the GPU"
	}
	return ""
}

// ─── Artifact Registry, quotas, logging ─────────────────────────────────

func (f *aiFakeGCP) artifact(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	if strings.HasSuffix(path, "/repositories") && r.Method == "POST" {
		id := r.URL.Query().Get("repositoryId")
		key := path + "/" + id
		if f.repos[key] {
			aiFakeError(w, 409, "ALREADY_EXISTS", "the repository already exists")
			return
		}
		f.repos[key] = true
		aiFakeJSON(w, 200, map[string]any{"name": "operations/repo-" + id, "done": true})
		return
	}
	idx := strings.Index(path, "/dockerImages/")
	if idx < 0 {
		// GET …/repositories/<id>: its mode.
		if i := strings.Index(path, "/repositories/"); i >= 0 && r.Method == "GET" {
			id := path[i+len("/repositories/"):]
			mode := "STANDARD_REPOSITORY"
			if f.remoteRepos[id] {
				mode = "REMOTE_REPOSITORY"
			}
			aiFakeJSON(w, 200, map[string]any{"name": path, "format": "DOCKER", "mode": mode})
			return
		}
		aiFakeError(w, 404, "NOT_FOUND", path)
		return
	}
	img, _ := url.PathUnescape(path[idx+len("/dockerImages/"):])
	// A remote repository holds an image only once it has been pulled.
	if i := strings.Index(path, "/repositories/"); i >= 0 {
		repo := strings.SplitN(path[i+len("/repositories/"):], "/", 2)[0]
		if f.remoteRepos[repo] {
			digest := img[strings.LastIndex(img, "@")+1:]
			if !f.pulled[digest] {
				aiFakeError(w, 404, "NOT_FOUND", "image not found: "+img)
				return
			}
		}
	}
	for ref := range f.missingImages {
		if strings.HasSuffix(ref, img) {
			aiFakeError(w, 404, "NOT_FOUND", "image not found: "+img)
			return
		}
	}
	aiFakeJSON(w, 200, map[string]any{"name": path, "uri": img})
}

// registry answers Docker Registry v2 manifest requests
// (<project>/<repo>/<image…>/manifests/<digest>). Through a remote repository
// it fetches from the upstream: 200 when the upstream holds the digest.
func (f *aiFakeGCP) registry(w http.ResponseWriter, r *http.Request, path string) {
	i := strings.Index(path, "/manifests/")
	if i < 0 {
		aiFakeError(w, 404, "NOT_FOUND", path)
		return
	}
	digest := path[i+len("/manifests/"):]
	parts := strings.SplitN(path[:i], "/", 3)
	if len(parts) < 3 || !f.remoteRepos[parts[1]] || !f.upstream[digest] {
		aiFakeError(w, 404, "NOT_FOUND", "manifest unknown")
		return
	}
	if f.pulled == nil {
		f.pulled = map[string]bool{}
	}
	f.pulled[digest] = true
	w.Header().Set("Docker-Content-Digest", digest)
	aiFakeJSON(w, 200, map[string]any{"schemaVersion": 2})
}

// aiFakeL4Quota is the Cloud Quotas id of Vertex AI's L4 training quota.
const aiFakeL4Quota = "CustomModelTrainingL4GPUsPerProjectPerRegion"

func (f *aiFakeGCP) quota(w http.ResponseWriter, r *http.Request, path string) {
	idx := strings.Index(path, "/quotaInfos/")
	if idx < 0 {
		aiFakeError(w, 404, "NOT_FOUND", path)
		return
	}
	id := path[idx+len("/quotaInfos/"):]
	var infos []any
	for key, limit := range f.quotas {
		qid, region, _ := strings.Cut(key, "|")
		if qid == id {
			infos = append(infos, map[string]any{"dimensions": map[string]any{"region": region}, "details": map[string]any{"value": strconv.Itoa(limit)}})
		}
	}
	if infos == nil {
		infos = []any{map[string]any{"dimensions": map[string]any{}, "details": map[string]any{"value": "1"}}}
	}
	aiFakeJSON(w, 200, map[string]any{"quotaId": id, "dimensionsInfos": infos})
}

func (f *aiFakeGCP) logging(w http.ResponseWriter, body []byte) {
	var req struct {
		Filter string `json:"filter"`
	}
	json.Unmarshal(body, &req)
	var entries []any
	for _, j := range f.jobs {
		if strings.Contains(req.Filter, `"`+j.id+`"`) {
			for i := len(j.logs) - 1; i >= 0; i-- {
				entries = append(entries, map[string]any{"textPayload": j.logs[i]})
			}
		}
	}
	aiFakeJSON(w, 200, map[string]any{"entries": entries})
}

// ─── Cloud Build and the billing catalogue ──────────────────────────────

// cloudBuild runs a build at once: its image's digest is the SHA-256 of the
// build request (a build of the same source is the same image).
func (f *aiFakeGCP) cloudBuild(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	if strings.HasSuffix(path, "/builds") && r.Method == "POST" {
		var req struct {
			Source struct {
				StorageSource struct{ Bucket, Object string } `json:"storageSource"`
			} `json:"source"`
			Images []string `json:"images"`
		}
		json.Unmarshal(body, &req)
		if _, ok := f.get(req.Source.StorageSource.Bucket, req.Source.StorageSource.Object); !ok || len(req.Images) == 0 {
			aiFakeError(w, 400, "INVALID_ARGUMENT", "the build's source is not in the bucket")
			return
		}
		f.opSeq++
		id := fmt.Sprintf("build-%d", f.opSeq)
		f.builds[path+"/"+id] = map[string]any{"id": id, "status": "SUCCESS", "results": map[string]any{"images": []any{
			map[string]any{"name": req.Images[0], "digest": "sha256:" + aiSha256Hex(body)}}}}
		aiFakeJSON(w, 200, map[string]any{"name": "operations/build/p/" + id, "done": false,
			"metadata": map[string]any{"build": map[string]any{"id": id, "status": "QUEUED"}}})
		return
	}
	if res, ok := f.builds[path]; ok {
		aiFakeJSON(w, 200, res)
		return
	}
	aiFakeError(w, 404, "NOT_FOUND", path)
}

// billing serves a tiny catalogue: an L4 GPU and a vCPU for Cloud Run.
func (f *aiFakeGCP) billing(w http.ResponseWriter, r *http.Request, path string) {
	if path == "services" {
		aiFakeJSON(w, 200, map[string]any{"services": []any{
			map[string]any{"serviceId": "152E-C115-5142", "displayName": "Cloud Run"},
			map[string]any{"serviceId": "C7E2-9256-1C43", "displayName": "Vertex AI"}}})
		return
	}
	sku := func(desc, unit string, units string, nanos int) map[string]any {
		return map[string]any{"description": desc, "serviceRegions": []any{"us-central1"},
			"pricingInfo": []any{map[string]any{"pricingExpression": map[string]any{"usageUnit": unit,
				"tieredRates": []any{map[string]any{"unitPrice": map[string]any{"units": units, "nanos": nanos}}}}}}}
	}
	switch path {
	case "services/152E-C115-5142/skus":
		aiFakeJSON(w, 200, map[string]any{"skus": []any{
			sku("Services Nvidia L4 GPU in us-central1 (no zonal redundancy)", "s", "0", 186700),
			sku("Services CPU in us-central1", "s", "0", 18000)}})
	case "services/C7E2-9256-1C43/skus":
		aiFakeJSON(w, 200, map[string]any{"skus": []any{
			sku("Custom Training on G2 Instance Core running in Americas", "h", "0", 25000000)}})
	default:
		aiFakeError(w, 404, "NOT_FOUND", path)
	}
}

// ─── control ────────────────────────────────────────────────────────────

func (f *aiFakeGCP) control(w http.ResponseWriter, r *http.Request, what string) {
	q := r.URL.Query()
	switch what {
	case "stats":
		jobs := []any{}
		for _, j := range f.jobs {
			jobs = append(jobs, map[string]any{"name": j.name, "state": j.state, "labels": j.labels, "attempt": j.attempt,
				"resumeFrom": j.resumeFrom, "step": j.step})
		}
		services := []any{}
		for _, s := range f.services {
			services = append(services, map[string]any{"name": s.name, "generation": s.generation, "policy": s.policy})
		}
		objects := 0
		for _, b := range f.objects {
			objects += len(b)
		}
		aiFakeJSON(w, 200, map[string]any{"creates": f.creates, "cancels": f.cancels, "jobDeletes": f.jobDeletes,
			"serviceCreates": f.serviceCreates, "servicePatches": f.servicePatches, "serviceDeletes": f.serviceDeletes,
			"idTokens": f.idTokens, "accessTokens": f.accessTokens, "federated": f.federated, "jobs": jobs, "services": services, "objects": objects})
	case "fail":
		if j := f.jobs[q.Get("job")]; j != nil {
			j.forceFail = orDefault(q.Get("message"), "The replica workerpool0-0 exited with a non-zero status of 1.")
		}
		aiFakeJSON(w, 200, map[string]any{})
	case "stockout":
		f.stockouts++
		aiFakeJSON(w, 200, map[string]any{})
	case "remote-repo":
		if f.remoteRepos == nil {
			f.remoteRepos, f.upstream = map[string]bool{}, map[string]bool{}
		}
		f.remoteRepos[q.Get("repo")] = true
		if d := q.Get("upstream"); d != "" {
			f.upstream[d] = true
		}
		aiFakeJSON(w, 200, map[string]any{})
	case "pending":
		ms, _ := strconv.Atoi(q.Get("ms"))
		if f.pendingFor == nil {
			f.pendingFor = map[string]time.Duration{}
		}
		f.pendingFor[q.Get("run")] = time.Duration(ms) * time.Millisecond
		aiFakeJSON(w, 200, map[string]any{})
	case "unasked-cancel":
		if f.unaskedCancel == nil {
			f.unaskedCancel = map[string]int{}
		}
		f.unaskedCancel[q.Get("run")]++
		aiFakeJSON(w, 200, map[string]any{})
	case "clockscale":
		x, _ := strconv.ParseFloat(q.Get("x"), 64)
		f.clockScale = x
		aiFakeJSON(w, 200, map[string]any{})
	case "listlag":
		ms, _ := strconv.Atoi(q.Get("ms"))
		f.listLag = time.Duration(ms) * time.Millisecond
		aiFakeJSON(w, 200, map[string]any{})
	case "quota":
		limit, _ := strconv.Atoi(q.Get("limit"))
		f.quotas[orDefault(q.Get("id"), aiFakeL4Quota)+"|"+q.Get("region")] = limit
		aiFakeJSON(w, 200, map[string]any{})
	case "bucket":
		f.location[q.Get("name")] = q.Get("location")
		aiFakeJSON(w, 200, map[string]any{})
	case "image-missing":
		f.missingImages[q.Get("ref")] = true
		aiFakeJSON(w, 200, map[string]any{})
	case "unready":
		f.unready[q.Get("service")] = orDefault(q.Get("reason"), "The user-provided container failed to start and listen on the port.")
		aiFakeJSON(w, 200, map[string]any{})
	case "objects":
		var names []string
		for b, objs := range f.objects {
			for n := range objs {
				if strings.HasPrefix(b+"/"+n, q.Get("prefix")) {
					names = append(names, b+"/"+n)
				}
			}
		}
		sort.Strings(names)
		aiFakeJSON(w, 200, map[string]any{"objects": names})
	default:
		aiFakeError(w, 404, "NOT_FOUND", "control "+what)
	}
}
