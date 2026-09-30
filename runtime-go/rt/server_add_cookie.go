//go:build !js

package rt

// Server_addCookie is `Sky.Http.Server.addCookie : Cookie -> Response ->
// Response`: attach a cookie built with `Server.cookie`. It is the typed form
// of the pre-built-Cookie shape that `withCookie` accepted while it was typed
// `any` (the CAST audit finding, v0.27.0); the attribute handling is the same
// code path.
func Server_addCookie(cookie any, resp any) any {
	return Server_withCookie(cookie, resp)
}
