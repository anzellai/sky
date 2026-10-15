//go:build !js

package rt

import (
	"sync"
	"testing"
)

// TestEnvRefreshHooksAreRaceFreeAgainstReaders is the regression for the data
// race `go test -race` reported between TestSeededDefaultIsNotInheritedByChildren
// (SetPortDefault -> SetSkyDefault -> the env-prefix refresh hooks, writing
// logThreshold / logJSON) and a log line written on another goroutine (a
// Durable.persist goroutine logging a classified panic).
//
// The race is in the product, not only in the test: the refresh hooks run from
// SetSkyDefault, SetEnvPrefix and ApplyConfig, and `Sky.Config.apply`
// (Config_apply) calls ApplyConfig from a running Task, while other goroutines
// log and open Sky.Live sessions. So every value a refresh hook writes must be
// read and written atomically. Run under -race: before the fix the detector
// reports a DATA RACE on logThreshold / logJSON / sseChanBuffer and the test
// fails.
func TestEnvRefreshHooksAreRaceFreeAgainstReaders(t *testing.T) {
	name := skyEnvName("LIVE_PORT")
	cleanName(t, name)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			// The readers: a log line (logThreshold, logJSON) and a new SSE
			// channel (sseChanBuffer).
			logEmit(logLevelDebug, "debug", "env-refresh-race-probe", nil)
			_ = make(chan sseFrame, sseBufferSize())
		}
	}()
	for i := 0; i < 200; i++ {
		SetPortDefault("8000") // runs every refresh hook
	}
	close(stop)
	wg.Wait()
}
