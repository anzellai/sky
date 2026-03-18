package skylive_rt

import (
	"fmt"
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

// AssignSkyIDs assigns sequential sky-id attributes to all element nodes.
// For V1, every element gets a sky-id. Static analysis optimization comes later.
func AssignSkyIDs(node *VNode) {
	counter := 0
	assignIDs(node, &counter)
}

func assignIDs(node *VNode, counter *int) {
	if node.Tag != "" && node.Tag != "__raw__" {
		node.SkyID = fmt.Sprintf("s%d", *counter)
		if node.Attrs == nil {
			node.Attrs = make(map[string]string)
		}
		node.Attrs["sky-id"] = node.SkyID
		*counter++
	}
	for _, child := range node.Children {
		assignIDs(child, counter)
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

	// Attributes (sorted for deterministic output)
	for k, v := range node.Attrs {
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
	sb.WriteString("</head>\n<body>\n")
	sb.WriteString("<div sky-root='")
	sb.WriteString(sid)
	sb.WriteString("'>\n")
	sb.WriteString(RenderToString(bodyContent))
	sb.WriteString("\n</div>\n</body>\n</html>")
	return sb.String()
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func escapeAttr(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "'", "&#39;")
	return s
}
