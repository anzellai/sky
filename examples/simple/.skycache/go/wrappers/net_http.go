package sky_wrappers

import (
	"net/http"
	"io/fs"
	"io"
	"context"
	"time"
	"net/url"
	"bufio"
	"net"
	"mime/multipart"
	"crypto/tls"
	"log"
)

func Sky_net_http_AllowQuerySemicolons(arg0 any) http.Handler {
	_arg0 := arg0.(http.Handler)
	return http.AllowQuerySemicolons(_arg0)
}

func Sky_net_http_CanonicalHeaderKey(arg0 any) string {
	_arg0 := arg0.(string)
	return http.CanonicalHeaderKey(_arg0)
}

func Sky_net_http_DetectContentType(arg0 any) string {
	_arg0 := arg0.([]byte)
	return http.DetectContentType(_arg0)
}

func Sky_net_http_Error(arg0 any, arg1 any, arg2 any) any {
	_arg0 := arg0.(http.ResponseWriter)
	_arg1 := arg1.(string)
	_arg2 := arg2.(int)
	http.Error(_arg0, _arg1, _arg2)
	return struct{}{}
}

func Sky_net_http_FS(arg0 any) http.FileSystem {
	_arg0 := arg0.(fs.FS)
	return http.FS(_arg0)
}

func Sky_net_http_FileServer(arg0 any) http.Handler {
	_arg0 := arg0.(http.FileSystem)
	return http.FileServer(_arg0)
}

func Sky_net_http_FileServerFS(arg0 any) http.Handler {
	_arg0 := arg0.(fs.FS)
	return http.FileServerFS(_arg0)
}

func Sky_net_http_Get(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := http.Get(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_Handle(arg0 any, arg1 any) any {
	_arg0 := arg0.(string)
	_arg1 := arg1.(http.Handler)
	http.Handle(_arg0, _arg1)
	return struct{}{}
}

func Sky_net_http_HandleFunc(arg0 any, arg1 any) any {
	_arg0 := arg0.(string)
	_skyFn1 := arg1.(func(any) any)
	_arg1 := func(p0 http.ResponseWriter, p1 *http.Request) {
		_skyFn1(p0).(func(any) any)(p1)
	}
	http.HandleFunc(_arg0, _arg1)
	return struct{}{}
}

func Sky_net_http_Head(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := http.Head(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_ListenAndServe(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(http.Handler)
	err := http.ListenAndServe(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_ListenAndServeTLS(arg0 any, arg1 any, arg2 any, arg3 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	_arg2 := arg2.(string)
	_arg3 := arg3.(http.Handler)
	err := http.ListenAndServeTLS(_arg0, _arg1, _arg2, _arg3)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_MaxBytesHandler(arg0 any, arg1 any) http.Handler {
	_arg0 := arg0.(http.Handler)
	_arg1 := arg1.(int64)
	return http.MaxBytesHandler(_arg0, _arg1)
}

func Sky_net_http_MaxBytesReader(arg0 any, arg1 any, arg2 any) io.ReadCloser {
	_arg0 := arg0.(http.ResponseWriter)
	_arg1 := arg1.(io.ReadCloser)
	_arg2 := arg2.(int64)
	return http.MaxBytesReader(_arg0, _arg1, _arg2)
}

func Sky_net_http_NewCrossOriginProtection() *http.CrossOriginProtection {
	return http.NewCrossOriginProtection()
}

func Sky_net_http_NewFileTransport(arg0 any) http.RoundTripper {
	_arg0 := arg0.(http.FileSystem)
	return http.NewFileTransport(_arg0)
}

func Sky_net_http_NewFileTransportFS(arg0 any) http.RoundTripper {
	_arg0 := arg0.(fs.FS)
	return http.NewFileTransportFS(_arg0)
}

func Sky_net_http_NewRequest(arg0 any, arg1 any, arg2 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	_arg2 := arg2.(io.Reader)
	res, err := http.NewRequest(_arg0, _arg1, _arg2)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_NewRequestWithContext(arg0 any, arg1 any, arg2 any, arg3 any) SkyResult {
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	_arg2 := arg2.(string)
	_arg3 := arg3.(io.Reader)
	res, err := http.NewRequestWithContext(_arg0, _arg1, _arg2, _arg3)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_NewResponseController(arg0 any) *http.ResponseController {
	_arg0 := arg0.(http.ResponseWriter)
	return http.NewResponseController(_arg0)
}

func Sky_net_http_NewServeMux() *http.ServeMux {
	return http.NewServeMux()
}

func Sky_net_http_NotFound(arg0 any, arg1 any) any {
	_arg0 := arg0.(http.ResponseWriter)
	_arg1 := arg1.(*http.Request)
	http.NotFound(_arg0, _arg1)
	return struct{}{}
}

func Sky_net_http_NotFoundHandler() http.Handler {
	return http.NotFoundHandler()
}

func Sky_net_http_ParseCookie(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := http.ParseCookie(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_ParseHTTPVersion(arg0 any) (int, int, bool) {
	_arg0 := arg0.(string)
	return http.ParseHTTPVersion(_arg0)
}

func Sky_net_http_ParseSetCookie(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := http.ParseSetCookie(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_ParseTime(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := http.ParseTime(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_Post(arg0 any, arg1 any, arg2 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	_arg2 := arg2.(io.Reader)
	res, err := http.Post(_arg0, _arg1, _arg2)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_PostForm(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(url.Values)
	res, err := http.PostForm(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_ProxyFromEnvironment(arg0 any) SkyResult {
	_arg0 := arg0.(*http.Request)
	res, err := http.ProxyFromEnvironment(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_ProxyURL(arg0 any) func(*http.Request) (*url.URL, error) {
	_arg0 := arg0.(*url.URL)
	return http.ProxyURL(_arg0)
}

func Sky_net_http_ReadRequest(arg0 any) SkyResult {
	_arg0 := arg0.(*bufio.Reader)
	res, err := http.ReadRequest(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_ReadResponse(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(*bufio.Reader)
	_arg1 := arg1.(*http.Request)
	res, err := http.ReadResponse(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_Redirect(arg0 any, arg1 any, arg2 any, arg3 any) any {
	_arg0 := arg0.(http.ResponseWriter)
	_arg1 := arg1.(*http.Request)
	_arg2 := arg2.(string)
	_arg3 := arg3.(int)
	http.Redirect(_arg0, _arg1, _arg2, _arg3)
	return struct{}{}
}

func Sky_net_http_RedirectHandler(arg0 any, arg1 any) http.Handler {
	_arg0 := arg0.(string)
	_arg1 := arg1.(int)
	return http.RedirectHandler(_arg0, _arg1)
}

func Sky_net_http_Serve(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(net.Listener)
	_arg1 := arg1.(http.Handler)
	err := http.Serve(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_ServeContent(arg0 any, arg1 any, arg2 any, arg3 any, arg4 any) any {
	_arg0 := arg0.(http.ResponseWriter)
	_arg1 := arg1.(*http.Request)
	_arg2 := arg2.(string)
	_arg3 := arg3.(time.Time)
	_arg4 := arg4.(io.ReadSeeker)
	http.ServeContent(_arg0, _arg1, _arg2, _arg3, _arg4)
	return struct{}{}
}

func Sky_net_http_ServeFile(arg0 any, arg1 any, arg2 any) any {
	_arg0 := arg0.(http.ResponseWriter)
	_arg1 := arg1.(*http.Request)
	_arg2 := arg2.(string)
	http.ServeFile(_arg0, _arg1, _arg2)
	return struct{}{}
}

func Sky_net_http_ServeFileFS(arg0 any, arg1 any, arg2 any, arg3 any) any {
	_arg0 := arg0.(http.ResponseWriter)
	_arg1 := arg1.(*http.Request)
	_arg2 := arg2.(fs.FS)
	_arg3 := arg3.(string)
	http.ServeFileFS(_arg0, _arg1, _arg2, _arg3)
	return struct{}{}
}

func Sky_net_http_ServeTLS(arg0 any, arg1 any, arg2 any, arg3 any) SkyResult {
	_arg0 := arg0.(net.Listener)
	_arg1 := arg1.(http.Handler)
	_arg2 := arg2.(string)
	_arg3 := arg3.(string)
	err := http.ServeTLS(_arg0, _arg1, _arg2, _arg3)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_SetCookie(arg0 any, arg1 any) any {
	_arg0 := arg0.(http.ResponseWriter)
	_arg1 := arg1.(*http.Cookie)
	http.SetCookie(_arg0, _arg1)
	return struct{}{}
}

func Sky_net_http_StatusText(arg0 any) string {
	_arg0 := arg0.(int)
	return http.StatusText(_arg0)
}

func Sky_net_http_StripPrefix(arg0 any, arg1 any) http.Handler {
	_arg0 := arg0.(string)
	_arg1 := arg1.(http.Handler)
	return http.StripPrefix(_arg0, _arg1)
}

func Sky_net_http_TimeoutHandler(arg0 any, arg1 any, arg2 any) http.Handler {
	_arg0 := arg0.(http.Handler)
	_arg1 := arg1.(time.Duration)
	_arg2 := arg2.(string)
	return http.TimeoutHandler(_arg0, _arg1, _arg2)
}

func Sky_net_http_DefaultClient() any {
	return http.DefaultClient
}

func Sky_net_http_DefaultServeMux() any {
	return http.DefaultServeMux
}

func Sky_net_http_DefaultTransport() any {
	return http.DefaultTransport
}

func Sky_net_http_ErrAbortHandler() any {
	return http.ErrAbortHandler
}

func Sky_net_http_ErrBodyNotAllowed() any {
	return http.ErrBodyNotAllowed
}

func Sky_net_http_ErrBodyReadAfterClose() any {
	return http.ErrBodyReadAfterClose
}

func Sky_net_http_ErrContentLength() any {
	return http.ErrContentLength
}

func Sky_net_http_ErrHandlerTimeout() any {
	return http.ErrHandlerTimeout
}

func Sky_net_http_ErrHeaderTooLong() any {
	return http.ErrHeaderTooLong
}

func Sky_net_http_ErrHijacked() any {
	return http.ErrHijacked
}

func Sky_net_http_ErrLineTooLong() any {
	return http.ErrLineTooLong
}

func Sky_net_http_ErrMissingBoundary() any {
	return http.ErrMissingBoundary
}

func Sky_net_http_ErrMissingContentLength() any {
	return http.ErrMissingContentLength
}

func Sky_net_http_ErrMissingFile() any {
	return http.ErrMissingFile
}

func Sky_net_http_ErrNoCookie() any {
	return http.ErrNoCookie
}

func Sky_net_http_ErrNoLocation() any {
	return http.ErrNoLocation
}

func Sky_net_http_ErrNotMultipart() any {
	return http.ErrNotMultipart
}

func Sky_net_http_ErrNotSupported() any {
	return http.ErrNotSupported
}

func Sky_net_http_ErrSchemeMismatch() any {
	return http.ErrSchemeMismatch
}

func Sky_net_http_ErrServerClosed() any {
	return http.ErrServerClosed
}

func Sky_net_http_ErrShortBody() any {
	return http.ErrShortBody
}

func Sky_net_http_ErrSkipAltProtocol() any {
	return http.ErrSkipAltProtocol
}

func Sky_net_http_ErrUnexpectedTrailer() any {
	return http.ErrUnexpectedTrailer
}

func Sky_net_http_ErrUseLastResponse() any {
	return http.ErrUseLastResponse
}

func Sky_net_http_ErrWriteAfterFlush() any {
	return http.ErrWriteAfterFlush
}

func Sky_net_http_LocalAddrContextKey() any {
	return http.LocalAddrContextKey
}

func Sky_net_http_NoBody() any {
	return http.NoBody
}

func Sky_net_http_ServerContextKey() any {
	return http.ServerContextKey
}

func Sky_net_http_DefaultMaxHeaderBytes() any {
	return http.DefaultMaxHeaderBytes
}

func Sky_net_http_DefaultMaxIdleConnsPerHost() any {
	return http.DefaultMaxIdleConnsPerHost
}

func Sky_net_http_MethodConnect() any {
	return http.MethodConnect
}

func Sky_net_http_MethodDelete() any {
	return http.MethodDelete
}

func Sky_net_http_MethodGet() any {
	return http.MethodGet
}

func Sky_net_http_MethodHead() any {
	return http.MethodHead
}

func Sky_net_http_MethodOptions() any {
	return http.MethodOptions
}

func Sky_net_http_MethodPatch() any {
	return http.MethodPatch
}

func Sky_net_http_MethodPost() any {
	return http.MethodPost
}

func Sky_net_http_MethodPut() any {
	return http.MethodPut
}

func Sky_net_http_MethodTrace() any {
	return http.MethodTrace
}

func Sky_net_http_SameSiteDefaultMode() any {
	return http.SameSiteDefaultMode
}

func Sky_net_http_SameSiteLaxMode() any {
	return http.SameSiteLaxMode
}

func Sky_net_http_SameSiteNoneMode() any {
	return http.SameSiteNoneMode
}

func Sky_net_http_SameSiteStrictMode() any {
	return http.SameSiteStrictMode
}

func Sky_net_http_StateActive() any {
	return http.StateActive
}

func Sky_net_http_StateClosed() any {
	return http.StateClosed
}

func Sky_net_http_StateHijacked() any {
	return http.StateHijacked
}

func Sky_net_http_StateIdle() any {
	return http.StateIdle
}

func Sky_net_http_StateNew() any {
	return http.StateNew
}

func Sky_net_http_StatusAccepted() any {
	return http.StatusAccepted
}

func Sky_net_http_StatusAlreadyReported() any {
	return http.StatusAlreadyReported
}

func Sky_net_http_StatusBadGateway() any {
	return http.StatusBadGateway
}

func Sky_net_http_StatusBadRequest() any {
	return http.StatusBadRequest
}

func Sky_net_http_StatusConflict() any {
	return http.StatusConflict
}

func Sky_net_http_StatusContinue() any {
	return http.StatusContinue
}

func Sky_net_http_StatusCreated() any {
	return http.StatusCreated
}

func Sky_net_http_StatusEarlyHints() any {
	return http.StatusEarlyHints
}

func Sky_net_http_StatusExpectationFailed() any {
	return http.StatusExpectationFailed
}

func Sky_net_http_StatusFailedDependency() any {
	return http.StatusFailedDependency
}

func Sky_net_http_StatusForbidden() any {
	return http.StatusForbidden
}

func Sky_net_http_StatusFound() any {
	return http.StatusFound
}

func Sky_net_http_StatusGatewayTimeout() any {
	return http.StatusGatewayTimeout
}

func Sky_net_http_StatusGone() any {
	return http.StatusGone
}

func Sky_net_http_StatusHTTPVersionNotSupported() any {
	return http.StatusHTTPVersionNotSupported
}

func Sky_net_http_StatusIMUsed() any {
	return http.StatusIMUsed
}

func Sky_net_http_StatusInsufficientStorage() any {
	return http.StatusInsufficientStorage
}

func Sky_net_http_StatusInternalServerError() any {
	return http.StatusInternalServerError
}

func Sky_net_http_StatusLengthRequired() any {
	return http.StatusLengthRequired
}

func Sky_net_http_StatusLocked() any {
	return http.StatusLocked
}

func Sky_net_http_StatusLoopDetected() any {
	return http.StatusLoopDetected
}

func Sky_net_http_StatusMethodNotAllowed() any {
	return http.StatusMethodNotAllowed
}

func Sky_net_http_StatusMisdirectedRequest() any {
	return http.StatusMisdirectedRequest
}

func Sky_net_http_StatusMovedPermanently() any {
	return http.StatusMovedPermanently
}

func Sky_net_http_StatusMultiStatus() any {
	return http.StatusMultiStatus
}

func Sky_net_http_StatusMultipleChoices() any {
	return http.StatusMultipleChoices
}

func Sky_net_http_StatusNetworkAuthenticationRequired() any {
	return http.StatusNetworkAuthenticationRequired
}

func Sky_net_http_StatusNoContent() any {
	return http.StatusNoContent
}

func Sky_net_http_StatusNonAuthoritativeInfo() any {
	return http.StatusNonAuthoritativeInfo
}

func Sky_net_http_StatusNotAcceptable() any {
	return http.StatusNotAcceptable
}

func Sky_net_http_StatusNotExtended() any {
	return http.StatusNotExtended
}

func Sky_net_http_StatusNotFound() any {
	return http.StatusNotFound
}

func Sky_net_http_StatusNotImplemented() any {
	return http.StatusNotImplemented
}

func Sky_net_http_StatusNotModified() any {
	return http.StatusNotModified
}

func Sky_net_http_StatusOK() any {
	return http.StatusOK
}

func Sky_net_http_StatusPartialContent() any {
	return http.StatusPartialContent
}

func Sky_net_http_StatusPaymentRequired() any {
	return http.StatusPaymentRequired
}

func Sky_net_http_StatusPermanentRedirect() any {
	return http.StatusPermanentRedirect
}

func Sky_net_http_StatusPreconditionFailed() any {
	return http.StatusPreconditionFailed
}

func Sky_net_http_StatusPreconditionRequired() any {
	return http.StatusPreconditionRequired
}

func Sky_net_http_StatusProcessing() any {
	return http.StatusProcessing
}

func Sky_net_http_StatusProxyAuthRequired() any {
	return http.StatusProxyAuthRequired
}

func Sky_net_http_StatusRequestEntityTooLarge() any {
	return http.StatusRequestEntityTooLarge
}

func Sky_net_http_StatusRequestHeaderFieldsTooLarge() any {
	return http.StatusRequestHeaderFieldsTooLarge
}

func Sky_net_http_StatusRequestTimeout() any {
	return http.StatusRequestTimeout
}

func Sky_net_http_StatusRequestURITooLong() any {
	return http.StatusRequestURITooLong
}

func Sky_net_http_StatusRequestedRangeNotSatisfiable() any {
	return http.StatusRequestedRangeNotSatisfiable
}

func Sky_net_http_StatusResetContent() any {
	return http.StatusResetContent
}

func Sky_net_http_StatusSeeOther() any {
	return http.StatusSeeOther
}

func Sky_net_http_StatusServiceUnavailable() any {
	return http.StatusServiceUnavailable
}

func Sky_net_http_StatusSwitchingProtocols() any {
	return http.StatusSwitchingProtocols
}

func Sky_net_http_StatusTeapot() any {
	return http.StatusTeapot
}

func Sky_net_http_StatusTemporaryRedirect() any {
	return http.StatusTemporaryRedirect
}

func Sky_net_http_StatusTooEarly() any {
	return http.StatusTooEarly
}

func Sky_net_http_StatusTooManyRequests() any {
	return http.StatusTooManyRequests
}

func Sky_net_http_StatusUnauthorized() any {
	return http.StatusUnauthorized
}

func Sky_net_http_StatusUnavailableForLegalReasons() any {
	return http.StatusUnavailableForLegalReasons
}

func Sky_net_http_StatusUnprocessableEntity() any {
	return http.StatusUnprocessableEntity
}

func Sky_net_http_StatusUnsupportedMediaType() any {
	return http.StatusUnsupportedMediaType
}

func Sky_net_http_StatusUpgradeRequired() any {
	return http.StatusUpgradeRequired
}

func Sky_net_http_StatusUseProxy() any {
	return http.StatusUseProxy
}

func Sky_net_http_StatusVariantAlsoNegotiates() any {
	return http.StatusVariantAlsoNegotiates
}

func Sky_net_http_TimeFormat() any {
	return http.TimeFormat
}

func Sky_net_http_TrailerPrefix() any {
	return http.TrailerPrefix
}

func Sky_net_http_ClientCloseIdleConnections(this any) any {
	_this := this.(*http.Client)

	_this.CloseIdleConnections()
	return struct{}{}
}

func Sky_net_http_ClientDo(this any, arg0 any) SkyResult {
	_this := this.(*http.Client)
	_arg0 := arg0.(*http.Request)
	res, err := _this.Do(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_ClientGet(this any, arg0 any) SkyResult {
	_this := this.(*http.Client)
	_arg0 := arg0.(string)
	res, err := _this.Get(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_ClientHead(this any, arg0 any) SkyResult {
	_this := this.(*http.Client)
	_arg0 := arg0.(string)
	res, err := _this.Head(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_ClientPost(this any, arg0 any, arg1 any, arg2 any) SkyResult {
	_this := this.(*http.Client)
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	_arg2 := arg2.(io.Reader)
	res, err := _this.Post(_arg0, _arg1, _arg2)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_ClientPostForm(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(*http.Client)
	_arg0 := arg0.(string)
	_arg1 := arg1.(url.Values)
	res, err := _this.PostForm(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_ClientTransport(this any) http.RoundTripper {
	_this := this.(*http.Client)

	return _this.Transport
}

func Sky_net_http_ClientCheckRedirect(this any) func(req *http.Request, via []*http.Request) error {
	_this := this.(*http.Client)

	return _this.CheckRedirect
}

func Sky_net_http_ClientJar(this any) http.CookieJar {
	_this := this.(*http.Client)

	return _this.Jar
}

func Sky_net_http_ClientTimeout(this any) time.Duration {
	_this := this.(*http.Client)

	return _this.Timeout
}

func Sky_net_http_ClientConnAvailable(this any) int {
	_this := this.(*http.ClientConn)

	return _this.Available()
}

func Sky_net_http_ClientConnClose(this any) SkyResult {
	_this := this.(*http.ClientConn)

	err := _this.Close()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_ClientConnErr(this any) SkyResult {
	_this := this.(*http.ClientConn)

	err := _this.Err()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_ClientConnInFlight(this any) int {
	_this := this.(*http.ClientConn)

	return _this.InFlight()
}

func Sky_net_http_ClientConnRelease(this any) any {
	_this := this.(*http.ClientConn)

	_this.Release()
	return struct{}{}
}

func Sky_net_http_ClientConnReserve(this any) SkyResult {
	_this := this.(*http.ClientConn)

	err := _this.Reserve()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_ClientConnRoundTrip(this any, arg0 any) SkyResult {
	_this := this.(*http.ClientConn)
	_arg0 := arg0.(*http.Request)
	res, err := _this.RoundTrip(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_ClientConnSetStateHook(this any, arg0 any) any {
	_this := this.(*http.ClientConn)
	_skyFn0 := arg0.(func(any) any)
	_arg0 := func(p0 *http.ClientConn) {
		_skyFn0(p0)
	}
	_this.SetStateHook(_arg0)
	return struct{}{}
}

func Sky_net_http_CloseNotifierCloseNotify(this any) <-chan bool {
	_this := this.(http.CloseNotifier)

	return _this.CloseNotify()
}

func Sky_net_http_ConnStateString(this any) string {
	_this := this.(http.ConnState)

	return _this.String()
}

func Sky_net_http_CookieString(this any) string {
	_this := this.(*http.Cookie)

	return _this.String()
}

func Sky_net_http_CookieValid(this any) SkyResult {
	_this := this.(*http.Cookie)

	err := _this.Valid()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_CookieName(this any) string {
	_this := this.(*http.Cookie)

	return _this.Name
}

func Sky_net_http_CookieValue(this any) string {
	_this := this.(*http.Cookie)

	return _this.Value
}

func Sky_net_http_CookieQuoted(this any) bool {
	_this := this.(*http.Cookie)

	return _this.Quoted
}

func Sky_net_http_CookiePath(this any) string {
	_this := this.(*http.Cookie)

	return _this.Path
}

func Sky_net_http_CookieDomain(this any) string {
	_this := this.(*http.Cookie)

	return _this.Domain
}

func Sky_net_http_CookieExpires(this any) time.Time {
	_this := this.(*http.Cookie)

	return _this.Expires
}

func Sky_net_http_CookieRawExpires(this any) string {
	_this := this.(*http.Cookie)

	return _this.RawExpires
}

func Sky_net_http_CookieMaxAge(this any) int {
	_this := this.(*http.Cookie)

	return _this.MaxAge
}

func Sky_net_http_CookieSecure(this any) bool {
	_this := this.(*http.Cookie)

	return _this.Secure
}

func Sky_net_http_CookieHttpOnly(this any) bool {
	_this := this.(*http.Cookie)

	return _this.HttpOnly
}

func Sky_net_http_CookieSameSite(this any) http.SameSite {
	_this := this.(*http.Cookie)

	return _this.SameSite
}

func Sky_net_http_CookiePartitioned(this any) bool {
	_this := this.(*http.Cookie)

	return _this.Partitioned
}

func Sky_net_http_CookieRaw(this any) string {
	_this := this.(*http.Cookie)

	return _this.Raw
}

func Sky_net_http_CookieUnparsed(this any) any {
	_this := this.(*http.Cookie)

	_val := _this.Unparsed
	_result := make([]any, len(_val))
	for _i, _v := range _val { _result[_i] = _v }
	return _result
}

func Sky_net_http_CookieJarCookies(this any, arg0 any) []*http.Cookie {
	_this := this.(http.CookieJar)
	_arg0 := arg0.(*url.URL)
	return _this.Cookies(_arg0)
}

func Sky_net_http_CookieJarSetCookies(this any, arg0 any, arg1 any) any {
	_this := this.(http.CookieJar)
	_arg0 := arg0.(*url.URL)
	_arg1 := arg1.([]*http.Cookie)
	_this.SetCookies(_arg0, _arg1)
	return struct{}{}
}

func Sky_net_http_CrossOriginProtectionAddInsecureBypassPattern(this any, arg0 any) any {
	_this := this.(*http.CrossOriginProtection)
	_arg0 := arg0.(string)
	_this.AddInsecureBypassPattern(_arg0)
	return struct{}{}
}

func Sky_net_http_CrossOriginProtectionAddTrustedOrigin(this any, arg0 any) SkyResult {
	_this := this.(*http.CrossOriginProtection)
	_arg0 := arg0.(string)
	err := _this.AddTrustedOrigin(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_CrossOriginProtectionCheck(this any, arg0 any) SkyResult {
	_this := this.(*http.CrossOriginProtection)
	_arg0 := arg0.(*http.Request)
	err := _this.Check(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_CrossOriginProtectionHandler(this any, arg0 any) http.Handler {
	_this := this.(*http.CrossOriginProtection)
	_arg0 := arg0.(http.Handler)
	return _this.Handler(_arg0)
}

func Sky_net_http_CrossOriginProtectionSetDenyHandler(this any, arg0 any) any {
	_this := this.(*http.CrossOriginProtection)
	_arg0 := arg0.(http.Handler)
	_this.SetDenyHandler(_arg0)
	return struct{}{}
}

func Sky_net_http_DirOpen(this any, arg0 any) SkyResult {
	_this := this.(http.Dir)
	_arg0 := arg0.(string)
	res, err := _this.Open(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_FileReaddir(this any, arg0 any) SkyResult {
	_this := this.(http.File)
	_arg0 := arg0.(int)
	res, err := _this.Readdir(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_FileStat(this any) SkyResult {
	_this := this.(http.File)

	res, err := _this.Stat()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_FileSystemOpen(this any, arg0 any) SkyResult {
	_this := this.(http.FileSystem)
	_arg0 := arg0.(string)
	res, err := _this.Open(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_FlusherFlush(this any) any {
	_this := this.(http.Flusher)

	_this.Flush()
	return struct{}{}
}

func Sky_net_http_HTTP2ConfigMaxConcurrentStreams(this any) int {
	_this := this.(*http.HTTP2Config)

	return _this.MaxConcurrentStreams
}

func Sky_net_http_HTTP2ConfigStrictMaxConcurrentRequests(this any) bool {
	_this := this.(*http.HTTP2Config)

	return _this.StrictMaxConcurrentRequests
}

func Sky_net_http_HTTP2ConfigMaxDecoderHeaderTableSize(this any) int {
	_this := this.(*http.HTTP2Config)

	return _this.MaxDecoderHeaderTableSize
}

func Sky_net_http_HTTP2ConfigMaxEncoderHeaderTableSize(this any) int {
	_this := this.(*http.HTTP2Config)

	return _this.MaxEncoderHeaderTableSize
}

func Sky_net_http_HTTP2ConfigMaxReadFrameSize(this any) int {
	_this := this.(*http.HTTP2Config)

	return _this.MaxReadFrameSize
}

func Sky_net_http_HTTP2ConfigMaxReceiveBufferPerConnection(this any) int {
	_this := this.(*http.HTTP2Config)

	return _this.MaxReceiveBufferPerConnection
}

func Sky_net_http_HTTP2ConfigMaxReceiveBufferPerStream(this any) int {
	_this := this.(*http.HTTP2Config)

	return _this.MaxReceiveBufferPerStream
}

func Sky_net_http_HTTP2ConfigSendPingTimeout(this any) time.Duration {
	_this := this.(*http.HTTP2Config)

	return _this.SendPingTimeout
}

func Sky_net_http_HTTP2ConfigPingTimeout(this any) time.Duration {
	_this := this.(*http.HTTP2Config)

	return _this.PingTimeout
}

func Sky_net_http_HTTP2ConfigWriteByteTimeout(this any) time.Duration {
	_this := this.(*http.HTTP2Config)

	return _this.WriteByteTimeout
}

func Sky_net_http_HTTP2ConfigPermitProhibitedCipherSuites(this any) bool {
	_this := this.(*http.HTTP2Config)

	return _this.PermitProhibitedCipherSuites
}

func Sky_net_http_HTTP2ConfigCountError(this any) func(errType string) {
	_this := this.(*http.HTTP2Config)

	return _this.CountError
}

func Sky_net_http_HandlerServeHTTP(this any, arg0 any, arg1 any) any {
	_this := this.(http.Handler)
	_arg0 := arg0.(http.ResponseWriter)
	_arg1 := arg1.(*http.Request)
	_this.ServeHTTP(_arg0, _arg1)
	return struct{}{}
}

func Sky_net_http_HandlerFuncServeHTTP(this any, arg0 any, arg1 any) any {
	_this := this.(http.HandlerFunc)
	_arg0 := arg0.(http.ResponseWriter)
	_arg1 := arg1.(*http.Request)
	_this.ServeHTTP(_arg0, _arg1)
	return struct{}{}
}

func Sky_net_http_HeaderAdd(this any, arg0 any, arg1 any) any {
	_this := this.(http.Header)
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	_this.Add(_arg0, _arg1)
	return struct{}{}
}

func Sky_net_http_HeaderClone(this any) http.Header {
	_this := this.(http.Header)

	return _this.Clone()
}

func Sky_net_http_HeaderDel(this any, arg0 any) any {
	_this := this.(http.Header)
	_arg0 := arg0.(string)
	_this.Del(_arg0)
	return struct{}{}
}

func Sky_net_http_HeaderGet(this any, arg0 any) string {
	_this := this.(http.Header)
	_arg0 := arg0.(string)
	return _this.Get(_arg0)
}

func Sky_net_http_HeaderSet(this any, arg0 any, arg1 any) any {
	_this := this.(http.Header)
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	_this.Set(_arg0, _arg1)
	return struct{}{}
}

func Sky_net_http_HeaderValues(this any, arg0 any) []string {
	_this := this.(http.Header)
	_arg0 := arg0.(string)
	return _this.Values(_arg0)
}

func Sky_net_http_HeaderWrite(this any, arg0 any) SkyResult {
	_this := this.(http.Header)
	_arg0 := arg0.(io.Writer)
	err := _this.Write(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_HeaderWriteSubset(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(http.Header)
	_arg0 := arg0.(io.Writer)
	_arg1 := arg1.(map[string]bool)
	err := _this.WriteSubset(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_HijackerHijack(this any) (net.Conn, *bufio.ReadWriter, error) {
	_this := this.(http.Hijacker)

	return _this.Hijack()
}

func Sky_net_http_MaxBytesErrorError(this any) string {
	_this := this.(*http.MaxBytesError)

	return _this.Error()
}

func Sky_net_http_MaxBytesErrorLimit(this any) int64 {
	_this := this.(*http.MaxBytesError)

	return _this.Limit
}

func Sky_net_http_ProtocolErrorError(this any) string {
	_this := this.(*http.ProtocolError)

	return _this.Error()
}

func Sky_net_http_ProtocolErrorIs(this any, arg0 any) bool {
	_this := this.(*http.ProtocolError)
	_arg0 := arg0.(error)
	return _this.Is(_arg0)
}

func Sky_net_http_ProtocolErrorErrorString(this any) string {
	_this := this.(*http.ProtocolError)

	return _this.ErrorString
}

func Sky_net_http_ProtocolsHTTP1(this any) bool {
	_this := this.(*http.Protocols)

	return _this.HTTP1()
}

func Sky_net_http_ProtocolsHTTP2(this any) bool {
	_this := this.(*http.Protocols)

	return _this.HTTP2()
}

func Sky_net_http_ProtocolsSetHTTP1(this any, arg0 any) any {
	_this := this.(*http.Protocols)
	_arg0 := arg0.(bool)
	_this.SetHTTP1(_arg0)
	return struct{}{}
}

func Sky_net_http_ProtocolsSetHTTP2(this any, arg0 any) any {
	_this := this.(*http.Protocols)
	_arg0 := arg0.(bool)
	_this.SetHTTP2(_arg0)
	return struct{}{}
}

func Sky_net_http_ProtocolsSetUnencryptedHTTP2(this any, arg0 any) any {
	_this := this.(*http.Protocols)
	_arg0 := arg0.(bool)
	_this.SetUnencryptedHTTP2(_arg0)
	return struct{}{}
}

func Sky_net_http_ProtocolsString(this any) string {
	_this := this.(*http.Protocols)

	return _this.String()
}

func Sky_net_http_ProtocolsUnencryptedHTTP2(this any) bool {
	_this := this.(*http.Protocols)

	return _this.UnencryptedHTTP2()
}

func Sky_net_http_PushOptionsMethod(this any) string {
	_this := this.(*http.PushOptions)

	return _this.Method
}

func Sky_net_http_PushOptionsHeader(this any) http.Header {
	_this := this.(*http.PushOptions)

	return _this.Header
}

func Sky_net_http_PusherPush(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(http.Pusher)
	_arg0 := arg0.(string)
	_arg1 := arg1.(*http.PushOptions)
	err := _this.Push(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_RequestAddCookie(this any, arg0 any) any {
	_this := this.(*http.Request)
	_arg0 := arg0.(*http.Cookie)
	_this.AddCookie(_arg0)
	return struct{}{}
}

func Sky_net_http_RequestBasicAuth(this any) (string, string, bool) {
	_this := this.(*http.Request)

	return _this.BasicAuth()
}

func Sky_net_http_RequestClone(this any, arg0 any) *http.Request {
	_this := this.(*http.Request)
	_arg0 := arg0.(context.Context)
	return _this.Clone(_arg0)
}

func Sky_net_http_RequestContext(this any) context.Context {
	_this := this.(*http.Request)

	return _this.Context()
}

func Sky_net_http_RequestCookie(this any, arg0 any) SkyResult {
	_this := this.(*http.Request)
	_arg0 := arg0.(string)
	res, err := _this.Cookie(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_RequestCookies(this any) []*http.Cookie {
	_this := this.(*http.Request)

	return _this.Cookies()
}

func Sky_net_http_RequestCookiesNamed(this any, arg0 any) []*http.Cookie {
	_this := this.(*http.Request)
	_arg0 := arg0.(string)
	return _this.CookiesNamed(_arg0)
}

func Sky_net_http_RequestFormFile(this any, arg0 any) (multipart.File, *multipart.FileHeader, error) {
	_this := this.(*http.Request)
	_arg0 := arg0.(string)
	return _this.FormFile(_arg0)
}

func Sky_net_http_RequestFormValue(this any, arg0 any) string {
	_this := this.(*http.Request)
	_arg0 := arg0.(string)
	return _this.FormValue(_arg0)
}

func Sky_net_http_RequestMultipartReader(this any) SkyResult {
	_this := this.(*http.Request)

	res, err := _this.MultipartReader()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_RequestParseForm(this any) SkyResult {
	_this := this.(*http.Request)

	err := _this.ParseForm()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_RequestParseMultipartForm(this any, arg0 any) SkyResult {
	_this := this.(*http.Request)
	_arg0 := arg0.(int64)
	err := _this.ParseMultipartForm(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_RequestPathValue(this any, arg0 any) string {
	_this := this.(*http.Request)
	_arg0 := arg0.(string)
	return _this.PathValue(_arg0)
}

func Sky_net_http_RequestPostFormValue(this any, arg0 any) string {
	_this := this.(*http.Request)
	_arg0 := arg0.(string)
	return _this.PostFormValue(_arg0)
}

func Sky_net_http_RequestProtoAtLeast(this any, arg0 any, arg1 any) bool {
	_this := this.(*http.Request)
	_arg0 := arg0.(int)
	_arg1 := arg1.(int)
	return _this.ProtoAtLeast(_arg0, _arg1)
}

func Sky_net_http_RequestReferer(this any) string {
	_this := this.(*http.Request)

	return _this.Referer()
}

func Sky_net_http_RequestSetBasicAuth(this any, arg0 any, arg1 any) any {
	_this := this.(*http.Request)
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	_this.SetBasicAuth(_arg0, _arg1)
	return struct{}{}
}

func Sky_net_http_RequestSetPathValue(this any, arg0 any, arg1 any) any {
	_this := this.(*http.Request)
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	_this.SetPathValue(_arg0, _arg1)
	return struct{}{}
}

func Sky_net_http_RequestUserAgent(this any) string {
	_this := this.(*http.Request)

	return _this.UserAgent()
}

func Sky_net_http_RequestWithContext(this any, arg0 any) *http.Request {
	_this := this.(*http.Request)
	_arg0 := arg0.(context.Context)
	return _this.WithContext(_arg0)
}

func Sky_net_http_RequestWrite(this any, arg0 any) SkyResult {
	_this := this.(*http.Request)
	_arg0 := arg0.(io.Writer)
	err := _this.Write(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_RequestWriteProxy(this any, arg0 any) SkyResult {
	_this := this.(*http.Request)
	_arg0 := arg0.(io.Writer)
	err := _this.WriteProxy(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_RequestMethod(this any) string {
	_this := this.(*http.Request)

	return _this.Method
}

func Sky_net_http_RequestURL(this any) *url.URL {
	_this := this.(*http.Request)

	return _this.URL
}

func Sky_net_http_RequestProto(this any) string {
	_this := this.(*http.Request)

	return _this.Proto
}

func Sky_net_http_RequestProtoMajor(this any) int {
	_this := this.(*http.Request)

	return _this.ProtoMajor
}

func Sky_net_http_RequestProtoMinor(this any) int {
	_this := this.(*http.Request)

	return _this.ProtoMinor
}

func Sky_net_http_RequestHeader(this any) http.Header {
	_this := this.(*http.Request)

	return _this.Header
}

func Sky_net_http_RequestBody(this any) io.ReadCloser {
	_this := this.(*http.Request)

	return _this.Body
}

func Sky_net_http_RequestGetBody(this any) func() (io.ReadCloser, error) {
	_this := this.(*http.Request)

	return _this.GetBody
}

func Sky_net_http_RequestContentLength(this any) int64 {
	_this := this.(*http.Request)

	return _this.ContentLength
}

func Sky_net_http_RequestTransferEncoding(this any) any {
	_this := this.(*http.Request)

	_val := _this.TransferEncoding
	_result := make([]any, len(_val))
	for _i, _v := range _val { _result[_i] = _v }
	return _result
}

func Sky_net_http_RequestClose(this any) bool {
	_this := this.(*http.Request)

	return _this.Close
}

func Sky_net_http_RequestHost(this any) string {
	_this := this.(*http.Request)

	return _this.Host
}

func Sky_net_http_RequestForm(this any) url.Values {
	_this := this.(*http.Request)

	return _this.Form
}

func Sky_net_http_RequestPostForm(this any) url.Values {
	_this := this.(*http.Request)

	return _this.PostForm
}

func Sky_net_http_RequestMultipartForm(this any) *multipart.Form {
	_this := this.(*http.Request)

	return _this.MultipartForm
}

func Sky_net_http_RequestTrailer(this any) http.Header {
	_this := this.(*http.Request)

	return _this.Trailer
}

func Sky_net_http_RequestRemoteAddr(this any) string {
	_this := this.(*http.Request)

	return _this.RemoteAddr
}

func Sky_net_http_RequestRequestURI(this any) string {
	_this := this.(*http.Request)

	return _this.RequestURI
}

func Sky_net_http_RequestTLS(this any) *tls.ConnectionState {
	_this := this.(*http.Request)

	return _this.TLS
}

func Sky_net_http_RequestCancel(this any) <-chan struct{} {
	_this := this.(*http.Request)

	return _this.Cancel
}

func Sky_net_http_RequestResponse(this any) *http.Response {
	_this := this.(*http.Request)

	return _this.Response
}

func Sky_net_http_RequestPattern(this any) string {
	_this := this.(*http.Request)

	return _this.Pattern
}

func Sky_net_http_ResponseCookies(this any) []*http.Cookie {
	_this := this.(*http.Response)

	return _this.Cookies()
}

func Sky_net_http_ResponseLocation(this any) SkyResult {
	_this := this.(*http.Response)

	res, err := _this.Location()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_ResponseProtoAtLeast(this any, arg0 any, arg1 any) bool {
	_this := this.(*http.Response)
	_arg0 := arg0.(int)
	_arg1 := arg1.(int)
	return _this.ProtoAtLeast(_arg0, _arg1)
}

func Sky_net_http_ResponseWrite(this any, arg0 any) SkyResult {
	_this := this.(*http.Response)
	_arg0 := arg0.(io.Writer)
	err := _this.Write(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_ResponseStatus(this any) string {
	_this := this.(*http.Response)

	return _this.Status
}

func Sky_net_http_ResponseStatusCode(this any) int {
	_this := this.(*http.Response)

	return _this.StatusCode
}

func Sky_net_http_ResponseProto(this any) string {
	_this := this.(*http.Response)

	return _this.Proto
}

func Sky_net_http_ResponseProtoMajor(this any) int {
	_this := this.(*http.Response)

	return _this.ProtoMajor
}

func Sky_net_http_ResponseProtoMinor(this any) int {
	_this := this.(*http.Response)

	return _this.ProtoMinor
}

func Sky_net_http_ResponseHeader(this any) http.Header {
	_this := this.(*http.Response)

	return _this.Header
}

func Sky_net_http_ResponseBody(this any) io.ReadCloser {
	_this := this.(*http.Response)

	return _this.Body
}

func Sky_net_http_ResponseContentLength(this any) int64 {
	_this := this.(*http.Response)

	return _this.ContentLength
}

func Sky_net_http_ResponseTransferEncoding(this any) any {
	_this := this.(*http.Response)

	_val := _this.TransferEncoding
	_result := make([]any, len(_val))
	for _i, _v := range _val { _result[_i] = _v }
	return _result
}

func Sky_net_http_ResponseClose(this any) bool {
	_this := this.(*http.Response)

	return _this.Close
}

func Sky_net_http_ResponseUncompressed(this any) bool {
	_this := this.(*http.Response)

	return _this.Uncompressed
}

func Sky_net_http_ResponseTrailer(this any) http.Header {
	_this := this.(*http.Response)

	return _this.Trailer
}

func Sky_net_http_ResponseRequest(this any) *http.Request {
	_this := this.(*http.Response)

	return _this.Request
}

func Sky_net_http_ResponseTLS(this any) *tls.ConnectionState {
	_this := this.(*http.Response)

	return _this.TLS
}

func Sky_net_http_ResponseControllerEnableFullDuplex(this any) SkyResult {
	_this := this.(*http.ResponseController)

	err := _this.EnableFullDuplex()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_ResponseControllerFlush(this any) SkyResult {
	_this := this.(*http.ResponseController)

	err := _this.Flush()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_ResponseControllerHijack(this any) (net.Conn, *bufio.ReadWriter, error) {
	_this := this.(*http.ResponseController)

	return _this.Hijack()
}

func Sky_net_http_ResponseControllerSetReadDeadline(this any, arg0 any) SkyResult {
	_this := this.(*http.ResponseController)
	_arg0 := arg0.(time.Time)
	err := _this.SetReadDeadline(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_ResponseControllerSetWriteDeadline(this any, arg0 any) SkyResult {
	_this := this.(*http.ResponseController)
	_arg0 := arg0.(time.Time)
	err := _this.SetWriteDeadline(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_ResponseWriterHeader(this any) http.Header {
	_this := this.(http.ResponseWriter)

	return _this.Header()
}

func Sky_net_http_ResponseWriterWrite(this any, arg0 any) SkyResult {
	_this := this.(http.ResponseWriter)
	_arg0 := arg0.([]byte)
	res, err := _this.Write(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_ResponseWriterWriteHeader(this any, arg0 any) any {
	_this := this.(http.ResponseWriter)
	_arg0 := arg0.(int)
	_this.WriteHeader(_arg0)
	return struct{}{}
}

func Sky_net_http_RoundTripperRoundTrip(this any, arg0 any) SkyResult {
	_this := this.(http.RoundTripper)
	_arg0 := arg0.(*http.Request)
	res, err := _this.RoundTrip(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_ServeMuxHandle(this any, arg0 any, arg1 any) any {
	_this := this.(*http.ServeMux)
	_arg0 := arg0.(string)
	_arg1 := arg1.(http.Handler)
	_this.Handle(_arg0, _arg1)
	return struct{}{}
}

func Sky_net_http_ServeMuxHandleFunc(this any, arg0 any, arg1 any) any {
	_this := this.(*http.ServeMux)
	_arg0 := arg0.(string)
	_skyFn1 := arg1.(func(any) any)
	_arg1 := func(p0 http.ResponseWriter, p1 *http.Request) {
		_skyFn1(p0).(func(any) any)(p1)
	}
	_this.HandleFunc(_arg0, _arg1)
	return struct{}{}
}

func Sky_net_http_ServeMuxHandler(this any, arg0 any) (http.Handler, string) {
	_this := this.(*http.ServeMux)
	_arg0 := arg0.(*http.Request)
	return _this.Handler(_arg0)
}

func Sky_net_http_ServeMuxServeHTTP(this any, arg0 any, arg1 any) any {
	_this := this.(*http.ServeMux)
	_arg0 := arg0.(http.ResponseWriter)
	_arg1 := arg1.(*http.Request)
	_this.ServeHTTP(_arg0, _arg1)
	return struct{}{}
}

func Sky_net_http_ServerClose(this any) SkyResult {
	_this := this.(*http.Server)

	err := _this.Close()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_ServerListenAndServe(this any) SkyResult {
	_this := this.(*http.Server)

	err := _this.ListenAndServe()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_ServerListenAndServeTLS(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(*http.Server)
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	err := _this.ListenAndServeTLS(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_ServerRegisterOnShutdown(this any, arg0 any) any {
	_this := this.(*http.Server)
	_skyFn0 := arg0.(func(any) any)
	_arg0 := func() {
		_skyFn0(nil)
	}
	_this.RegisterOnShutdown(_arg0)
	return struct{}{}
}

func Sky_net_http_ServerServe(this any, arg0 any) SkyResult {
	_this := this.(*http.Server)
	_arg0 := arg0.(net.Listener)
	err := _this.Serve(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_ServerServeTLS(this any, arg0 any, arg1 any, arg2 any) SkyResult {
	_this := this.(*http.Server)
	_arg0 := arg0.(net.Listener)
	_arg1 := arg1.(string)
	_arg2 := arg2.(string)
	err := _this.ServeTLS(_arg0, _arg1, _arg2)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_ServerSetKeepAlivesEnabled(this any, arg0 any) any {
	_this := this.(*http.Server)
	_arg0 := arg0.(bool)
	_this.SetKeepAlivesEnabled(_arg0)
	return struct{}{}
}

func Sky_net_http_ServerShutdown(this any, arg0 any) SkyResult {
	_this := this.(*http.Server)
	_arg0 := arg0.(context.Context)
	err := _this.Shutdown(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_http_ServerAddr(this any) string {
	_this := this.(*http.Server)

	return _this.Addr
}

func Sky_net_http_ServerHandler(this any) http.Handler {
	_this := this.(*http.Server)

	return _this.Handler
}

func Sky_net_http_ServerDisableGeneralOptionsHandler(this any) bool {
	_this := this.(*http.Server)

	return _this.DisableGeneralOptionsHandler
}

func Sky_net_http_ServerTLSConfig(this any) *tls.Config {
	_this := this.(*http.Server)

	return _this.TLSConfig
}

func Sky_net_http_ServerReadTimeout(this any) time.Duration {
	_this := this.(*http.Server)

	return _this.ReadTimeout
}

func Sky_net_http_ServerReadHeaderTimeout(this any) time.Duration {
	_this := this.(*http.Server)

	return _this.ReadHeaderTimeout
}

func Sky_net_http_ServerWriteTimeout(this any) time.Duration {
	_this := this.(*http.Server)

	return _this.WriteTimeout
}

func Sky_net_http_ServerIdleTimeout(this any) time.Duration {
	_this := this.(*http.Server)

	return _this.IdleTimeout
}

func Sky_net_http_ServerMaxHeaderBytes(this any) int {
	_this := this.(*http.Server)

	return _this.MaxHeaderBytes
}

func Sky_net_http_ServerTLSNextProto(this any) map[string]func(*http.Server, *tls.Conn, http.Handler) {
	_this := this.(*http.Server)

	return _this.TLSNextProto
}

func Sky_net_http_ServerConnState(this any) func(net.Conn, http.ConnState) {
	_this := this.(*http.Server)

	return _this.ConnState
}

func Sky_net_http_ServerErrorLog(this any) *log.Logger {
	_this := this.(*http.Server)

	return _this.ErrorLog
}

func Sky_net_http_ServerBaseContext(this any) func(net.Listener) context.Context {
	_this := this.(*http.Server)

	return _this.BaseContext
}

func Sky_net_http_ServerConnContext(this any) func(ctx context.Context, c net.Conn) context.Context {
	_this := this.(*http.Server)

	return _this.ConnContext
}

func Sky_net_http_ServerHTTP2(this any) *http.HTTP2Config {
	_this := this.(*http.Server)

	return _this.HTTP2
}

func Sky_net_http_ServerProtocols(this any) *http.Protocols {
	_this := this.(*http.Server)

	return _this.Protocols
}

func Sky_net_http_TransportCancelRequest(this any, arg0 any) any {
	_this := this.(*http.Transport)
	_arg0 := arg0.(*http.Request)
	_this.CancelRequest(_arg0)
	return struct{}{}
}

func Sky_net_http_TransportClone(this any) *http.Transport {
	_this := this.(*http.Transport)

	return _this.Clone()
}

func Sky_net_http_TransportCloseIdleConnections(this any) any {
	_this := this.(*http.Transport)

	_this.CloseIdleConnections()
	return struct{}{}
}

func Sky_net_http_TransportNewClientConn(this any, arg0 any, arg1 any, arg2 any) SkyResult {
	_this := this.(*http.Transport)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	_arg2 := arg2.(string)
	res, err := _this.NewClientConn(_arg0, _arg1, _arg2)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_TransportRegisterProtocol(this any, arg0 any, arg1 any) any {
	_this := this.(*http.Transport)
	_arg0 := arg0.(string)
	_arg1 := arg1.(http.RoundTripper)
	_this.RegisterProtocol(_arg0, _arg1)
	return struct{}{}
}

func Sky_net_http_TransportRoundTrip(this any, arg0 any) SkyResult {
	_this := this.(*http.Transport)
	_arg0 := arg0.(*http.Request)
	res, err := _this.RoundTrip(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_http_TransportProxy(this any) func(*http.Request) (*url.URL, error) {
	_this := this.(*http.Transport)

	return _this.Proxy
}

func Sky_net_http_TransportOnProxyConnectResponse(this any) func(ctx context.Context, proxyURL *url.URL, connectReq *http.Request, connectRes *http.Response) error {
	_this := this.(*http.Transport)

	return _this.OnProxyConnectResponse
}

func Sky_net_http_TransportDialContext(this any) func(ctx context.Context, network string, addr string) (net.Conn, error) {
	_this := this.(*http.Transport)

	return _this.DialContext
}

func Sky_net_http_TransportDial(this any) func(network string, addr string) (net.Conn, error) {
	_this := this.(*http.Transport)

	return _this.Dial
}

func Sky_net_http_TransportDialTLSContext(this any) func(ctx context.Context, network string, addr string) (net.Conn, error) {
	_this := this.(*http.Transport)

	return _this.DialTLSContext
}

func Sky_net_http_TransportDialTLS(this any) func(network string, addr string) (net.Conn, error) {
	_this := this.(*http.Transport)

	return _this.DialTLS
}

func Sky_net_http_TransportTLSClientConfig(this any) *tls.Config {
	_this := this.(*http.Transport)

	return _this.TLSClientConfig
}

func Sky_net_http_TransportTLSHandshakeTimeout(this any) time.Duration {
	_this := this.(*http.Transport)

	return _this.TLSHandshakeTimeout
}

func Sky_net_http_TransportDisableKeepAlives(this any) bool {
	_this := this.(*http.Transport)

	return _this.DisableKeepAlives
}

func Sky_net_http_TransportDisableCompression(this any) bool {
	_this := this.(*http.Transport)

	return _this.DisableCompression
}

func Sky_net_http_TransportMaxIdleConns(this any) int {
	_this := this.(*http.Transport)

	return _this.MaxIdleConns
}

func Sky_net_http_TransportMaxIdleConnsPerHost(this any) int {
	_this := this.(*http.Transport)

	return _this.MaxIdleConnsPerHost
}

func Sky_net_http_TransportMaxConnsPerHost(this any) int {
	_this := this.(*http.Transport)

	return _this.MaxConnsPerHost
}

func Sky_net_http_TransportIdleConnTimeout(this any) time.Duration {
	_this := this.(*http.Transport)

	return _this.IdleConnTimeout
}

func Sky_net_http_TransportResponseHeaderTimeout(this any) time.Duration {
	_this := this.(*http.Transport)

	return _this.ResponseHeaderTimeout
}

func Sky_net_http_TransportExpectContinueTimeout(this any) time.Duration {
	_this := this.(*http.Transport)

	return _this.ExpectContinueTimeout
}

func Sky_net_http_TransportTLSNextProto(this any) map[string]func(authority string, c *tls.Conn) http.RoundTripper {
	_this := this.(*http.Transport)

	return _this.TLSNextProto
}

func Sky_net_http_TransportProxyConnectHeader(this any) http.Header {
	_this := this.(*http.Transport)

	return _this.ProxyConnectHeader
}

func Sky_net_http_TransportGetProxyConnectHeader(this any) func(ctx context.Context, proxyURL *url.URL, target string) (http.Header, error) {
	_this := this.(*http.Transport)

	return _this.GetProxyConnectHeader
}

func Sky_net_http_TransportMaxResponseHeaderBytes(this any) int64 {
	_this := this.(*http.Transport)

	return _this.MaxResponseHeaderBytes
}

func Sky_net_http_TransportWriteBufferSize(this any) int {
	_this := this.(*http.Transport)

	return _this.WriteBufferSize
}

func Sky_net_http_TransportReadBufferSize(this any) int {
	_this := this.(*http.Transport)

	return _this.ReadBufferSize
}

func Sky_net_http_TransportForceAttemptHTTP2(this any) bool {
	_this := this.(*http.Transport)

	return _this.ForceAttemptHTTP2
}

func Sky_net_http_TransportHTTP2(this any) *http.HTTP2Config {
	_this := this.(*http.Transport)

	return _this.HTTP2
}

func Sky_net_http_TransportProtocols(this any) *http.Protocols {
	_this := this.(*http.Transport)

	return _this.Protocols
}

