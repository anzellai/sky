package sky_wrappers

// Stopwatch widget group — built in Go because:
//   1. Timer logic requires goroutines and mutexes
//   2. The toggle button's callback references itself (circular ref)
// All other fyne UI (app, window, layout) uses Sky's auto-generated bindings.

import (
	"fmt"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

// Shared stopwatch state, initialised once on first access.
var swOnce sync.Once
var swDisplay *widget.Label
var swToggleBtn *widget.Button
var swResetBtn *widget.Button

func initStopwatch() {
	sw := &stopwatch{}
	swDisplay = widget.NewLabel("00:00.0")
	swDisplay.Alignment = fyne.TextAlignCenter

	swToggleBtn = widget.NewButton("Start", func() {
		sw.toggle(swDisplay, swToggleBtn)
	})
	swResetBtn = widget.NewButton("Reset", func() {
		sw.reset(swDisplay, swToggleBtn)
	})
}

func SkyNewSize(w any, h any) any {
	return fyne.NewSize(float32(w.(int)), float32(h.(int)))
}

func StopwatchDisplay() any { swOnce.Do(initStopwatch); return swDisplay }
func StopwatchToggleBtn() any { swOnce.Do(initStopwatch); return swToggleBtn }
func StopwatchResetBtn() any { swOnce.Do(initStopwatch); return swResetBtn }

// ── Timer logic ────────────────────────────────────────────────

type stopwatch struct {
	mu      sync.Mutex
	running bool
	elapsed time.Duration
	ticker  *time.Ticker
}

func (s *stopwatch) toggle(label *widget.Label, btn *widget.Button) {
	s.mu.Lock()
	if s.running {
		s.running = false
		if s.ticker != nil {
			s.ticker.Stop()
		}
		btn.SetText("Start")
		s.mu.Unlock()
	} else {
		s.running = true
		s.ticker = time.NewTicker(100 * time.Millisecond)
		btn.SetText("Pause")
		s.mu.Unlock()

		go func() {
			for range s.ticker.C {
				s.mu.Lock()
				if !s.running {
					s.mu.Unlock()
					return
				}
				s.elapsed += 100 * time.Millisecond
				text := fmt.Sprintf("%02d:%02d.%d",
					int(s.elapsed.Minutes()),
					int(s.elapsed.Seconds())%60,
					int(s.elapsed.Milliseconds()/100)%10)
				s.mu.Unlock()
				label.SetText(text)
			}
		}()
	}
}

func (s *stopwatch) reset(label *widget.Label, btn *widget.Button) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		s.running = false
		if s.ticker != nil {
			s.ticker.Stop()
		}
	}
	s.elapsed = 0
	label.SetText("00:00.0")
	btn.SetText("Start")
}
