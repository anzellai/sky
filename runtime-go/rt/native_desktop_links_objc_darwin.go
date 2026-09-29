//go:build cgo && darwin

package rt

// The Cocoa half of universal links in the macOS desktop app
// (native_desktop_links_darwin.go holds the Go half; native_links.go the
// routing rule). The window comes from the webview library, which installs
// its own NSApplication delegate (`WebviewAppDelegate`) while it launches the
// app. A link reaches an app through that delegate:
//
//   - `application:continueUserActivity:restorationHandler:` with an
//     NSUserActivityTypeBrowsingWeb activity: a universal link (the
//     associated-domains entitlement, verified by Apple against the site's
//     apple-app-site-association file);
//   - the GetURL Apple event: a URL sent to the app (`open -a App url`, a
//     link another app hands over). AppKit turns it into
//     `application:openURLs:` only for a scheme in CFBundleURLTypes.
//
// The library's delegate implements neither method, so every link used to be
// dropped and the app opened on its first page. sky_links_install adds the
// methods to the delegate's class and takes the GetURL event when the app is
// about to finish launching (the delegate exists by then, and AppKit has not
// yet delivered the launch's own URLs), and every link goes to Go
// (skyDesktopLinkIn), which decides whether the host is declared and where
// the link opens.
//
// The definitions live here, apart from the //export file: cgo allows only
// declarations in the preamble of a file that exports a Go function.

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework AppKit -framework WebKit
#include <stdlib.h>
#include <string.h>
#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>
#import <WebKit/WebKit.h>
#import <objc/runtime.h>

// Defined in Go (native_desktop_links_darwin.go).
extern void skyDesktopLinkIn(char *url);

static void sky_link_in(NSURL *u) {
	if (u == nil) return;
	NSString *s = u.absoluteString;
	if (s == nil) return;
	char *c = strdup(s.UTF8String);
	skyDesktopLinkIn(c);
	free(c);
}

// The GetURL Apple event (`open -a App <url>`, a link handed to the app).
// AppKit passes it to `application:openURLs:` only for a scheme the app
// declares in CFBundleURLTypes, and an app does not declare http(s) (that
// would make it a browser), so the shell takes the event itself.
@interface SkyLinkEvents : NSObject
@end

@implementation SkyLinkEvents
- (void)getURL:(NSAppleEventDescriptor *)event withReply:(NSAppleEventDescriptor *)reply {
	NSString *s = [[event paramDescriptorForKeyword:'----'] stringValue];
	if (s != nil) sky_link_in([NSURL URLWithString:s]);
}
@end

static SkyLinkEvents *sky_link_events;

static void sky_links_patch_delegate(void) {
	if (sky_link_events == nil) {
		// Installed here, as the app finishes launching: AppKit installs its
		// own handlers before, and a handler installed earlier is replaced.
		sky_link_events = [[SkyLinkEvents alloc] init];
		[[NSAppleEventManager sharedAppleEventManager]
		    setEventHandler:sky_link_events
		        andSelector:@selector(getURL:withReply:)
		      forEventClass:'GURL'
		         andEventID:'GURL'];
	}
	id d = [NSApp delegate];
	if (d == nil) return;
	Class cls = object_getClass(d);
	BOOL added = NO;
	// A URL AppKit hands the delegate (a scheme a CFBundleURLTypes fragment
	// declares).
	added |= class_addMethod(cls, @selector(application:openURLs:),
		imp_implementationWithBlock(^(id self, NSApplication *app, NSArray<NSURL *> *urls) {
			for (NSURL *u in urls) sky_link_in(u);
		}), "v@:@@");
	// A universal link (Handoff from Safari, a link the system routes to the
	// app for its associated domain).
	NSString *willTypes = [NSString stringWithFormat:@"%s@:@@", @encode(BOOL)];
	added |= class_addMethod(cls, @selector(application:willContinueUserActivityWithType:),
		imp_implementationWithBlock(^BOOL(id self, NSApplication *app, NSString *type) {
			return [type isEqualToString:NSUserActivityTypeBrowsingWeb];
		}), willTypes.UTF8String);
	NSString *contTypes = [NSString stringWithFormat:@"%s@:@@@?", @encode(BOOL)];
	added |= class_addMethod(cls, @selector(application:continueUserActivity:restorationHandler:),
		imp_implementationWithBlock(^BOOL(id self, NSApplication *app, NSUserActivity *act,
		                                  void (^restore)(NSArray *)) {
			if (![act.activityType isEqualToString:NSUserActivityTypeBrowsingWeb]
			    || act.webpageURL == nil) {
				return NO;
			}
			sky_link_in(act.webpageURL);
			return YES;
		}), contTypes.UTF8String);
	if (added) {
		// NSApplication reads what its delegate implements when the delegate
		// is set: set it again so the new methods are seen.
		[NSApp setDelegate:nil];
		[NSApp setDelegate:d];
	}
}

// Called once, before the webview library creates the window and launches
// the app.
void sky_links_install(void) {
	static BOOL installed = NO;
	if (installed) return;
	installed = YES;
	[NSApplication sharedApplication];
	if ([NSApp delegate] != nil) {
		sky_links_patch_delegate();
		return;
	}
	[[NSNotificationCenter defaultCenter]
	    addObserverForName:NSApplicationWillFinishLaunchingNotification
	                object:nil
	                 queue:nil
	            usingBlock:^(NSNotification *n) { sky_links_patch_delegate(); }];
}

// The app's `applinks:` hosts, one per line (malloc'd), from the `SkyLinkHosts`
// array the release build writes into Info.plist. NULL for a bare binary or an
// app that declares none.
char *sky_links_hosts(void) {
	@autoreleasepool {
		id v = [[NSBundle mainBundle] objectForInfoDictionaryKey:@"SkyLinkHosts"];
		if (![v isKindOfClass:[NSArray class]]) return NULL;
		NSMutableArray *out = [NSMutableArray array];
		for (id h in (NSArray *)v) {
			if ([h isKindOfClass:[NSString class]]) [out addObject:h];
		}
		if (out.count == 0) return NULL;
		return strdup([out componentsJoinedByString:@"\n"].UTF8String);
	}
}

// Open `target` in the window's web view. When the page shows the app (same
// scheme, host and port) and is not loading, run `inPlace` (the client
// router's pushState + popstate), so the app keeps its state; otherwise load
// the address.
void sky_link_navigate(void *window, const char *target, const char *inPlace) {
	@autoreleasepool {
		NSWindow *win = (__bridge NSWindow *)window;
		if (win == nil) return;
		id view = [win contentView];
		if (![view isKindOfClass:[WKWebView class]]) return;
		WKWebView *web = (WKWebView *)view;
		NSURL *t = [NSURL URLWithString:[NSString stringWithUTF8String:target]];
		if (t == nil) return;
		NSURL *cur = web.URL;
		BOOL same = cur != nil && !web.isLoading && cur.scheme != nil && t.scheme != nil
			&& [cur.scheme caseInsensitiveCompare:t.scheme] == NSOrderedSame
			&& cur.host != nil && t.host != nil
			&& [cur.host caseInsensitiveCompare:t.host] == NSOrderedSame
			&& ((cur.port == nil && t.port == nil) || [cur.port isEqual:t.port]);
		if (same && inPlace != NULL && inPlace[0] != 0) {
			[web evaluateJavaScript:[NSString stringWithUTF8String:inPlace] completionHandler:nil];
		} else {
			[web loadRequest:[NSURLRequest requestWithURL:t]];
		}
	}
}
*/
import "C"
