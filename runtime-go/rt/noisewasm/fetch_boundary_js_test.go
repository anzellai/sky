//go:build js && wasm

package noisewasm

import (
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
        if (mode === "synchronous-throw") {
          throw new TypeError("public synchronous fetch failure");
        }
        if (mode === "promise-reject") {
          return wrap(Promise.reject(new TypeError("public promise rejection")));
        }
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
              callbacks: callbacks.length};
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
