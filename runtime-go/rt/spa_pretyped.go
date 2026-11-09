package rt

// What the user typed before the Sky.Spa client was live (v0.29.7).
//
// The server renders the first paint, and the page shows it before the wasm
// client runs. A person (or a password manager, autofill, a test driver) can
// type into its fields in that time. The browser keeps two things for every
// form control: what the server rendered (defaultValue, defaultChecked,
// defaultSelected) and what the control holds now (value, checked, selected).
// When they differ, the user changed the control. So the client reads that
// difference once, before its first paint, puts the values back after the
// paint, and sends each changed control its own input and change events once
// it is live: the app's handlers see the text as if it was typed then.
//
// This is a check of state, not a record of events. It does not depend on
// when the text arrived, on how it arrived, or on the data-sky-hydrating
// marker (which a 12 s safety net removes on a slow start).
//
// The decisions are pure and live here, so a host test covers them
// (spa_pretyped_test.go). spa_pretyped_wasm.go reads and writes the DOM.

// spaPretypedSkip lists the input types that hold no typed value.
var spaPretypedSkip = map[string]bool{
	"submit": true, "button": true, "reset": true, "image": true, "hidden": true, "file": true,
}

// spaPretypedKind says how a control is compared: "text" (value against
// defaultValue), "check" (checked against defaultChecked), "select", or ""
// for a control that holds no typed value.
func spaPretypedKind(tag, typ string) string {
	switch tag {
	case "TEXTAREA":
		return "text"
	case "SELECT":
		return "select"
	case "INPUT":
		if spaPretypedSkip[typ] {
			return ""
		}
		if typ == "checkbox" || typ == "radio" {
			return "check"
		}
		return "text"
	}
	return ""
}

// spaOptState is one <option>: whether it is selected now, whether the server
// marked it selected, and whether it is disabled.
type spaOptState struct {
	Selected, DefaultSelected, Disabled bool
}

// spaSelectDirty reports whether the user changed a <select>. A multiple
// select starts with exactly the options the server marked. A single select
// with no marked option shows its first option that is not disabled (HTML
// "selectedness setting"), so that option being selected is not a change.
func spaSelectDirty(multiple bool, opts []spaOptState) bool {
	if multiple {
		for _, o := range opts {
			if o.Selected != o.DefaultSelected {
				return true
			}
		}
		return false
	}
	want := -1
	for i, o := range opts {
		if o.DefaultSelected {
			want = i // the last marked option wins, as in the browser
		}
	}
	if want < 0 {
		for i, o := range opts {
			if !o.Disabled {
				want = i
				break
			}
		}
	}
	got := -1
	for i, o := range opts {
		if o.Selected {
			got = i
			break
		}
	}
	return got != want
}
