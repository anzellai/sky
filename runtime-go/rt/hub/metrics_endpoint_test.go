package hub

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// `Store.insertedTotal` / `droppedTotal` existed, `Stats()` returned them, and
// no hub endpoint read either — so the hub's own insert and drop rates were
// unobservable. The hub is the thing that collects everyone else's telemetry;
// it being the one process nobody can scrape is the gap.
//
// The gate posts a KNOWN number of records through the real OTLP path and then
// reads the number back out of the endpoint. The expected value is the count of
// HTTP requests this test made — not `store.Stats()`, which would make the
// assertion an identity that holds whatever the endpoint reports.
func TestHubMetricsEndpointReportsWhatTheReceiverActuallyIngested(t *testing.T) {
	recv, store, cleanup := newTestHub(t, HubConfig{AuthMode: "off"})
	defer cleanup()

	const posted = 3
	for i := 0; i < posted; i++ {
		body := protoLogBody(t, "svc", "info", fmt.Sprintf("line-%d", i))
		req := httptest.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/x-protobuf")
		w := httptest.NewRecorder()
		mux(recv).ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("POST /v1/logs #%d = %d, want 200; body=%s", i, w.Code, w.Body.String())
		}
	}
	// insertedTotal is incremented by the BATCHER at commit, not by Insert, so
	// the scrape has to happen after the batch has landed.
	store.FlushSync(2 * time.Second)

	body := scrapeHubMetrics(t, recv, "")
	if got := promValue(t, body, "sky_hub_items_inserted_total"); got != posted {
		t.Errorf("sky_hub_items_inserted_total = %v, want %d — the endpoint does not report "+
			"what the receiver ingested\n%s", got, posted, body)
	}
	// A counter with no HELP/TYPE is not a scrapeable metric.
	for _, want := range []string{
		"# HELP sky_hub_items_inserted_total",
		"# TYPE sky_hub_items_inserted_total counter",
		"# HELP sky_hub_items_dropped_total",
		"# TYPE sky_hub_items_dropped_total counter",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition is missing %q — a scraper rejects a family with no header\n%s",
				want, body)
		}
	}
}

// The drop half. `droppedTotal` is the counter that matters most, because a
// drop is the one event the hub cannot recover from, and until the saturation
// warn line landed it was invisible. A log line an operator has to be reading
// is not a rate they can alert on.
func TestHubMetricsEndpointReportsDrops(t *testing.T) {
	// A Store literal with a tiny queue, for the reason the saturation gate
	// gives: Insert's drop accounting is local to Insert, and racing a real
	// batcher to observe a drop is asserting you won a race.
	const capacity = 2
	const sent = 10
	store := &Store{queue: make(chan pendingItem, capacity)}
	recv := newReceiver(HubConfig{AuthMode: "off"}, store)

	items := make([]pendingItem, sent)
	for i := range items {
		items[i] = logItem(fmt.Sprintf("burst-%d", i))
	}
	store.Insert(items)

	body := scrapeHubMetrics(t, recv, "")
	if got := promValue(t, body, "sky_hub_items_dropped_total"); got != sent-capacity {
		t.Errorf("sky_hub_items_dropped_total = %v, want %d (%d sent into a queue of %d)\n%s",
			got, sent-capacity, sent, capacity, body)
	}
}

// The endpoint reports ingest VOLUME, which healthz and readyz deliberately do
// not — they answer "is it up" and are left open for load balancers. Volume is
// operational data about the deployment, so it rides the same bearer token the
// OTLP endpoints do. In "off" mode authMiddleware is a pass-through, so the
// default single-operator hub is unchanged.
func TestHubMetricsEndpointIsBehindTheHubToken(t *testing.T) {
	const token = "mtttttttttttttttttttttttttttttttt"
	recv, _, cleanup := newTestHub(t, HubConfig{AuthMode: "token", Token: token})
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/_hub/metrics", nil)
	w := httptest.NewRecorder()
	mux(recv).ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated GET /_hub/metrics = %d, want 401 — the hub's ingest volume "+
			"is not a liveness probe", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/_hub/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	mux(recv).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("authenticated GET /_hub/metrics = %d, want 200", w.Code)
	}
}

func scrapeHubMetrics(t *testing.T, recv *receiver, token string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/_hub/metrics", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	mux(recv).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /_hub/metrics = %d, want 200; body=%q", w.Code, w.Body.String())
	}
	return w.Body.String()
}

// promValue pulls one unlabelled sample out of a Prometheus text exposition.
func promValue(t *testing.T, body, name string) float64 {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		field, value, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || field != name {
			continue
		}
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			t.Fatalf("metric %s has unparseable value %q: %v", name, value, err)
		}
		return v
	}
	t.Fatalf("metric %s is absent from the exposition:\n%s", name, body)
	return 0
}
