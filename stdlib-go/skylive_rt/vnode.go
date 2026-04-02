package skylive_rt

import (
	"fmt"
	"reflect"
	"strings"
)

// VNode represents a virtual DOM node for server-side rendering and diffing.
type VNode struct {
	Tag      string            // HTML tag name (e.g., "div", "span"). Empty for text nodes.
	Attrs    map[string]string // HTML attributes including sky-* event attributes.
	Children []*VNode          // Child nodes.
	Text     string            // Text content (only for text nodes where Tag == "").
	SkyID    string            // Compiler-assigned ID for diffing. Empty for static nodes.
	Key      string            // Optional key for list diffing.
}

// TextNode creates a text VNode.
func TextNode(text string) *VNode {
	return &VNode{Text: text}
}

// Element creates an element VNode.
func Element(tag string, attrs map[string]string, children []*VNode) *VNode {
	return &VNode{Tag: tag, Attrs: attrs, Children: children}
}

// RawNode creates a raw HTML node (not escaped).
func RawNode(html string) *VNode {
	return &VNode{Tag: "__raw__", Text: html}
}

// VoidElement creates a self-closing element (input, br, hr, img, meta, link).
func VoidElement(tag string, attrs map[string]string) *VNode {
	return &VNode{Tag: tag, Attrs: attrs}
}

var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

// AssignSkyIDs assigns path-based sky-id attributes to all element nodes.
// IDs are based on tree position (e.g., "s0", "s0-1", "s0-1-2") so they
// remain stable when siblings change. This prevents ID collisions during
// DOM patching when the tree structure changes (e.g., auth state change).
func AssignSkyIDs(node *VNode) {
	assignIDs(node, "s0")
}

func assignIDs(node *VNode, path string) {
	if node.Tag != "" && node.Tag != "__raw__" {
		node.SkyID = path
		if node.Attrs == nil {
			node.Attrs = make(map[string]string)
		}
		node.Attrs["sky-id"] = node.SkyID
	}
	childIdx := 0
	for _, child := range node.Children {
		if child.Tag != "" && child.Tag != "__raw__" {
			assignIDs(child, fmt.Sprintf("%s-%d", path, childIdx))
			childIdx++
		} else {
			// Text nodes and raw nodes don't get IDs but still count for positioning
			assignIDs(child, fmt.Sprintf("%s-%d", path, childIdx))
			childIdx++
		}
	}
}

// RenderToString renders a VNode tree to an HTML string.
func RenderToString(node *VNode) string {
	var sb strings.Builder
	renderNode(&sb, node)
	return sb.String()
}

func renderNode(sb *strings.Builder, node *VNode) {
	if node == nil {
		return
	}

	// Text node
	if node.Tag == "" {
		sb.WriteString(escapeHTML(node.Text))
		return
	}

	// Raw HTML node
	if node.Tag == "__raw__" {
		sb.WriteString(node.Text)
		return
	}

	// Opening tag
	sb.WriteByte('<')
	sb.WriteString(node.Tag)

	// Attributes — for textarea, skip "value" (rendered as text content instead)
	var textareaVal string
	var hasTextareaVal bool
	for k, v := range node.Attrs {
		if node.Tag == "textarea" && k == "value" {
			textareaVal = v
			hasTextareaVal = true
			continue
		}
		sb.WriteByte(' ')
		sb.WriteString(k)
		sb.WriteString("='")
		sb.WriteString(escapeAttr(v))
		sb.WriteByte('\'')
	}

	// Void elements (self-closing)
	if voidElements[node.Tag] {
		sb.WriteByte('>')
		return
	}

	sb.WriteByte('>')

	// Textarea: render value as text content (browsers ignore value attribute)
	if node.Tag == "textarea" && hasTextareaVal {
		sb.WriteString(escapeHTML(textareaVal))
		sb.WriteString("</textarea>")
		return
	}

	// Children
	for _, child := range node.Children {
		renderNode(sb, child)
	}

	// Closing tag
	sb.WriteString("</")
	sb.WriteString(node.Tag)
	sb.WriteByte('>')
}

// RenderFullPage renders a complete HTML page with the Sky.Live shell.
func RenderFullPage(bodyContent *VNode, title string, sid string) string {
	var sb strings.Builder
	sb.WriteString("<!DOCTYPE html>\n<html>\n<head>\n")
	sb.WriteString("<meta charset='utf-8'>\n")
	sb.WriteString("<meta name='viewport' content='width=device-width, initial-scale=1'>\n")
	sb.WriteString("<title>")
	sb.WriteString(escapeHTML(title))
	sb.WriteString("</title>\n")
	sb.WriteString("<script src='/_sky/live.js' defer></script>\n")
	// Loading overlay: shown during server round-trips, hidden on response/SSE.
	// Users can override styles by targeting #sky-loader and #sky-loader .sky-spinner.
	sb.WriteString(`<style>
#sky-loader{position:fixed;top:0;left:0;width:100%;height:100%;z-index:99999;display:none;align-items:center;justify-content:center;background:rgba(255,255,255,0.15);backdrop-filter:blur(1px);pointer-events:all;transition:opacity .15s}
#sky-loader.sky-loading{display:flex}
#sky-loader .sky-spinner{width:28px;height:28px;border:3px solid rgba(0,0,0,0.1);border-top-color:#666;border-radius:50%;animation:sky-spin .6s linear infinite}
@keyframes sky-spin{to{transform:rotate(360deg)}}
</style>`)
	sb.WriteString("\n</head>\n<body>\n")
	sb.WriteString("<div id='sky-loader'><div class='sky-spinner'></div></div>\n")
	sb.WriteString("<div sky-root='")
	sb.WriteString(sid)
	sb.WriteString("'>\n")
	sb.WriteString(RenderToString(bodyContent))
	sb.WriteString("\n</div>\n</body>\n</html>")
	return sb.String()
}

// MapToVNode converts a Sky VNode record (map[string]any) into a *VNode.
// Sky's Html functions produce records like {tag, attrs, children, text}.
// Attrs are lists of Tuple2{V0: key, V1: value}.
// Falls back to ParseHTML for legacy string values.
func MapToVNode(v any) *VNode {
	if v == nil {
		return TextNode("")
	}
	// Legacy fallback: if view returns a string, parse it
	if s, ok := v.(string); ok {
		return ParseHTML(s)
	}
	rec, ok := v.(map[string]any)
	if !ok {
		return TextNode(fmt.Sprintf("%v", v))
	}

	tag, _ := rec["tag"].(string)
	text, _ := rec["text"].(string)

	// Text node
	if tag == "" {
		return TextNode(text)
	}
	// Raw HTML node
	if tag == "__raw__" {
		return RawNode(text)
	}

	// Parse attributes from []any of tuples
	attrs := make(map[string]string)
	if attrList, ok := rec["attrs"].([]any); ok {
		for _, a := range attrList {
			// Sky tuples: map (legacy), struct with V0/V1, or struct with Fields
			if m, ok := a.(map[string]any); ok {
				key, _ := m["V0"].(string)
				val, _ := m["V1"].(string)
				if key != "" {
					attrs[key] = val
				}
			} else {
				// Try struct via reflect (SkyTuple2 or SkyADT with Fields)
				key, val := extractTupleKV(a)
				if key != "" {
					attrs[key] = val
				}
			}
		}
	}

	// Parse children recursively
	var children []*VNode
	if childList, ok := rec["children"].([]any); ok {
		children = make([]*VNode, 0, len(childList))
		for _, c := range childList {
			children = append(children, MapToVNode(c))
		}
	}

	return &VNode{Tag: tag, Attrs: attrs, Children: children}
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// extractTupleKV extracts key/value from a struct with V0/V1 or Fields fields.
// Handles SkyTuple2{V0, V1} and SkyADT{Fields: []any{k, v}} from compiled Sky code.
func extractTupleKV(v any) (string, string) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Struct {
		return "", ""
	}
	// Try V0/V1 fields (SkyTuple2)
	v0 := rv.FieldByName("V0")
	v1 := rv.FieldByName("V1")
	if v0.IsValid() && v1.IsValid() {
		return fmt.Sprintf("%v", v0.Interface()), fmt.Sprintf("%v", v1.Interface())
	}
	// Try Fields slice (SkyADT with Fields []any)
	fields := rv.FieldByName("Fields")
	if fields.IsValid() && fields.Kind() == reflect.Slice && fields.Len() >= 2 {
		return fmt.Sprintf("%v", fields.Index(0).Interface()), fmt.Sprintf("%v", fields.Index(1).Interface())
	}
	return "", ""
}

func escapeAttr(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "'", "&#39;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
