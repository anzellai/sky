package rt

import "strings"

// Typing during a hold RPC, the render half (the queue half is
// spaSched.typingSources in spa_rpcqueue.go).
//
// A hold RPC holds every later Msg until its answer has run, and the
// keystrokes of a focused text box are among them: the box takes the letters
// while the model does not. The render that follows the answer, and the render
// of each held keystroke as the queue drains, come from a model that is BEHIND
// the box. Writing that model's text into the box rewinds it and clamps the
// caret, and the held keystrokes then run against a box that was rewritten, so
// letters land in the wrong places ("harbourside" came out scrambled).
//
// So while a control's own input Msgs wait in the queue, the render does not
// write its value (or its checked state). Nothing is lost: when the last of
// them has run, the model equals what the control shows, and the next render
// writes nothing (diffTrees' focused-input alignment, and applyAttrs skips an
// identical value). An arm that sets a DIFFERENT value (a trim, a reset) still
// writes it on that render, as it does with no hold. The order of the Msgs is
// not touched: each still runs once, in arrival order.

// spaTypedAttr reports whether attribute k is the control state a person's
// input sets (the value of a text box, the checked state of a box).
func spaTypedAttr(k string) bool {
	return k == "value" || k == "checked"
}

// spaKeepTyped returns patches without the value and checked writes to the
// controls in typing (sky-ids of controls whose input Msgs still wait). The
// ids are the ones the DOM holds now; a children patch that renames a kept
// subtree moves them, and the later patches of the batch use the new ids, so
// the set follows each rename in patch order. patches is not changed.
func spaKeepTyped(patches []Patch, typing map[string]bool) []Patch {
	if len(typing) == 0 || len(patches) == 0 {
		return patches
	}
	ids := make(map[string]bool, len(typing))
	for id := range typing {
		ids[id] = true
	}
	out := make([]Patch, 0, len(patches))
	for _, p := range patches {
		for _, k := range p.Kids {
			if k.Keep == "" || k.ID == "" || k.ID == k.Keep {
				continue
			}
			var moved []string
			for id := range ids {
				if id == k.Keep || strings.HasPrefix(id, k.Keep+".") {
					moved = append(moved, k.ID+id[len(k.Keep):])
				}
			}
			for _, id := range moved {
				ids[id] = true
			}
		}
		if !ids[p.ID] || p.Attrs == nil {
			out = append(out, p)
			continue
		}
		var attrs map[string]string
		dropped := false
		for k, v := range p.Attrs {
			if spaTypedAttr(k) {
				dropped = true
				continue
			}
			if attrs == nil {
				attrs = map[string]string{}
			}
			attrs[k] = v
		}
		if !dropped {
			out = append(out, p)
			continue
		}
		p.Attrs = attrs
		if attrs == nil && p.Text == nil && p.HTML == nil && p.Kids == nil && p.Replace == nil && !p.Remove {
			continue // only the typed state changed: nothing to send
		}
		out = append(out, p)
	}
	return out
}
