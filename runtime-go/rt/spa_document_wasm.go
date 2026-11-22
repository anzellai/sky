//go:build js

package rt

// The Sky.Spa client half of the <html> attributes (Spa.withDocument,
// document_attrs.go). After each render the client computes the set from the
// model once and compares it with the set it applied last. An equal set does
// nothing. A different set is applied to document.documentElement: each
// attribute is set, and each one the client set before that the new set does
// not have is removed. The client never touches an attribute it did not set,
// so the runtime's data-sky-hydrating marker stays until the client clears
// it. On a server-rendered page the first apply finds the same values the
// server wrote and only records the names.

import "syscall/js"

var (
	// spaDocument is the app's `model -> List Document.Attribute` (nil: the
	// app has no Spa.withDocument).
	spaDocument any
	// spaDocApplied is the set applied last; spaDocValid says one was.
	spaDocApplied []docAttr
	spaDocValid   bool
)

// spaResetDocument reads the builder at boot and forgets the last set.
func spaResetDocument(cfg any) {
	spaDocument = fieldOrNil(cfg, "Document")
	spaDocApplied = nil
	spaDocValid = false
}

// spaApplyDocument applies the set of the current model when it differs from
// the set applied last. Called once per render.
func spaApplyDocument() {
	if spaDocument == nil {
		return
	}
	next := documentAttrs(spaDocument, spaModel)
	if next == nil {
		return // the function failed; documentAttrs logged it
	}
	next = withDefaultLang(next)
	if spaDocValid && sameDocAttrs(next, spaDocApplied) {
		return
	}
	el := js.Global().Get("document").Get("documentElement")
	if !el.Truthy() {
		return
	}
	keep := make(map[string]bool, len(next))
	for _, a := range next {
		keep[a.Name] = true
	}
	for _, a := range spaDocApplied {
		if !keep[a.Name] {
			el.Call("removeAttribute", a.Name)
		}
	}
	for _, a := range next {
		cur := el.Call("getAttribute", a.Name)
		if cur.Type() != js.TypeString || cur.String() != a.Value {
			el.Call("setAttribute", a.Name, a.Value)
		}
	}
	spaDocApplied = next
	spaDocValid = true
}
