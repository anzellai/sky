//go:build cgo && darwin && skytest_links

package rt

// A test-only entry point for universal links in the macOS desktop app,
// compiled only with `-tags skytest_links` (the native_shell_flow macOS test
// builds the shell with GOFLAGS=-tags=skytest_links). A universal link needs
// the associated-domains entitlement, which an ad hoc signature cannot carry,
// and Apple's check of the site's apple-app-site-association file, so a test
// cannot make macOS deliver one. This hook delivers the same thing macOS
// does: an NSUserActivityTypeBrowsingWeb activity with the link as its
// webpageURL, sent to the app delegate's
// `application:continueUserActivity:restorationHandler:`. Each URL in
// SKY_TEST_LINK_ACTIVITIES (comma-separated) is sent in turn, the first when
// the window has started and each next one 8 seconds later.

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework AppKit
#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>
#import <objc/message.h>
#include <stdlib.h>

static void sky_test_post_activity(const char *url, double delay) {
	NSString *s = [NSString stringWithUTF8String:url];
	dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(delay * NSEC_PER_SEC)),
	               dispatch_get_main_queue(), ^{
		NSUserActivity *act =
		    [[NSUserActivity alloc] initWithActivityType:NSUserActivityTypeBrowsingWeb];
		act.webpageURL = [NSURL URLWithString:s];
		id d = [NSApp delegate];
		SEL sel = @selector(application:continueUserActivity:restorationHandler:);
		if (![d respondsToSelector:sel]) {
			NSLog(@"sky test: the app delegate does not take a user activity");
			return;
		}
		((BOOL (*)(id, SEL, id, id, id))objc_msgSend)(d, sel, NSApp, act, ^(NSArray *r) {});
	});
}
*/
import "C"

import (
	"os"
	"strings"
	"unsafe"
)

func init() {
	desktopLinkTestHook = func() {
		spec := strings.TrimSpace(os.Getenv("SKY_TEST_LINK_ACTIVITIES"))
		if spec == "" {
			return
		}
		for i, u := range strings.Split(spec, ",") {
			cu := C.CString(strings.TrimSpace(u))
			C.sky_test_post_activity(cu, C.double(8*i))
			C.free(unsafe.Pointer(cu))
		}
	}
}
