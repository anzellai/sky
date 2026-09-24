//go:build !js

package rt

import (
	"strings"
	"testing"
)

// TestDevBannerStaysOffTheBottomBar — the dev "Console" badge must not sit over
// an app's controls. It used to be pinned bottom-right (`right:12px;
// bottom:12px`), which is exactly where an app puts its primary action: a chat
// composer's Send button, a form's Submit, a cart's Checkout. In development the
// badge covered the Send button of the Sky.Spa chat example and intercepted the
// click. It is now a compact tab on the right EDGE, vertically centred, clear of
// both the header bar and the bottom bar.
func TestDevBannerStaysOffTheBottomBar(t *testing.T) {
	t.Setenv("ENV", "development")
	t.Setenv("SKY_ENV", "")
	t.Setenv("SKY_DEV_BANNER", "")
	b := devBannerHTML()
	if b == "" {
		t.Fatal("dev banner absent in development; the test is vacuous")
	}
	i := strings.Index(b, `style="`)
	if i < 0 {
		t.Fatalf("dev banner has no inline style:\n%s", b)
	}
	style := b[i+len(`style="`):]
	style = style[:strings.Index(style, `"`)]
	if strings.Contains(style, "bottom:") || strings.Contains(style, "top:0") {
		t.Fatalf("dev banner is pinned to the top or bottom bar, where app controls live:\n%s", style)
	}
	for _, want := range []string{"position:fixed", "right:0", "top:50%"} {
		if !strings.Contains(style, want) {
			t.Fatalf("dev banner style lacks %q (a right-edge, vertically centred tab):\n%s", want, style)
		}
	}
}
