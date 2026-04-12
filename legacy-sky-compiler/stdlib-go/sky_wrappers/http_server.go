package sky_wrappers

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Sky_http_server_Listen starts an HTTP server with Sky route handlers.
func Sky_http_server_Listen(portNum any, routes any) any {
	return func() any {
		mux := skyBuildMux(Sky_AsList(routes), "")
		addr := fmt.Sprintf(":%d", Sky_AsInt(portNum))
		fmt.Fprintf(os.Stderr, "Sky server listening on %s\n", addr)
		err := http.ListenAndServe(addr, mux)
		if err != nil {
			return SkyErr(err.Error())
		}
		return SkyOk(struct{}{})
	}
}

// Sky_http_server_ListenTLS starts an HTTPS server.
func Sky_http_server_ListenTLS(portNum, certFile, keyFile, routes any) any {
	return func() any {
		mux := skyBuildMux(Sky_AsList(routes), "")
		addr := fmt.Sprintf(":%d", Sky_AsInt(portNum))
		fmt.Fprintf(os.Stderr, "Sky server (TLS) listening on %s\n", addr)
		err := http.ListenAndServeTLS(addr, Sky_AsString(certFile), Sky_AsString(keyFile), mux)
		if err != nil {
			return SkyErr(err.Error())
		}
		return SkyOk(struct{}{})
	}
}

func skyBuildMux(routes []any, prefix string) *http.ServeMux {
	mux := http.NewServeMux()

	for _, r := range routes {
		rm := Sky_AsMap(r)
		if rm == nil {
			continue
		}
		skyName, _ := rm["SkyName"].(string)

		switch skyName {
		case "RouteEntry":
			method := Sky_AsString(rm["V0"])
			pattern := prefix + Sky_AsString(rm["V1"])
			handler := rm["V2"]

			muxPattern := pattern
			if method != "*" {
				muxPattern = method + " " + pattern
			}
			mux.HandleFunc(muxPattern, skyMakeHandler(handler))

		case "RouteGroup":
			groupPrefix := prefix + Sky_AsString(rm["V0"])
			groupRoutes := Sky_AsList(rm["V1"])
			subMux := skyBuildMux(groupRoutes, groupPrefix)
			mux.Handle(groupPrefix+"/", subMux)

		case "RouteStatic":
			urlPrefix := prefix + Sky_AsString(rm["V0"])
			dirPath := Sky_AsString(rm["V1"])
			fs := http.FileServer(http.Dir(dirPath))
			mux.Handle(urlPrefix+"/", http.StripPrefix(urlPrefix, fs))
		}
	}

	return mux
}

func skyMakeHandler(handler any) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		defer func() {
			if rec := recover(); rec != nil {
				http.Error(w, fmt.Sprintf("Internal Server Error: %v", rec), 500)
				fmt.Fprintf(os.Stderr, "%s %s 500 %s (panic: %v)\n", r.Method, r.URL.Path, time.Since(start), rec)
			}
		}()

		skyReq := skyBuildRequest(r)

		// Call Sky handler: handler(request) -> Task String Response
		var taskResult any
		if fn, ok := handler.(func(any) any); ok {
			taskResult = fn(skyReq)
		} else if fn2, ok := handler.(func(any, any) any); ok {
			taskResult = fn2(nil, skyReq)
		} else {
			http.Error(w, "Invalid handler", 500)
			return
		}

		// Execute the Task thunk
		var skyResp any
		if thunk, ok := taskResult.(func() any); ok {
			skyResp = thunk()
		} else if result, ok := taskResult.(SkyResult); ok {
			skyResp = result
		} else {
			skyResp = SkyOk(taskResult)
		}

		result, ok := skyResp.(SkyResult)
		if !ok {
			// Not a SkyResult — treat as direct response value
			skyWriteResponse(w, Sky_AsMap(skyResp))
			fmt.Fprintf(os.Stderr, "%s %s 200 %s\n", r.Method, r.URL.Path, time.Since(start))
			return
		}

		if result.Tag == 1 {
			http.Error(w, Sky_AsString(result.ErrValue), 500)
			fmt.Fprintf(os.Stderr, "%s %s 500 %s\n", r.Method, r.URL.Path, time.Since(start))
			return
		}

		resp := Sky_AsMap(result.OkValue)
		if resp == nil {
			http.Error(w, "Internal Server Error", 500)
			return
		}

		skyWriteResponse(w, resp)
		status := Sky_AsInt(resp["status"])
		if status == 0 {
			status = 200
		}
		fmt.Fprintf(os.Stderr, "%s %s %d %s\n", r.Method, r.URL.Path, status, time.Since(start))
	}
}

func skyBuildRequest(r *http.Request) map[string]any {
	_ = r.ParseForm()
	body, _ := io.ReadAll(r.Body)

	headers := make([]any, 0)
	for k, vs := range r.Header {
		for _, v := range vs {
			headers = append(headers, SkyTuple2{k, v})
		}
	}

	cookies := make([]any, 0)
	for _, c := range r.Cookies() {
		cookies = append(cookies, SkyTuple2{c.Name, c.Value})
	}

	query := make([]any, 0)
	for k, vs := range r.URL.Query() {
		for _, v := range vs {
			query = append(query, SkyTuple2{k, v})
		}
	}

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

func skyWriteResponse(w http.ResponseWriter, resp map[string]any) {
	if resp == nil {
		w.WriteHeader(200)
		return
	}

	// Set cookies
	if cookies, ok := resp["cookies"].([]any); ok {
		for _, c := range cookies {
			cm := Sky_AsMap(c)
			if cm == nil {
				continue
			}
			http.SetCookie(w, &http.Cookie{
				Name:     Sky_AsString(cm["name"]),
				Value:    Sky_AsString(cm["value"]),
				Path:     Sky_AsString(cm["path"]),
				MaxAge:   Sky_AsInt(cm["maxAge"]),
				HttpOnly: Sky_AsBool(cm["httpOnly"]),
				Secure:   Sky_AsBool(cm["secure"]),
				SameSite: skyParseSameSite(Sky_AsString(cm["sameSite"])),
			})
		}
	}

	// Set headers
	if headers, ok := resp["headers"].([]any); ok {
		for _, h := range headers {
			if t, ok := h.(Tuple2); ok {
				w.Header().Set(Sky_AsString(t.V0), Sky_AsString(t.V1))
			}
		}
	}

	status := Sky_AsInt(resp["status"])
	if status == 0 {
		status = 200
	}
	w.WriteHeader(status)
	fmt.Fprint(w, Sky_AsString(resp["body"]))
}

func skyParseSameSite(s string) http.SameSite {
	switch strings.ToLower(s) {
	case "strict":
		return http.SameSiteStrictMode
	case "none":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
}
