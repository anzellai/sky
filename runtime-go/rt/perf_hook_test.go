package rt

import (
	"encoding/json"
	"testing"
)

func TestMemStatsLineCarriesTheCounters(t *testing.T) {
	before := erasedTypedConsCount.Load()
	_ = List_cons(1, []int{2, 3})
	_ = List_cons(1, []any{2, 3})
	var got struct {
		S struct {
			Mallocs         uint64 `json:"mallocs"`
			TotalAlloc      uint64 `json:"totalAlloc"`
			ErasedTypedCons uint64 `json:"erasedTypedCons"`
		} `json:"skyMemStats"`
	}
	if err := json.Unmarshal([]byte(memStatsLine()), &got); err != nil {
		t.Fatalf("exit line is not JSON: %v", err)
	}
	if got.S.Mallocs == 0 || got.S.TotalAlloc == 0 {
		t.Fatalf("exit line has no malloc counts: %+v", got.S)
	}
	// Only the typed tail counts; the []any tail is the erased fast path.
	if int64(got.S.ErasedTypedCons) != before+1 {
		t.Fatalf("erasedTypedCons = %d, want %d", got.S.ErasedTypedCons, before+1)
	}
}

func TestMemStatsOnExitIsOffByDefault(t *testing.T) {
	t.Setenv("SKY_RT_MEMSTATS_ON_EXIT", "")
	if memStatsOnExitEnabled() {
		t.Fatal("the exit line must be off unless SKY_RT_MEMSTATS_ON_EXIT=1")
	}
	t.Setenv("SKY_RT_MEMSTATS_ON_EXIT", "1")
	if !memStatsOnExitEnabled() {
		t.Fatal("SKY_RT_MEMSTATS_ON_EXIT=1 must turn the exit line on")
	}
}
