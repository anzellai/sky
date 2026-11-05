//go:build !js

package rt

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

type aiServeLoadingRoundTrip func(*http.Request) (*http.Response, error)

func (f aiServeLoadingRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Model the precise published-but-loading state ensureLoaded creates. Virtual
// scheduling waits until every goroutine is durably blocked; no sleep can
// accidentally let a request arrive only after loading already completed.
func TestAiTierWaitsForResidentLoadBeforeSendingCompletions(t *testing.T) {
	for _, failLoad := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failLoad], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tier := newAiTier(aiTierConfig{engineURL: "http://engine.invalid", baseName: "base"})
				loading := make(chan struct{})
				resident := &aiResident{served: "support-v2", loading: loading}
				tier.resident[resident.served] = resident
				var engineCalls atomic.Int32
				var engineLoaded atomic.Bool
				tier.client.Transport = aiServeLoadingRoundTrip(func(*http.Request) (*http.Response, error) {
					engineCalls.Add(1)
					status := http.StatusOK
					if !engineLoaded.Load() {
						status = http.StatusNotFound
					}
					return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("synthetic completion"))}, nil
				})
				request := httptest.NewRequest("POST", "http://tier.invalid/v1/chat/completions", nil)
				response := httptest.NewRecorder()
				done := make(chan struct{})
				go func() {
					defer close(done)
					tier.proxy(response, request, []byte(`{"model":"support-v2"}`), resident.served)
				}()
				synctest.Wait()
				select {
				case <-done:
					t.Error("request completed before its adapter load finished")
				default:
				}
				if engineCalls.Load() != 0 {
					t.Error("a loading resident was forwarded to the engine")
				}
				tier.mu.Lock()
				if resident.inflight != 1 {
					t.Errorf("waiting request has %d leases, want one", resident.inflight)
				}
				if failLoad {
					resident.loadErr = errors.New("synthetic load failure")
				} else {
					engineLoaded.Store(true)
				}
				close(loading)
				resident.loading = nil
				tier.mu.Unlock()
				synctest.Wait()
				<-done
				wantStatus, wantCalls := http.StatusOK, int32(1)
				if failLoad {
					wantStatus, wantCalls = http.StatusServiceUnavailable, 0
				}
				if response.Code != wantStatus || engineCalls.Load() != wantCalls {
					t.Errorf("load outcome: status %d, engine calls %d; want %d, %d", response.Code, engineCalls.Load(), wantStatus, wantCalls)
				}
				tier.mu.Lock()
				remaining := resident.inflight
				tier.mu.Unlock()
				if remaining != 0 {
					t.Errorf("completed request retained %d leases", remaining)
				}
			})
		})
	}
}

// Canceling one waiting request must neither forward it nor cancel the shared
// background load that other requests can still await.
func TestAiTierCanceledResidentLoadWaitReleasesOnlyItsRequestLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tier := newAiTier(aiTierConfig{engineURL: "http://engine.invalid", baseName: "base"})
		loading, finishLoad := make(chan struct{}), make(chan struct{})
		resident := &aiResident{served: "support-v2", loading: loading}
		tier.resident[resident.served] = resident
		var engineCalls atomic.Int32
		tier.client.Transport = aiServeLoadingRoundTrip(func(*http.Request) (*http.Response, error) {
			engineCalls.Add(1)
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("synthetic completion"))}, nil
		})
		// Stand in for the independent ensureLoaded worker. It is released only
		// after the canceled request has finished and its lease has been checked.
		loadDone := make(chan struct{})
		go func() {
			defer close(loadDone)
			<-finishLoad
			tier.mu.Lock()
			close(loading)
			resident.loading = nil
			tier.mu.Unlock()
		}()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		request := httptest.NewRequest("POST", "http://tier.invalid/v1/chat/completions", nil).WithContext(ctx)
		response := httptest.NewRecorder()
		requestDone := make(chan struct{})
		go func() {
			defer close(requestDone)
			tier.proxy(response, request, []byte(`{"model":"support-v2"}`), resident.served)
		}()
		synctest.Wait()
		tier.mu.Lock()
		parkedLeases := resident.inflight
		tier.mu.Unlock()
		if parkedLeases != 1 {
			t.Errorf("pending request has %d leases, want one", parkedLeases)
		}
		cancel()
		synctest.Wait()
		select {
		case <-requestDone:
		default:
			t.Error("canceled request remained blocked on the shared load")
		}
		if engineCalls.Load() != 0 {
			t.Error("canceled load waiter reached the engine")
		}
		tier.mu.Lock()
		leases, pending, same := resident.inflight, resident.loading == loading, tier.resident[resident.served] == resident
		tier.mu.Unlock()
		if leases != 0 || !pending || !same {
			t.Errorf("cancellation changed shared load or retained lease: leases=%d pending=%v sameResident=%v", leases, pending, same)
		}
		select {
		case <-loading:
			t.Error("request cancellation completed the shared load")
		default:
		}
		select {
		case <-loadDone:
			t.Error("background load stopped before explicit release")
		default:
		}
		close(finishLoad)
		synctest.Wait()
		<-loadDone
		<-requestDone
		tier.mu.Lock()
		leases, pending = resident.inflight, resident.loading != nil
		tier.mu.Unlock()
		if leases != 0 || pending || engineCalls.Load() != 0 {
			t.Error("background completion replayed canceled request or retained its lease")
		}
	})
}
