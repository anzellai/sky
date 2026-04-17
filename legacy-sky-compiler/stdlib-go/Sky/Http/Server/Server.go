package sky_sky_http_server

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Sky types
type SkyResult struct {
	Tag      int
	SkyName  string
	OkValue  any
	ErrValue any
}
type SkyTuple2 struct{ V0, V1 any }

func skyOk(v any) SkyResult  { return SkyResult{Tag: 0, SkyName: "Ok", OkValue: v} }
func skyErr(v any) SkyResult { return SkyResult{Tag: 1, SkyName: "Err", ErrValue: v} }

// Listen starts an HTTP server. Returns a Task (thunk).
func Listen(port any, routes any) any {
	return func() any {
		mux := buildMux(asList(routes), nil, "")
		addr := fmt.Sprintf(":%d", asInt(port))
		fmt.Fprintf(os.Stderr, "Sky server listening on %s\n", addr)
		err := http.ListenAndServe(addr, mux)
		if err != nil {
			return skyErr(err.Error())
		}
		return skyOk(struct{}{})
	}
}

// ListenTLS starts an HTTPS server.
func ListenTLS(port, certFile, keyFile, routes any) any {
	return func() any {
		mux := buildMux(asList(routes), nil, "")
		addr := fmt.Sprintf(":%d", asInt(port))
		fmt.Fprintf(os.Stderr, "Sky server (TLS) listening on %s\n", addr)
		err := http.ListenAndServeTLS(addr, asString(certFile), asString(keyFile), mux)
		if err != nil {
			return skyErr(err.Error())
		}
		return skyOk(struct{}{})
	}
}

// ══════════════════════════════════════════════════════
// ROUTER
// ══════════════════════════════════════════════════════

type route struct {
	method  string
	pattern string
	handler func(http.ResponseWriter, *http.Request)
}

type middleware func(http.Handler) http.Handler

func buildMux(routes []any, mws []func(any) any, prefix string) http.Handler {
	mux := http.NewServeMux()
	var allRoutes []route
	var skyMws []func(any) any
	copy(skyMws, mws)

	for _, r := range routes {
		rm := asMap(r)
		if rm == nil {
			continue
		}
		skyName := asString(rm["SkyName"])

		switch skyName {
		case "RouteEntry":
			method := asString(rm["V0"])
			pattern := prefix + asString(rm["V1"])
			handler := rm["V2"]
			allRoutes = append(allRoutes, route{method, pattern, makeSkyHandler(handler, skyMws)})

		case "RouteGroup":
			groupPrefix := prefix + asString(rm["V0"])
			groupRoutes := asList(rm["V1"])
			// Recursively build routes with prefix
			subHandler := buildMux(groupRoutes, skyMws, groupPrefix)
			mux.Handle(groupPrefix+"/", http.StripPrefix("", subHandler))
			continue

		case "RouteMiddleware":
			mw := rm["V1"]
			if mw != nil {
				skyMws = append(skyMws, mw.(func(any) any))
			}
			continue

		case "RouteStatic":
			urlPrefix := prefix + asString(rm["V0"])
			dirPath := asString(rm["V1"])
			fs := http.FileServer(http.Dir(dirPath))
			mux.Handle(urlPrefix+"/", http.StripPrefix(urlPrefix, fs))
			continue
		}
	}

	// Register all routes
	for _, r := range allRoutes {
		r := r // capture
		pattern := r.pattern
		if r.method != "*" {
			pattern = r.method + " " + r.pattern
		}
		mux.HandleFunc(pattern, r.handler)
	}

	return mux
}

// ══════════════════════════════════════════════════════
// SKY HANDLER ADAPTER
// ══════════════════════════════════════════════════════

func makeSkyHandler(handler any, middlewares []func(any) any) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Build Sky Request record
		skyReq := buildSkyRequest(r)

		// Apply middlewares (wrap the handler)
		currentHandler := handler
		for i := len(middlewares) - 1; i >= 0; i-- {
			mw := middlewares[i]
			if fn, ok := mw.(func(any) any); ok {
				currentHandler = fn(currentHandler)
			}
		}

		// Call the Sky handler: handler(request) -> Task String Response
		var skyResp any

		defer func() {
			if rec := recover(); rec != nil {
				http.Error(w, fmt.Sprintf("Internal Server Error: %v", rec), 500)
				logRequest(r, 500, start)
			}
		}()

		fn := currentHandler.(func(any) any)
		taskResult := fn(skyReq)

		// Execute the Task
		skyResp = runTask(taskResult)

		// Extract Response
		result := asSkyResult(skyResp)
		if result.Tag == 1 {
			// Task failed
			http.Error(w, asString(result.ErrValue), 500)
			logRequest(r, 500, start)
			return
		}

		resp := asMap(result.OkValue)
		if resp == nil {
			// Direct value (not wrapped in Result)
			resp = asMap(taskResult)
			if resp == nil {
				resp = asMap(skyResp)
			}
		}
		if resp == nil {
			http.Error(w, "Internal Server Error", 500)
			logRequest(r, 500, start)
			return
		}

		writeResponse(w, resp)
		logRequest(r, asInt(resp["status"]), start)
	}
}

func buildSkyRequest(r *http.Request) map[string]any {
	_ = r.ParseForm()
	body, _ := io.ReadAll(r.Body)

	// Parse headers
	headers := make([]any, 0)
	for k, vs := range r.Header {
		for _, v := range vs {
			headers = append(headers, SkyTuple2{V0: k, V1: v})
		}
	}

	// Parse cookies
	cookies := make([]any, 0)
	for _, c := range r.Cookies() {
		cookies = append(cookies, SkyTuple2{V0: c.Name, V1: c.Value})
	}

	// Parse query params
	query := make([]any, 0)
	for k, vs := range r.URL.Query() {
		for _, v := range vs {
			query = append(query, SkyTuple2{V0: k, V1: v})
		}
	}

	// Route params (from Go 1.22+ PathValue)
	params := make([]any, 0)

	protocol := "http"
	if r.TLS != nil {
		protocol = "https"
	}

	return map[string]any{
		"method":     r.Method,
		"path":       r.URL.Path,
		"body":       string(body),
		"headers":    headers,
		"params":     params,
		"query":      query,
		"cookies":    cookies,
		"remoteAddr": r.RemoteAddr,
		"host":       r.Host,
		"protocol":   protocol,
	}
}

func writeResponse(w http.ResponseWriter, resp map[string]any) {
	// Set cookies
	if cookies, ok := resp["cookies"].([]any); ok {
		for _, c := range cookies {
			cm := asMap(c)
			if cm == nil {
				continue
			}
			http.SetCookie(w, &http.Cookie{
				Name:     asString(cm["name"]),
				Value:    asString(cm["value"]),
				Path:     asString(cm["path"]),
				MaxAge:   asInt(cm["maxAge"]),
				HttpOnly: asBool(cm["httpOnly"]),
				Secure:   asBool(cm["secure"]),
				SameSite: parseSameSite(asString(cm["sameSite"])),
			})
		}
	}

	// Set headers
	if headers, ok := resp["headers"].([]any); ok {
		for _, h := range headers {
			if t, ok := h.(SkyTuple2); ok {
				w.Header().Set(asString(t.V0), asString(t.V1))
			}
		}
	}

	// Write status and body
	status := asInt(resp["status"])
	if status == 0 {
		status = 200
	}
	w.WriteHeader(status)
	fmt.Fprint(w, asString(resp["body"]))
}

func parseSameSite(s string) http.SameSite {
	switch strings.ToLower(s) {
	case "strict":
		return http.SameSiteStrictMode
	case "none":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
}

func logRequest(r *http.Request, status int, start time.Time) {
	duration := time.Since(start)
	fmt.Fprintf(os.Stderr, "%s %s %d %s\n", r.Method, r.URL.Path, status, duration)
}

// ══════════════════════════════════════════════════════
// TASK RUNTIME
// ══════════════════════════════════════════════════════

func runTask(task any) any {
	if t, ok := task.(func() any); ok {
		defer func() {
			if r := recover(); r != nil {
				// swallow
			}
		}()
		return t()
	}
	if r, ok := task.(SkyResult); ok {
		return r
	}
	return skyOk(task)
}

func asSkyResult(v any) SkyResult {
	if r, ok := v.(SkyResult); ok {
		return r
	}
	return SkyResult{}
}

// ══════════════════════════════════════════════════════
// TYPE HELPERS
// ══════════════════════════════════════════════════════

func asInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	default:
		return 0
	}
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func asBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func asList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}
