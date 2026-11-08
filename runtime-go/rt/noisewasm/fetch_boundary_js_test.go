//go:build js && wasm

package noisewasm

import (
	"reflect"
	"syscall/js"
	"testing"

	"sky-app/rt"
)

// This is a real JavaScript fetch function, not a Go js.Func that panics and
// not a rejected-Promise substitute for a synchronous exception. No network is
// used. The existing native Noise/Wasm gate builds this package against every
// actual production runtime file and runs it with the pinned Go Node loader.
const fetchBoundaryFixtureJS = `
return (() => {
  const originalFetch = globalThis.fetch;
  const originalSetTimeout = globalThis.setTimeout;
  const originalClearTimeout = globalThis.clearTimeout;
  let callbacks = [], active = new Set(), next = 0x40000000;
  let fetchCalls = 0, armed = 0, cleared = 0;
  let messageGets = 0, toStrings = 0;
  const wrap = promise => ({
    then(callback) {
      callbacks.push(callback);
      const result = promise.then(callback);
      return { catch(rejected) {
        callbacks.push(rejected);
        return result.catch(rejected);
      }};
    }
  });
  return {
    install(mode) {
      globalThis.setTimeout = function(callback, delay, ...args) {
        if (delay === 30000) {
          const id = ++next;
          active.add(id); callbacks.push(callback); armed++;
          return id;
        }
        return originalSetTimeout(callback, delay, ...args);
      };
      globalThis.clearTimeout = function(id) {
        if (active.delete(id)) { cleared++; return; }
        return originalClearTimeout(id);
      };
      globalThis.fetch = function(url, options) {
        fetchCalls++;
        if (url !== "/__sky_public_fetch_boundary__" ||
            options.method !== "POST" || options.body !== "public body") {
          throw new Error("public fixture request contract");
        }
        if (mode.startsWith("rejection-")) {
          let value;
          switch (mode) {
            case "rejection-error": value = new TypeError("public error rejection"); break;
            case "rejection-string": value = "public string rejection"; break;
            case "rejection-number": value = 42; break;
            case "rejection-boolean": value = true; break;
            case "rejection-message-getter":
              value = Object.defineProperty({}, "message", {get() {
                messageGets++; throw new TypeError("public message getter");
              }}); break;
            case "rejection-to-string":
              value = {toString() {
                toStrings++; throw new TypeError("public toString failure");
              }}; break;
            case "rejection-no-to-string": value = Object.create(null); break;
            case "rejection-noncallable-to-string": value = {toString: 42}; break;
            default: throw new Error("public rejection fixture mode");
          }
          return wrap(Promise.reject(value));
        }
        if (mode === "synchronous-throw") {
          throw new TypeError("public synchronous fetch failure");
        }
        if (mode === "promise-reject") {
          return wrap(Promise.reject(new TypeError("public promise rejection")));
        }
        if (mode === "malformed-promise") return 42;
        if (mode !== "success") throw new Error("public fixture mode");
        return wrap(Promise.resolve({
          status: 200,
          headers: {
            get() { return null; },
            forEach(callback) {
              callbacks.push(callback);
              callback("text/plain", "content-type");
            }
          },
          text() { return wrap(Promise.resolve("public response")); }
        }));
      };
    },
    snapshot() {
      return {fetchCalls, armed, cleared, active: active.size,
              callbacks: callbacks.length, messageGets, toStrings};
    },
    releasedCallbacks() {
      const originalError = console.error;
      let released = 0;
      console.error = function(...args) {
        if (args.length === 1 && args[0] === "call to released function") {
          released++; return;
        }
        return originalError.apply(console, args);
      };
      try { for (const callback of callbacks) callback(); }
      finally { console.error = originalError; }
      return released;
    },
    restore() {
      globalThis.fetch = originalFetch;
      globalThis.setTimeout = originalSetTimeout;
      globalThis.clearTimeout = originalClearTimeout;
    }
  };
})();
`

func TestWasmFetchBoundary(t *testing.T) {
	// Run normal controls first: a synchronous-throw RED must not hide a bad
	// fixture or substitute a Promise rejection for the exception under test.
	for _, mode := range []string{"success", "promise-reject", "synchronous-throw"} {
		t.Run(mode, func(t *testing.T) {
			fixture := js.Global().Get("Function").New(fetchBoundaryFixtureJS).Invoke()
			fixture.Call("install", mode)
			t.Cleanup(func() { fixture.Call("restore") })

			// This non-RPC POST is not retryable. It reaches the real fetchOnce
			// exactly once through the same typed Task boundary codegen emits.
			result := rt.Task_run(rt.TaskCoerceT[rt.SkyADT, rt.HttpResponse](
				rt.Http_post("/__sky_public_fetch_boundary__", "public body")))
			if mode == "success" {
				if result.Tag != 0 || result.OkValue.Status != 200 || result.OkValue.Body != "public response" {
					t.Fatal("successful JavaScript fetch did not return its actual response")
				}
			} else {
				if result.Tag != 1 {
					t.Fatal("failed JavaScript fetch did not return Task Err")
				}
				kind, ok := rt.AdtField(result.ErrValue, 0).(int)
				if !ok || kind != 1 { // Sky.Core.Error.Network
					t.Fatal("failed JavaScript fetch did not return classified Network")
				}
			}

			state := fixture.Call("snapshot")
			if state.Get("fetchCalls").Int() != 1 || state.Get("armed").Int() != 1 ||
				state.Get("cleared").Int() != 1 || state.Get("active").Int() != 0 {
				t.Fatal("fetch did not settle once and clear its actual timeout")
			}
			wantCallbacks := map[string]int{"success": 6, "promise-reject": 3, "synchronous-throw": 1}[mode]
			if state.Get("callbacks").Int() != wantCallbacks {
				t.Fatal("fixture did not observe the expected actual callback publications")
			}
			// Invoking a released js.Func logs this fixed Go runtime sentinel.
			// This observes actual releases, not a production counter or test hook.
			if fixture.Call("releasedCallbacks").Int() != wantCallbacks {
				t.Fatal("fetch retained a published Go callback after settling")
			}
		})
	}
}

// A non-Promise result is a host programming contract violation, not a fetch
// exception. It must stay loud while the actual timeout/callback is retired.
func TestWasmFetchProgrammingPanicStaysLoud(t *testing.T) {
	fixture := js.Global().Get("Function").New(fetchBoundaryFixtureJS).Invoke()
	fixture.Call("install", "malformed-promise")
	t.Cleanup(func() { fixture.Call("restore") })
	var caught any
	func() {
		defer func() { caught = recover() }()
		_ = rt.Task_run(rt.TaskCoerceT[rt.SkyADT, rt.HttpResponse](
			rt.Http_post("/__sky_public_fetch_boundary__", "public body")))
	}()
	if _, programming := caught.(*js.ValueError); !programming {
		t.Fatalf("invalid host Promise must retain its Go programming panic, got %T", caught)
	}
	state := fixture.Call("snapshot")
	if state.Get("fetchCalls").Int() != 1 || state.Get("armed").Int() != 1 ||
		state.Get("cleared").Int() != 1 || state.Get("active").Int() != 0 ||
		state.Get("callbacks").Int() != 1 || fixture.Call("releasedCallbacks").Int() != 1 {
		t.Fatal("programming-panic unwind retained the actual timeout/callback")
	}
}

// The host gate also runs each of these in a separate Node process: a fatal
// callback panic in one reason must not hide the remaining rejection shapes.
func TestWasmFetchRejectionFormatting(t *testing.T) {
	cases := []struct {
		name    string
		message string
		gets    int
		strings int
	}{
		{"rejection-error", "public error rejection", 0, 0},
		{"rejection-string", "public string rejection", 0, 0},
		{"rejection-number", "42", 0, 0},
		{"rejection-boolean", "true", 0, 0},
		{"rejection-message-getter", "request failed", 1, 0},
		{"rejection-to-string", "request failed", 0, 1},
		{"rejection-no-to-string", "request failed", 0, 0},
		{"rejection-noncallable-to-string", "request failed", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := js.Global().Get("Function").New(fetchBoundaryFixtureJS).Invoke()
			fixture.Call("install", tc.name)
			t.Cleanup(func() { fixture.Call("restore") })
			result := rt.Task_run(rt.TaskCoerceT[rt.SkyADT, rt.HttpResponse](
				rt.Http_post("/__sky_public_fetch_boundary__", "public body")))
			if result.Tag != 1 || rt.AdtField(result.ErrValue, 0) != 1 {
				t.Fatal("valid JavaScript rejection did not return classified Network Task Err")
			}
			info := reflect.ValueOf(rt.AdtField(result.ErrValue, 1))
			if info.Kind() != reflect.Struct {
				t.Fatal("Network error did not retain its structured Error info")
			}
			message := info.FieldByName("Message")
			if !message.IsValid() || message.Kind() != reflect.String || message.String() != "http.post: "+tc.message {
				t.Fatal("Network error did not preserve a safe rejection description")
			}
			state := fixture.Call("snapshot")
			if state.Get("fetchCalls").Int() != 1 || state.Get("armed").Int() != 1 ||
				state.Get("cleared").Int() != 1 || state.Get("active").Int() != 0 ||
				state.Get("callbacks").Int() != 3 || fixture.Call("releasedCallbacks").Int() != 3 {
				t.Fatal("rejection formatting retained an actual timer/callback")
			}
			if state.Get("messageGets").Int() != tc.gets || state.Get("toStrings").Int() != tc.strings {
				t.Fatal("rejection formatting repeated a foreign getter or method")
			}
		})
	}
}
