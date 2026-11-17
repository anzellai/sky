//go:build cgo && darwin

package rt

import "testing"

// Issue #328: the desktop page shell writes the base reset first in <head>.
func TestBaseResetFirstOnWebviewPage(t *testing.T) {
	assertResetFirstInHead(t, webviewPageWrap("<p>hi</p>"), "")
}
