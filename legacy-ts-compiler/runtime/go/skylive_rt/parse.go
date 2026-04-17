package skylive_rt

import (
	"strings"
)

// ParseHTML parses an HTML string into a VNode tree.
// This is a simple parser for Sky-generated HTML which is well-formed.
// Used in V1 where view() returns HTML strings; future versions will
// emit VNode construction directly.
func ParseHTML(html string) *VNode {
	html = strings.TrimSpace(html)
	if html == "" {
		return TextNode("")
	}

	// If it doesn't start with <, it's a text node
	if !strings.HasPrefix(html, "<") {
		return TextNode(html)
	}

	nodes := parseNodes(html)
	if len(nodes) == 0 {
		return TextNode("")
	}
	if len(nodes) == 1 {
		return nodes[0]
	}
	// Multiple top-level nodes: wrap in a div
	return &VNode{Tag: "div", Children: nodes}
}

func parseNodes(html string) []*VNode {
	var nodes []*VNode
	pos := 0

	for pos < len(html) {
		// Skip whitespace between tags
		for pos < len(html) && (html[pos] == ' ' || html[pos] == '\n' || html[pos] == '\r' || html[pos] == '\t') {
			pos++
		}
		if pos >= len(html) {
			break
		}

		if html[pos] == '<' {
			// Check for closing tag
			if pos+1 < len(html) && html[pos+1] == '/' {
				break // Closing tag signals end of children
			}

			node, newPos := parseElement(html, pos)
			if node != nil {
				nodes = append(nodes, node)
			}
			if newPos <= pos {
				pos++ // Prevent infinite loop
			} else {
				pos = newPos
			}
		} else {
			// Text node
			end := strings.IndexByte(html[pos:], '<')
			if end == -1 {
				text := html[pos:]
				if strings.TrimSpace(text) != "" {
					nodes = append(nodes, TextNode(unescapeHTML(text)))
				}
				pos = len(html)
			} else {
				text := html[pos : pos+end]
				if strings.TrimSpace(text) != "" {
					nodes = append(nodes, TextNode(unescapeHTML(text)))
				}
				pos = pos + end
			}
		}
	}

	return nodes
}

func parseElement(html string, pos int) (*VNode, int) {
	if pos >= len(html) || html[pos] != '<' {
		return nil, pos
	}

	// Find end of opening tag
	tagEnd := strings.IndexByte(html[pos:], '>')
	if tagEnd == -1 {
		return nil, len(html)
	}
	tagEnd += pos

	openTag := html[pos+1 : tagEnd]

	// Self-closing tag
	selfClosing := false
	if strings.HasSuffix(openTag, "/") {
		openTag = openTag[:len(openTag)-1]
		selfClosing = true
	}

	// Parse tag name and attributes
	parts := strings.Fields(openTag)
	if len(parts) == 0 {
		return nil, tagEnd + 1
	}

	tagName := parts[0]
	attrs := parseAttrs(parts[1:], html[pos+1+len(tagName):tagEnd])

	node := &VNode{
		Tag:   tagName,
		Attrs: attrs,
	}

	// Extract sky-id if present (for VNode trees loaded from persistent stores)
	if skyID, ok := attrs["sky-id"]; ok {
		node.SkyID = skyID
	}

	pos = tagEnd + 1

	// Void elements don't have children or closing tags
	if selfClosing || voidElements[tagName] {
		return node, pos
	}

	// Parse children until closing tag
	node.Children = parseChildNodes(html, &pos, tagName)

	// Skip closing tag
	closingTag := "</" + tagName + ">"
	if pos+len(closingTag) <= len(html) && html[pos:pos+len(closingTag)] == closingTag {
		pos += len(closingTag)
	}

	return node, pos
}

func parseChildNodes(html string, pos *int, parentTag string) []*VNode {
	var children []*VNode
	closingTag := "</" + parentTag + ">"

	for *pos < len(html) {
		// Check for closing tag
		if *pos+len(closingTag) <= len(html) && html[*pos:*pos+len(closingTag)] == closingTag {
			break
		}

		if html[*pos] == '<' {
			if *pos+1 < len(html) && html[*pos+1] == '/' {
				break
			}
			child, newPos := parseElement(html, *pos)
			if child != nil {
				children = append(children, child)
			}
			if newPos <= *pos {
				*pos++
			} else {
				*pos = newPos
			}
		} else {
			// Text content
			end := strings.IndexByte(html[*pos:], '<')
			if end == -1 {
				text := html[*pos:]
				if text != "" {
					children = append(children, TextNode(unescapeHTML(text)))
				}
				*pos = len(html)
			} else {
				text := html[*pos : *pos+end]
				if text != "" {
					children = append(children, TextNode(unescapeHTML(text)))
				}
				*pos = *pos + end
			}
		}
	}

	return children
}

func parseAttrs(parts []string, raw string) map[string]string {
	attrs := make(map[string]string)
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "/" {
		return attrs
	}

	// Simple attribute parser: handles key='value', key="value", key=value, and key (boolean)
	i := 0
	for i < len(raw) {
		// Skip whitespace
		for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t' || raw[i] == '\n') {
			i++
		}
		if i >= len(raw) {
			break
		}

		// Read key
		keyStart := i
		for i < len(raw) && raw[i] != '=' && raw[i] != ' ' && raw[i] != '\t' && raw[i] != '\n' {
			i++
		}
		key := raw[keyStart:i]
		if key == "" || key == "/" {
			i++
			continue
		}

		// Check for =
		if i < len(raw) && raw[i] == '=' {
			i++ // skip =
			if i < len(raw) && (raw[i] == '\'' || raw[i] == '"') {
				// Quoted value
				quote := raw[i]
				i++ // skip opening quote
				valStart := i
				for i < len(raw) && raw[i] != quote {
					i++
				}
				attrs[key] = unescapeAttr(raw[valStart:i])
				if i < len(raw) {
					i++ // skip closing quote
				}
			} else {
				// Unquoted value
				valStart := i
				for i < len(raw) && raw[i] != ' ' && raw[i] != '\t' {
					i++
				}
				attrs[key] = raw[valStart:i]
			}
		} else {
			// Boolean attribute
			attrs[key] = ""
		}
	}

	return attrs
}

func unescapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	return s
}

func unescapeAttr(s string) string {
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	return s
}
