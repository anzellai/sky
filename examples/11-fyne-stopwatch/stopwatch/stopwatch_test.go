package stopwatch

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

// waitText waits until read() returns want, or fails the test.
func waitText(t *testing.T, what string, read func() string, want func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var got string
		fyne.DoAndWait(func() { got = read() })
		if want(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: still %q", what, got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The window shows the display and both buttons, and the buttons drive the
// clock: Start runs it, Pause holds the time, Reset returns it to zero.
func TestContentIsAWiredStopwatch(t *testing.T) {
	test.NewTempApp(t)
	s := newStopwatch()
	w := test.NewTempWindow(t, s.content())

	for _, o := range []fyne.CanvasObject{s.display, s.toggle, s.reset} {
		if !isIn(w.Content(), o) {
			t.Fatalf("%T is not in the window", o)
		}
	}
	if s.display.Text != "00:00.0" || s.toggle.Text != "Start" || s.reset.Text != "Reset" {
		t.Fatalf("first paint: display %q, buttons %q %q", s.display.Text, s.toggle.Text, s.reset.Text)
	}

	test.Tap(s.toggle)
	if s.toggle.Text != "Pause" {
		t.Fatalf("after Start the button reads %q", s.toggle.Text)
	}
	running := waitText(t, "the clock never moved", func() string { return s.display.Text },
		func(v string) bool { return v >= "00:00.2" })

	test.Tap(s.toggle)
	if s.toggle.Text != "Start" {
		t.Fatalf("after Pause the button reads %q", s.toggle.Text)
	}
	var held string
	fyne.DoAndWait(func() { held = s.display.Text })
	if held < running {
		t.Fatalf("Pause went back in time: %q then %q", running, held)
	}
	time.Sleep(3 * tick)
	var later string
	fyne.DoAndWait(func() { later = s.display.Text })
	if later != held {
		t.Fatalf("a paused clock moved: %q then %q", held, later)
	}

	test.Tap(s.toggle)
	waitText(t, "a resumed clock did not continue", func() string { return s.display.Text },
		func(v string) bool { return v > held })
	test.Tap(s.reset)
	if s.toggle.Text != "Start" {
		t.Fatalf("after Reset the button reads %q", s.toggle.Text)
	}
	time.Sleep(3 * tick)
	waitText(t, "Reset did not return to zero", func() string { return s.display.Text },
		func(v string) bool { return v == "00:00.0" })
}

// isIn reports whether o is inside root.
func isIn(root, o fyne.CanvasObject) bool {
	if root == o {
		return true
	}
	if c, ok := root.(*fyne.Container); ok {
		for _, child := range c.Objects {
			if isIn(child, o) {
				return true
			}
		}
	}
	return false
}

func TestFormat(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                                     "00:00.0",
		1500 * time.Millisecond:               "00:01.5",
		61*time.Second + 300*time.Millisecond: "01:01.3",
		10 * time.Minute:                      "10:00.0",
	} {
		if got := format(d); got != want {
			t.Errorf("format(%v) = %q, want %q", d, got, want)
		}
	}
}
