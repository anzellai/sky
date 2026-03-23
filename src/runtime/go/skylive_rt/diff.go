package skylive_rt

import "encoding/json"

// Patch represents a DOM mutation to apply on the client.
type Patch struct {
	ID     string             `json:"id"`               // sky-id of the target element
	Text   *string            `json:"text,omitempty"`    // Replace textContent
	HTML   *string            `json:"html,omitempty"`    // Replace innerHTML
	Attrs  map[string]*string `json:"attrs,omitempty"`   // Set/remove attributes (nil value = remove)
	Remove bool               `json:"remove,omitempty"`  // Remove the element
	Append *string            `json:"append,omitempty"`  // Append HTML as last child
}

// Diff computes the patches needed to transform oldTree into newTree.
// V1 approach: compare nodes by sky-id. If structure differs, replace
// the entire subtree via innerHTML.
func Diff(oldTree, newTree *VNode) []Patch {
	var patches []Patch
	diffNodes(oldTree, newTree, &patches)
	return patches
}

func diffNodes(old, new_ *VNode, patches *[]Patch) {
	if old == nil && new_ == nil {
		return
	}

	// Node added (shouldn't happen at top level, but handle gracefully)
	if old == nil {
		return
	}

	// Node removed
	if new_ == nil {
		if old.SkyID != "" {
			*patches = append(*patches, Patch{ID: old.SkyID, Remove: true})
		}
		return
	}

	// Both are text nodes
	if old.Tag == "" && new_.Tag == "" {
		// Text nodes don't have sky-ids; they're handled via parent
		return
	}

	// Tag changed — replace entire subtree
	if old.Tag != new_.Tag {
		if old.SkyID != "" {
			html := RenderToString(new_)
			*patches = append(*patches, Patch{ID: old.SkyID, HTML: &html})
		}
		return
	}

	// Same tag, same sky-id — diff attributes and children
	if old.SkyID != "" {
		// Diff attributes
		attrChanges := diffAttrs(old.Attrs, new_.Attrs)
		// Textarea: value is rendered as textContent, not an HTML attribute.
		// Emit a Text patch instead of an Attrs patch for the value.
		if old.Tag == "textarea" {
			oldVal, _ := old.Attrs["value"]
			newVal, _ := new_.Attrs["value"]
			if oldVal != newVal {
				*patches = append(*patches, Patch{ID: old.SkyID, Text: &newVal})
			}
			// Remove value from attrChanges — it's handled via Text patch
			delete(attrChanges, "value")
		}
		if len(attrChanges) > 0 {
			*patches = append(*patches, Patch{ID: old.SkyID, Attrs: attrChanges})
		}
	}

	// Diff children
	diffChildren(old, new_, patches)
}

func diffAttrs(oldAttrs, newAttrs map[string]string) map[string]*string {
	changes := make(map[string]*string)

	for k, newV := range newAttrs {
		if k == "sky-id" {
			continue // Skip internal attribute
		}
		oldV, exists := oldAttrs[k]
		if !exists || oldV != newV {
			v := newV
			changes[k] = &v
		}
	}

	for k := range oldAttrs {
		if k == "sky-id" {
			continue
		}
		if _, exists := newAttrs[k]; !exists {
			changes[k] = nil // Remove attribute
		}
	}

	if len(changes) == 0 {
		return nil
	}
	return changes
}

func diffChildren(old, new_ *VNode, patches *[]Patch) {
	oldLen := len(old.Children)
	newLen := len(new_.Children)
	minLen := oldLen
	if newLen < minLen {
		minLen = newLen
	}

	// Check if children text content changed (common case: single text child)
	if oldLen == 1 && newLen == 1 &&
		old.Children[0].Tag == "" && new_.Children[0].Tag == "" {
		if old.Children[0].Text != new_.Children[0].Text {
			if old.SkyID != "" {
				text := new_.Children[0].Text
				*patches = append(*patches, Patch{ID: old.SkyID, Text: &text})
			}
		}
		return
	}

	// Compare matching children — recurse into all children with sky-ids,
	// and detect structural changes for children without sky-ids.
	structuralChange := false
	for i := 0; i < minLen; i++ {
		oldChild := old.Children[i]
		newChild := new_.Children[i]

		// Both text nodes — check if content changed
		if oldChild.Tag == "" && newChild.Tag == "" {
			if oldChild.Text != newChild.Text {
				structuralChange = true
			}
			continue
		}

		// Tag mismatch — structural change
		if oldChild.Tag != newChild.Tag {
			structuralChange = true
			continue
		}

		// Same tag with sky-id — recurse (always, regardless of structural changes)
		if oldChild.SkyID != "" {
			diffNodes(oldChild, newChild, patches)
			continue
		}

		// Same tag without sky-id (raw nodes, unstyled elements, etc.)
		if RenderToString(oldChild) != RenderToString(newChild) {
			structuralChange = true
		}
	}

	// If children count changed or structural change detected, re-render the whole parent
	if structuralChange || oldLen != newLen {
		if old.SkyID != "" {
			var childHTML string
			for _, child := range new_.Children {
				childHTML += RenderToString(child)
			}
			*patches = append(*patches, Patch{ID: old.SkyID, HTML: &childHTML})
		}
	}
}

// PatchesToJSON serializes patches to JSON bytes.
func PatchesToJSON(patches []Patch) ([]byte, error) {
	return json.Marshal(patches)
}
