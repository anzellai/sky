//go:build cgo && darwin

package rt

// The Go half of universal links in the macOS desktop app. The Cocoa half
// (native_desktop_links_objc_darwin.go) hands every link the app receives to
// skyDesktopLinkIn, on the main thread. A link that arrives while the app
// launches opens as the window's first page; one that arrives while it runs
// navigates in place (desktopLinkInPlaceJS), or loads while the page is still
// loading.

/*
#include <stdlib.h>

void sky_links_install(void);
char *sky_links_hosts(void);
void sky_link_navigate(void *window, const char *target, const char *inPlace);
*/
import "C"

import (
	"strings"
	"sync"
	"unsafe"
)

var desktopLinks struct {
	mu      sync.Mutex
	hosts   []string
	base    string
	window  unsafe.Pointer // the NSWindow once the first page loads; nil before
	pending string         // a link that arrived before the first page
}

// desktopLinkTestHook runs once the window has loaded its first page. Only
// the `skytest_links` build sets it (native_desktop_links_testhook_darwin.go).
var desktopLinkTestHook func()

// desktopLinksInstall prepares link routing for a window over `base`. Call it
// before the webview library launches the app.
func desktopLinksInstall(base string) {
	var hosts []string
	if p := C.sky_links_hosts(); p != nil {
		hosts = strings.Split(C.GoString(p), "\n")
		C.free(unsafe.Pointer(p))
	}
	desktopLinks.mu.Lock()
	desktopLinks.hosts, desktopLinks.base = hosts, base
	desktopLinks.window, desktopLinks.pending = nil, ""
	desktopLinks.mu.Unlock()
	C.sky_links_install()
}

// desktopLinksStart records the window and returns the first page: a link the
// app was launched with, else `base`.
func desktopLinksStart(window unsafe.Pointer) string {
	desktopLinks.mu.Lock()
	defer desktopLinks.mu.Unlock()
	desktopLinks.window = window
	first := desktopLinks.base
	if desktopLinks.pending != "" {
		first, desktopLinks.pending = desktopLinks.pending, ""
	}
	return first
}

// desktopLinksStop forgets the window (it is being destroyed).
func desktopLinksStop() {
	desktopLinks.mu.Lock()
	desktopLinks.window = nil
	desktopLinks.mu.Unlock()
}

//export skyDesktopLinkIn
func skyDesktopLinkIn(url *C.char) {
	link := C.GoString(url)
	desktopLinks.mu.Lock()
	target := desktopLinkTarget(desktopLinks.hosts, desktopLinks.base, link)
	window := desktopLinks.window
	if target != "" && window == nil {
		desktopLinks.pending = target
	}
	desktopLinks.mu.Unlock()
	if target == "" || window == nil {
		return
	}
	ct := C.CString(target)
	cj := C.CString(desktopLinkInPlaceJS(target))
	defer C.free(unsafe.Pointer(ct))
	defer C.free(unsafe.Pointer(cj))
	C.sky_link_navigate(window, ct, cj)
}
