package rt

import "testing"

func TestSpaPretypedKind(t *testing.T) {
	cases := []struct{ tag, typ, want string }{
		{"INPUT", "text", "text"},
		{"INPUT", "", "text"},
		{"INPUT", "email", "text"},
		{"INPUT", "password", "text"},
		{"INPUT", "checkbox", "check"},
		{"INPUT", "radio", "check"},
		{"INPUT", "file", ""},
		{"INPUT", "hidden", ""},
		{"INPUT", "submit", ""},
		{"TEXTAREA", "textarea", "text"},
		{"SELECT", "select-one", "select"},
		{"DIV", "", ""},
	}
	for _, c := range cases {
		if got := spaPretypedKind(c.tag, c.typ); got != c.want {
			t.Errorf("spaPretypedKind(%q, %q) = %q, want %q", c.tag, c.typ, got, c.want)
		}
	}
}

func TestSpaSelectDirty(t *testing.T) {
	o := func(sel, def, dis bool) spaOptState { return spaOptState{sel, def, dis} }
	cases := []struct {
		name     string
		multiple bool
		opts     []spaOptState
		want     bool
	}{
		// No option marked: the browser shows the first one. Not a change.
		{"unmarked, first shown", false, []spaOptState{o(true, false, false), o(false, false, false)}, false},
		// No option marked, first disabled: the browser shows the second.
		{"unmarked, first disabled", false, []spaOptState{o(false, false, true), o(true, false, false)}, false},
		{"unmarked, user picked second", false, []spaOptState{o(false, false, false), o(true, false, false)}, true},
		{"marked, unchanged", false, []spaOptState{o(false, false, false), o(true, true, false)}, false},
		{"marked, user picked first", false, []spaOptState{o(true, false, false), o(false, true, false)}, true},
		{"multiple, unchanged", true, []spaOptState{o(true, true, false), o(false, false, false)}, false},
		{"multiple, user added one", true, []spaOptState{o(true, true, false), o(true, false, false)}, true},
		{"multiple, none marked, none chosen", true, []spaOptState{o(false, false, false)}, false},
		{"empty select", false, nil, false},
	}
	for _, c := range cases {
		if got := spaSelectDirty(c.multiple, c.opts); got != c.want {
			t.Errorf("%s: spaSelectDirty = %v, want %v", c.name, got, c.want)
		}
	}
}
