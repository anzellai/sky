package rt

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
	"time"
)

// failDurableStore in production hands the refusal to its fatal callback.
// When that callback returns (the Sky.Spa sign-out store records the error and
// refuses every signed session itself), the log must not then print the DEV
// fallback box: it contradicts the refusal on the next line.
func TestFailDurableStoreInProductionNeverPrintsTheDevBox(t *testing.T) {
	t.Setenv("ENV", "production")
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	var fatalMsg string
	st := failDurableStore("postgres", errors.New("no such database"), time.Minute, func(format string, args ...any) {
		fatalMsg = format
	})
	if st == nil {
		t.Fatal("failDurableStore must still return a store value")
	}
	if fatalMsg == "" {
		t.Fatal("production must report through the fatal callback")
	}
	if strings.Contains(buf.String(), "DEV fallback") {
		t.Fatalf("production printed the dev fallback box:\n%s", buf.String())
	}
}

// In dev the box is the whole message and must still appear.
func TestFailDurableStoreInDevPrintsTheDevBox(t *testing.T) {
	t.Setenv("ENV", "dev")
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	failDurableStore("postgres", errors.New("down"), time.Minute, func(string, ...any) {
		t.Fatal("dev must not call fatal")
	})
	if !strings.Contains(buf.String(), "DEV fallback") {
		t.Fatalf("dev did not print the fallback box:\n%s", buf.String())
	}
}
