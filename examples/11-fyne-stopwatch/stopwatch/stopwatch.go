// Package stopwatch is the Go half of the 11-fyne-stopwatch example: the
// stopwatch's widgets and its clock.
//
// It is a local Go module. The Sky project adds it with
// `sky add ./stopwatch`, which records it in sky.toml and generates the
// typed binding `Example.Local.Stopwatch`. Sky creates the app and the
// window with the Fyne bindings and puts Content() in the window.
//
// The clock lives in Go because it needs a goroutine and a mutex, and the
// Start button's handler changes the button itself.
package stopwatch

import (
	"fmt"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// tick is how often the display is redrawn while the clock runs.
const tick = 100 * time.Millisecond

type stopwatch struct {
	mu      sync.Mutex
	running bool
	started time.Time     // when the current run began
	before  time.Duration // time counted by earlier runs
	stop    chan struct{} // closed to end the current run's ticker

	display *widget.Label
	toggle  *widget.Button
	reset   *widget.Button
}

// Content returns a new stopwatch: the time display, a Start / Pause button
// and a Reset button, laid out in one column.
func Content() fyne.CanvasObject {
	return newStopwatch().content()
}

func newStopwatch() *stopwatch {
	s := &stopwatch{}
	s.display = widget.NewLabel(format(0))
	s.display.Alignment = fyne.TextAlignCenter
	s.display.TextStyle = fyne.TextStyle{Monospace: true}
	s.toggle = widget.NewButton("Start", s.onToggle)
	s.toggle.Importance = widget.HighImportance
	s.reset = widget.NewButton("Reset", s.onReset)
	return s
}

func (s *stopwatch) content() fyne.CanvasObject {
	return container.NewVBox(s.display, container.NewGridWithColumns(2, s.toggle, s.reset))
}

// onToggle starts or pauses the clock. Fyne calls it on the UI goroutine.
func (s *stopwatch) onToggle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		s.pauseLocked()
		s.toggle.SetText("Start")
		s.display.SetText(format(s.before))
		return
	}
	s.running = true
	s.started = time.Now()
	s.stop = make(chan struct{})
	go s.run(s.stop)
	s.toggle.SetText("Pause")
}

// onReset stops the clock and sets it back to zero.
func (s *stopwatch) onReset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		s.pauseLocked()
	}
	s.before = 0
	s.toggle.SetText("Start")
	s.display.SetText(format(0))
}

func (s *stopwatch) pauseLocked() {
	s.running = false
	s.before += time.Since(s.started)
	close(s.stop)
}

// run redraws the display every tick until stop is closed. Widgets change
// only on the UI goroutine (fyne.Do).
func (s *stopwatch) run(stop chan struct{}) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.mu.Lock()
			if !s.running || s.stop != stop {
				s.mu.Unlock()
				return
			}
			text := format(s.before + time.Since(s.started))
			s.mu.Unlock()
			fyne.Do(func() {
				s.mu.Lock()
				defer s.mu.Unlock()
				if s.running && s.stop == stop {
					s.display.SetText(text)
				}
			})
		}
	}
}

// format shows a duration as minutes, seconds and tenths: 01:02.3.
func format(d time.Duration) string {
	tenths := int(d / (100 * time.Millisecond))
	return fmt.Sprintf("%02d:%02d.%d", tenths/600, (tenths/10)%60, tenths%10)
}
