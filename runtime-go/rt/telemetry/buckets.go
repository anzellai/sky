package telemetry

// BucketProfile is a fixed set of histogram bucket boundaries (in
// units appropriate to the metric — seconds for *_seconds, bytes for
// *_bytes). Three named profiles cover every Sky kernel metric;
// users CANNOT customise. Locked-bucket design rationale (full text
// in docs/v1-rfc/1-observability.md §"Resolved questions" #6):
//
//   - Cross-app Grafana dashboards rely on consistent buckets.
//     `histogram_quantile(0.99, sum(...) by (le))` only works when
//     every contributor uses the same `le` boundaries.
//   - Per-metric customisation is a footgun: developers tune buckets
//     to what they currently see, then traffic patterns shift and
//     the histogram becomes useless without a redeploy.
//   - Three profiles cover web/DB/job/file workloads. New profiles
//     can land in minor releases — but never per-call overrides.
//
// Each Sky metric maps to a profile in MetricBuckets below. Metrics
// without an explicit mapping default to BucketsLatency (the most
// common case).
type BucketProfile = []float64

var (
	// BucketsLatency — for hot-path latencies (web requests, DB
	// queries, Msg dispatch + diff). Spans 1ms to 5s with eight
	// boundaries. Most observations land in 1-100ms; the long-tail
	// 5s bucket catches genuine outliers without skewing the p99
	// histogram.
	BucketsLatency BucketProfile = []float64{
		0.001, 0.005, 0.010, 0.050,
		0.100, 0.500, 1.0, 5.0,
	}

	// BucketsDuration — for longer-running ops (jobs, large queries,
	// file uploads/downloads). Spans 10ms to 5min. Critical for
	// Std.Jobs latency dashboards where a job can legitimately take
	// minutes.
	BucketsDuration BucketProfile = []float64{
		0.010, 0.050, 0.100, 0.500,
		1.0, 5.0, 30.0, 60.0, 300.0,
	}

	// BucketsBytes — for payload / body sizes. Spans 100 B to 100
	// MB. Logarithmic-ish spacing to give equal-resolution coverage
	// across the typical web range.
	BucketsBytes BucketProfile = []float64{
		100, 1000, 10000, 100000,
		1000000, 10000000, 100000000,
	}
)

// MetricBuckets — canonical metric-name → BucketProfile mapping.
// Read by the Store at series construction. Adding a new Sky kernel
// metric requires picking one of these three profiles. User code
// that calls Observe on an un-mapped metric name gets BucketsLatency.
var MetricBuckets = map[string]BucketProfile{
	// HTTP / Live hot paths
	"sky_live_request_seconds": BucketsLatency,
	"sky_live_msg_seconds":     BucketsLatency,
	// Database
	"sky_db_query_seconds": BucketsLatency,
	// Background jobs (Phase 1.3)
	"sky_jobs_duration_seconds": BucketsDuration,
	// Payload sizes
	"sky_http_response_bytes": BucketsBytes,
	"sky_http_request_bytes":  BucketsBytes,
}

// bucketsFor returns the profile a metric name maps to, falling back
// to BucketsLatency when not registered. Called at series-construction
// time only; lookups don't happen on the hot path.
func bucketsFor(name string) BucketProfile {
	if profile, ok := MetricBuckets[name]; ok {
		return profile
	}
	return BucketsLatency
}
