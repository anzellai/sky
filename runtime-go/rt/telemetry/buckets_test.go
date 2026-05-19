package telemetry

import (
	"reflect"
	"testing"
)

// Per-metric profile assignment via MetricBuckets. Sky kernel
// metrics that need duration profile (jobs) or bytes profile
// (payloads) must NOT fall back to latency profile.

func TestBucketsFor_KnownMetricsUseRightProfile(t *testing.T) {
	cases := []struct {
		name    string
		profile BucketProfile
	}{
		{"sky_live_request_seconds", BucketsLatency},
		{"sky_live_msg_seconds", BucketsLatency},
		{"sky_db_query_seconds", BucketsLatency},
		{"sky_jobs_duration_seconds", BucketsDuration},
		{"sky_http_response_bytes", BucketsBytes},
		{"sky_http_request_bytes", BucketsBytes},
	}
	for _, c := range cases {
		got := bucketsFor(c.name)
		if !reflect.DeepEqual(got, c.profile) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.profile)
		}
	}
}

func TestBucketsFor_UnknownMetricDefaultsToLatency(t *testing.T) {
	got := bucketsFor("user_custom_metric")
	if !reflect.DeepEqual(got, BucketsLatency) {
		t.Errorf("unknown metric should default to BucketsLatency; got %v", got)
	}
}

// Verify Observe on a duration-profile metric uses the
// duration boundaries (i.e. emits a 60s and 300s bucket that
// wouldn't exist under the latency profile).
func TestObserve_DurationMetricUsesDurationBuckets(t *testing.T) {
	s := NewStore()
	// 120s observation — beyond latency's 5s cap, exactly between
	// duration's 60s and 300s boundaries.
	s.Observe("sky_jobs_duration_seconds",
		map[string]string{"queue": "default"}, 120.0)

	snap := s.Snapshot()
	var got *MetricSample
	for i := range snap {
		if snap[i].Name == "sky_jobs_duration_seconds" {
			got = &snap[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("expected sky_jobs_duration_seconds in snapshot")
	}
	// Boundaries should be the duration set.
	if _, ok := got.Buckets[300.0]; !ok {
		t.Errorf("missing 300s bucket — duration profile not applied")
	}
	if _, ok := got.Buckets[5.0]; !ok {
		// 5.0 is in both profiles, sanity.
		t.Errorf("missing 5s bucket")
	}
	// 120s observation falls in the 300s cumulative bucket.
	if got.Buckets[300.0] != 1 {
		t.Errorf("expected Buckets[300]=1, got %d", got.Buckets[300.0])
	}
	if got.Buckets[60.0] != 0 {
		t.Errorf("expected Buckets[60]=0 (120 > 60), got %d", got.Buckets[60.0])
	}
}

// Bytes metric: 50KB observation lands in 100KB cumulative bucket.
func TestObserve_BytesMetricUsesBytesBuckets(t *testing.T) {
	s := NewStore()
	s.Observe("sky_http_response_bytes",
		map[string]string{"route": "/api"}, 50000)
	snap := s.Snapshot()
	var got *MetricSample
	for i := range snap {
		if snap[i].Name == "sky_http_response_bytes" {
			got = &snap[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("expected sky_http_response_bytes in snapshot")
	}
	if got.Buckets[100000] != 1 {
		t.Errorf("expected Buckets[100000]=1, got %d", got.Buckets[100000])
	}
	if got.Buckets[10000] != 0 {
		t.Errorf("expected Buckets[10000]=0 (50000 > 10000), got %d", got.Buckets[10000])
	}
}
