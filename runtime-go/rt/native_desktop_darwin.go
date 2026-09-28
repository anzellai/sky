//go:build cgo && darwin

package rt

// The macOS desktop shell's side of the native-shell protocol
// (native_shell.go): the Keychain as the secure store and LocalAuthentication
// (Touch ID) as the biometric prompt. Compiled with the Sky.Webview window
// (webview.go, the same `cgo && darwin` constraint); webviewURLRun binds it for
// the Sky.Spa client it hosts.

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework Security -framework LocalAuthentication -framework CoreFoundation
#include <stdlib.h>
#include <string.h>
#import <Foundation/Foundation.h>
#import <Security/Security.h>
#import <LocalAuthentication/LocalAuthentication.h>

static NSMutableDictionary *sky_kc_query(const char *service, const char *account) {
	NSMutableDictionary *q = [NSMutableDictionary dictionary];
	q[(__bridge id)kSecClass] = (__bridge id)kSecClassGenericPassword;
	q[(__bridge id)kSecAttrService] = [NSString stringWithUTF8String:service];
	q[(__bridge id)kSecAttrAccount] = [NSString stringWithUTF8String:account];
	return q;
}

static int sky_kc_set(const char *service, const char *account, const void *data, int len) {
	@autoreleasepool {
		NSMutableDictionary *q = sky_kc_query(service, account);
		SecItemDelete((__bridge CFDictionaryRef)q);
		q[(__bridge id)kSecValueData] = [NSData dataWithBytes:data length:(NSUInteger)len];
		q[(__bridge id)kSecAttrAccessible] = (__bridge id)kSecAttrAccessibleWhenUnlockedThisDeviceOnly;
		return (int)SecItemAdd((__bridge CFDictionaryRef)q, NULL);
	}
}

// Returns the status; on success *out is a malloc'd copy the caller frees.
static int sky_kc_get(const char *service, const char *account, void **out, int *outlen) {
	@autoreleasepool {
		NSMutableDictionary *q = sky_kc_query(service, account);
		q[(__bridge id)kSecReturnData] = @YES;
		q[(__bridge id)kSecMatchLimit] = (__bridge id)kSecMatchLimitOne;
		CFTypeRef res = NULL;
		OSStatus st = SecItemCopyMatching((__bridge CFDictionaryRef)q, &res);
		if (st != errSecSuccess) return (int)st;
		NSData *d = (__bridge_transfer NSData *)res;
		*outlen = (int)d.length;
		*out = malloc(d.length > 0 ? d.length : 1);
		memcpy(*out, d.bytes, d.length);
		return 0;
	}
}

static int sky_kc_remove(const char *service, const char *account) {
	@autoreleasepool {
		OSStatus st = SecItemDelete((__bridge CFDictionaryRef)sky_kc_query(service, account));
		return st == errSecItemNotFound ? 0 : (int)st;
	}
}

// The main bundle's identifier, or NULL for a bare binary (malloc'd).
static char *sky_bundle_id(void) {
	@autoreleasepool {
		NSString *s = [[NSBundle mainBundle] bundleIdentifier];
		return s ? strdup(s.UTF8String) : NULL;
	}
}

// 0 authenticated, 1 failed, 2 cancelled, 3 unavailable. *msg is malloc'd.
static int sky_la_authenticate(const char *reason, char **msg) {
	@autoreleasepool {
		LAContext *ctx = [[LAContext alloc] init];
		NSError *err = nil;
		if (![ctx canEvaluatePolicy:LAPolicyDeviceOwnerAuthenticationWithBiometrics error:&err]) {
			*msg = strdup(err ? err.localizedDescription.UTF8String : "no biometrics on this Mac");
			return 3;
		}
		__block int rc = 1;
		__block char *m = NULL;
		dispatch_semaphore_t sem = dispatch_semaphore_create(0);
		[ctx evaluatePolicy:LAPolicyDeviceOwnerAuthenticationWithBiometrics
		    localizedReason:[NSString stringWithUTF8String:reason]
		              reply:^(BOOL ok, NSError *e) {
			if (ok) {
				rc = 0;
			} else {
				switch (e.code) {
				case LAErrorUserCancel:
				case LAErrorSystemCancel:
				case LAErrorAppCancel:
				case LAErrorUserFallback:
					rc = 2;
					break;
				case LAErrorAuthenticationFailed:
					rc = 1;
					break;
				default:
					rc = 3;
				}
				m = strdup(e.localizedDescription.UTF8String ? e.localizedDescription.UTF8String : "");
			}
			dispatch_semaphore_signal(sem);
		}];
		dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);
		*msg = m;
		return rc;
	}
}
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"
)

// errSecItemNotFound / errSecInteractionNotAllowed from Security.framework.
const (
	secItemNotFound        = -25300
	secInteractionNotAllow = -25308
)

// keychainStore is the macOS Keychain as a nativeSecureStore. Items are
// generic passwords under a service named for the app: the bundle identifier
// of the packaged `.app`, else the executable's name.
type keychainStore struct{ service string }

func newKeychainStore() keychainStore {
	if id := C.sky_bundle_id(); id != nil {
		defer C.free(unsafe.Pointer(id))
		return keychainStore{service: C.GoString(id)}
	}
	exe, _ := os.Executable()
	return keychainStore{service: "sky-native." + filepath.Base(exe)}
}

func keychainErr(status C.int) error {
	switch int(status) {
	case secInteractionNotAllow:
		return errors.New("unavailable: the keychain is locked")
	default:
		return fmt.Errorf("keychain status %d", int(status))
	}
}

func (k keychainStore) Set(key, value string) error {
	cs, ca := C.CString(k.service), C.CString(key)
	defer C.free(unsafe.Pointer(cs))
	defer C.free(unsafe.Pointer(ca))
	b := []byte(value)
	var p unsafe.Pointer
	if len(b) > 0 {
		p = C.CBytes(b)
		defer C.free(p)
	}
	if st := C.sky_kc_set(cs, ca, p, C.int(len(b))); st != 0 {
		return keychainErr(st)
	}
	return nil
}

func (k keychainStore) Get(key string) (string, bool, error) {
	cs, ca := C.CString(k.service), C.CString(key)
	defer C.free(unsafe.Pointer(cs))
	defer C.free(unsafe.Pointer(ca))
	var out unsafe.Pointer
	var n C.int
	st := C.sky_kc_get(cs, ca, &out, &n)
	if int(st) == secItemNotFound {
		return "", false, nil
	}
	if st != 0 {
		return "", false, keychainErr(st)
	}
	defer C.free(out)
	return string(C.GoBytes(out, n)), true, nil
}

func (k keychainStore) Remove(key string) error {
	cs, ca := C.CString(k.service), C.CString(key)
	defer C.free(unsafe.Pointer(cs))
	defer C.free(unsafe.Pointer(ca))
	if st := C.sky_kc_remove(cs, ca); st != 0 {
		return keychainErr(st)
	}
	return nil
}

// touchIDPrompt is LocalAuthentication as a nativeBiometric.
func touchIDPrompt(reason string) error {
	cr := C.CString(reason)
	defer C.free(unsafe.Pointer(cr))
	var msg *C.char
	rc := C.sky_la_authenticate(cr, &msg)
	text := ""
	if msg != nil {
		text = C.GoString(msg)
		C.free(unsafe.Pointer(msg))
	}
	switch rc {
	case 0:
		return nil
	case 1:
		return errors.New("failed: " + text)
	case 2:
		return errors.New("cancelled: " + text)
	default:
		return errors.New("unavailable: " + text)
	}
}

// desktopNativeInitJS installs window.__skyNative(op, payload) → Promise in
// every page the desktop window loads. The Go side runs the op OFF the main
// thread (a Keychain read or a Touch ID prompt must not freeze the window)
// and settles the Promise through window.__skyNativeCb.
const desktopNativeInitJS = `(function(){
  if (window.__skyNative) return;
  var seq = 0;
  window.__skyNativeCb = window.__skyNativeCb || {};
  window.__skyNative = function(op, payload) {
    return new Promise(function(resolve, reject) {
      var id = 'n' + (++seq);
      window.__skyNativeCb[id] = function(ok, data) {
        delete window.__skyNativeCb[id];
        if (ok) { resolve(data); } else { reject(data); }
      };
      window.__skyNativeStart(op, payload, id);
    });
  };
})();`
