//go:build !js

package rt

import (
	"html"
	"regexp"
	"strings"
)

var tuiRawTagRE = regexp.MustCompile(`<[^>]*>`)

// tuiRawText is the terminal rendering of a Std.Ui `Raw` node (Ui.html
// wrapping a Std.Html node): its text content on one line. A terminal cannot
// draw markup, so text leaves are kept, an HRaw string loses its tags and
// entities are decoded, and whitespace runs collapse to one space. It used to
// be the literal "[raw]", which hid the content.
func tuiRawText(node any) string {
	var parts []string
	var walk func(vn VNode)
	walk = func(vn VNode) {
		switch vn.Kind {
		case "text":
			parts = append(parts, vn.Text)
		case "raw":
			parts = append(parts, html.UnescapeString(tuiRawTagRE.ReplaceAllString(vn.Text, " ")))
		default:
			for _, c := range vn.Children {
				walk(c)
			}
		}
	}
	walk(HtmlToVNode(node))
	return sanitiseString(strings.Join(strings.Fields(strings.Join(parts, " ")), " "))
}
