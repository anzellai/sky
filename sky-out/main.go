package main

import (
	"bufio"
	"context"
	"fmt"
	net_http "net/http"
	"os"
	"strings"
	"time"
)

var skyVersion = "dev"

type SkyTuple2 struct{ V0, V1 any }

type SkyTuple3 struct{ V0, V1, V2 any }

type SkyResult struct {
	Tag               int
	SkyName           string
	OkValue, ErrValue any
}

type SkyMaybe struct {
	Tag       int
	SkyName   string
	JustValue any
}

var stdinReader *bufio.Reader
var sky_jsonDecoder_string = func(v any) any {
	if s, ok := v.(string); ok {
		return SkyOk(s)
	}
	return SkyErr("expected string")
}
var sky_jsonDecoder_int = func(v any) any {
	switch n := v.(type) {
	case float64:
		return SkyOk(int(n))
	case int:
		return SkyOk(n)
	}
	return SkyErr("expected int")
}
var sky_jsonDecoder_float = func(v any) any {
	if f, ok := v.(float64); ok {
		return SkyOk(f)
	}
	return SkyErr("expected float")
}
var sky_jsonDecoder_bool = func(v any) any {
	if b, ok := v.(bool); ok {
		return SkyOk(b)
	}
	return SkyErr("expected bool")
}
var sky_liveAppImpl = func(config any) any { return config }
var Duration = Time_Duration
var Location = Time_Location
var Month = Time_Month
var ParseError = Time_ParseError
var Ticker = Time_Ticker
var Time = Time_Time
var Timer = Time_Timer
var Weekday = Time_Weekday
var After = Time_After
var Date = Time_Date
var FixedZone = Time_FixedZone
var LoadLocation = Time_LoadLocation
var LoadLocationFromTZData = Time_LoadLocationFromTZData
var NewTicker = Time_NewTicker
var NewTimer = Time_NewTimer
var Now = Time_Now
var Parse = Time_Parse
var ParseDuration = Time_ParseDuration
var ParseInLocation = Time_ParseInLocation
var Since = Time_Since
var Sleep = Time_Sleep
var Tick = Time_Tick
var Unix = Time_Unix
var UnixMicro = Time_UnixMicro
var UnixMilli = Time_UnixMilli
var Until = Time_Until
var ParseErrorLayout = Time_ParseErrorLayout
var ParseErrorValue = Time_ParseErrorValue
var ParseErrorLayoutElem = Time_ParseErrorLayoutElem
var ParseErrorValueElem = Time_ParseErrorValueElem
var ParseErrorMessage = Time_ParseErrorMessage
var TickerC = Time_TickerC
var TimerC = Time_TimerC
var DurationAbs = Time_DurationAbs
var DurationHours = Time_DurationHours
var DurationMicroseconds = Time_DurationMicroseconds
var DurationMilliseconds = Time_DurationMilliseconds
var DurationMinutes = Time_DurationMinutes
var DurationNanoseconds = Time_DurationNanoseconds
var DurationRound = Time_DurationRound
var DurationSeconds = Time_DurationSeconds
var DurationString = Time_DurationString
var DurationTruncate = Time_DurationTruncate
var LocationString = Time_LocationString
var MonthString = Time_MonthString
var ParseErrorError = Time_ParseErrorError
var TickerReset = Time_TickerReset
var TickerStop = Time_TickerStop
var TimeAdd = Time_TimeAdd
var TimeAddDate = Time_TimeAddDate
var TimeAfter = Time_TimeAfter
var TimeAppendBinary = Time_TimeAppendBinary
var TimeAppendFormat = Time_TimeAppendFormat
var TimeAppendText = Time_TimeAppendText
var TimeBefore = Time_TimeBefore
var TimeCompare = Time_TimeCompare
var TimeDay = Time_TimeDay
var TimeEqual = Time_TimeEqual
var TimeFormat = Time_TimeFormat
var TimeGoString = Time_TimeGoString
var TimeGobDecode = Time_TimeGobDecode
var TimeGobEncode = Time_TimeGobEncode
var TimeHour = Time_TimeHour
var TimeISOWeek = Time_TimeISOWeek
var TimeIn = Time_TimeIn
var TimeIsDST = Time_TimeIsDST
var TimeIsZero = Time_TimeIsZero
var TimeLocal = Time_TimeLocal
var TimeLocation = Time_TimeLocation
var TimeMarshalBinary = Time_TimeMarshalBinary
var TimeMarshalJSON = Time_TimeMarshalJSON
var TimeMarshalText = Time_TimeMarshalText
var TimeMinute = Time_TimeMinute
var TimeMonth = Time_TimeMonth
var TimeNanosecond = Time_TimeNanosecond
var TimeRound = Time_TimeRound
var TimeSecond = Time_TimeSecond
var TimeString = Time_TimeString
var TimeSub = Time_TimeSub
var TimeTruncate = Time_TimeTruncate
var TimeUTC = Time_TimeUTC
var TimeUnix = Time_TimeUnix
var TimeUnixMicro = Time_TimeUnixMicro
var TimeUnixMilli = Time_TimeUnixMilli
var TimeUnixNano = Time_TimeUnixNano
var TimeUnmarshalBinary = Time_TimeUnmarshalBinary
var TimeUnmarshalJSON = Time_TimeUnmarshalJSON
var TimeUnmarshalText = Time_TimeUnmarshalText
var TimeWeekday = Time_TimeWeekday
var TimeYear = Time_TimeYear
var TimeYearDay = Time_TimeYearDay
var TimeZone = Time_TimeZone
var TimeZoneBounds = Time_TimeZoneBounds
var TimerReset = Time_TimerReset
var TimerStop = Time_TimerStop
var WeekdayString = Time_WeekdayString
var Local = Time_Local
var UTC = Time_UTC
var ANSIC = Time_ANSIC
var April = Time_April
var August = Time_August
var DateOnly = Time_DateOnly
var DateTime = Time_DateTime
var December = Time_December
var February = Time_February
var Friday = Time_Friday
var Hour = Time_Hour
var January = Time_January
var July = Time_July
var June = Time_June
var Kitchen = Time_Kitchen
var Layout = Time_Layout
var March = Time_March
var May = Time_May
var Microsecond = Time_Microsecond
var Millisecond = Time_Millisecond
var Minute = Time_Minute
var Monday = Time_Monday
var Nanosecond = Time_Nanosecond
var November = Time_November
var October = Time_October
var RFC1123 = Time_RFC1123
var RFC1123Z = Time_RFC1123Z
var RFC3339 = Time_RFC3339
var RFC3339Nano = Time_RFC3339Nano
var RFC822 = Time_RFC822
var RFC822Z = Time_RFC822Z
var RFC850 = Time_RFC850
var RubyDate = Time_RubyDate
var Saturday = Time_Saturday
var Second = Time_Second
var September = Time_September
var Stamp = Time_Stamp
var StampMicro = Time_StampMicro
var StampMilli = Time_StampMilli
var StampNano = Time_StampNano
var Sunday = Time_Sunday
var Thursday = Time_Thursday
var TimeOnly = Time_TimeOnly
var Tuesday = Time_Tuesday
var UnixDate = Time_UnixDate
var Wednesday = Time_Wednesday
var Time_Duration = map[string]any{"Tag": 0, "SkyName": "Duration"}
var Time_Location = map[string]any{"Tag": 0, "SkyName": "Location"}
var Time_Month = map[string]any{"Tag": 0, "SkyName": "Month"}
var Time_ParseError = map[string]any{"Tag": 0, "SkyName": "ParseError"}
var Time_Ticker = map[string]any{"Tag": 0, "SkyName": "Ticker"}
var Time_Time = map[string]any{"Tag": 0, "SkyName": "Time"}
var Time_Timer = map[string]any{"Tag": 0, "SkyName": "Timer"}
var Time_Weekday = map[string]any{"Tag": 0, "SkyName": "Weekday"}
var Sum224 = Crypto_Sha256_Sum224
var Sum256 = Crypto_Sha256_Sum256
var InvalidByteError = Encoding_Hex_InvalidByteError
var AppendDecode = Encoding_Hex_AppendDecode
var AppendEncode = Encoding_Hex_AppendEncode
var Decode = Encoding_Hex_Decode
var DecodeString = Encoding_Hex_DecodeString
var DecodedLen = Encoding_Hex_DecodedLen
var Dump = Encoding_Hex_Dump
var Encode = Encoding_Hex_Encode
var EncodeToString = Encoding_Hex_EncodeToString
var EncodedLen = Encoding_Hex_EncodedLen
var InvalidByteErrorError = Encoding_Hex_InvalidByteErrorError
var Encoding_Hex_InvalidByteError = map[string]any{"Tag": 0, "SkyName": "InvalidByteError"}
var Client = Net_Http_Client
var ClientConn = Net_Http_ClientConn
var CloseNotifier = Net_Http_CloseNotifier
var ConnState = Net_Http_ConnState
var Cookie = Net_Http_Cookie
var CookieJar = Net_Http_CookieJar
var CrossOriginProtection = Net_Http_CrossOriginProtection
var Dir = Net_Http_Dir
var File = Net_Http_File
var FileSystem = Net_Http_FileSystem
var Flusher = Net_Http_Flusher
var HTTP2Config = Net_Http_HTTP2Config
var Handler = Net_Http_Handler
var HandlerFunc = Net_Http_HandlerFunc
var Header = Net_Http_Header
var Hijacker = Net_Http_Hijacker
var MaxBytesError = Net_Http_MaxBytesError
var ProtocolError = Net_Http_ProtocolError
var Protocols = Net_Http_Protocols
var PushOptions = Net_Http_PushOptions
var Pusher = Net_Http_Pusher
var Request = Net_Http_Request
var Response = Net_Http_Response
var ResponseController = Net_Http_ResponseController
var ResponseWriter = Net_Http_ResponseWriter
var RoundTripper = Net_Http_RoundTripper
var SameSite = Net_Http_SameSite
var ServeMux = Net_Http_ServeMux
var Server = Net_Http_Server
var Transport = Net_Http_Transport
var AllowQuerySemicolons = Net_Http_AllowQuerySemicolons
var CanonicalHeaderKey = Net_Http_CanonicalHeaderKey
var DetectContentType = Net_Http_DetectContentType
var Error_ = Net_Http_Error_
var FileServer = Net_Http_FileServer
var Get = Net_Http_Get
var Handle = Net_Http_Handle
var HandleFunc = Net_Http_HandleFunc
var Head = Net_Http_Head
var ListenAndServe = Net_Http_ListenAndServe
var ListenAndServeTLS = Net_Http_ListenAndServeTLS
var MaxBytesHandler = Net_Http_MaxBytesHandler
var NewCrossOriginProtection = Net_Http_NewCrossOriginProtection
var NewFileTransport = Net_Http_NewFileTransport
var NewResponseController = Net_Http_NewResponseController
var NewServeMux = Net_Http_NewServeMux
var NotFound = Net_Http_NotFound
var NotFoundHandler = Net_Http_NotFoundHandler
var ParseCookie = Net_Http_ParseCookie
var ParseSetCookie = Net_Http_ParseSetCookie
var Redirect = Net_Http_Redirect
var RedirectHandler = Net_Http_RedirectHandler
var ServeFile = Net_Http_ServeFile
var SetCookie = Net_Http_SetCookie
var StatusText = Net_Http_StatusText
var StripPrefix = Net_Http_StripPrefix
var ClientTransport = Net_Http_ClientTransport
var ClientCheckRedirect = Net_Http_ClientCheckRedirect
var ClientJar = Net_Http_ClientJar
var ClientTimeout = Net_Http_ClientTimeout
var CookieName = Net_Http_CookieName
var CookieValue = Net_Http_CookieValue
var CookieQuoted = Net_Http_CookieQuoted
var CookiePath = Net_Http_CookiePath
var CookieDomain = Net_Http_CookieDomain
var CookieExpires = Net_Http_CookieExpires
var CookieRawExpires = Net_Http_CookieRawExpires
var CookieMaxAge = Net_Http_CookieMaxAge
var CookieSecure = Net_Http_CookieSecure
var CookieHttpOnly = Net_Http_CookieHttpOnly
var CookieSameSite = Net_Http_CookieSameSite
var CookiePartitioned = Net_Http_CookiePartitioned
var CookieRaw = Net_Http_CookieRaw
var CookieUnparsed = Net_Http_CookieUnparsed
var HTTP2ConfigMaxConcurrentStreams = Net_Http_HTTP2ConfigMaxConcurrentStreams
var HTTP2ConfigStrictMaxConcurrentRequests = Net_Http_HTTP2ConfigStrictMaxConcurrentRequests
var HTTP2ConfigMaxDecoderHeaderTableSize = Net_Http_HTTP2ConfigMaxDecoderHeaderTableSize
var HTTP2ConfigMaxEncoderHeaderTableSize = Net_Http_HTTP2ConfigMaxEncoderHeaderTableSize
var HTTP2ConfigMaxReadFrameSize = Net_Http_HTTP2ConfigMaxReadFrameSize
var HTTP2ConfigMaxReceiveBufferPerConnection = Net_Http_HTTP2ConfigMaxReceiveBufferPerConnection
var HTTP2ConfigMaxReceiveBufferPerStream = Net_Http_HTTP2ConfigMaxReceiveBufferPerStream
var HTTP2ConfigSendPingTimeout = Net_Http_HTTP2ConfigSendPingTimeout
var HTTP2ConfigPingTimeout = Net_Http_HTTP2ConfigPingTimeout
var HTTP2ConfigWriteByteTimeout = Net_Http_HTTP2ConfigWriteByteTimeout
var HTTP2ConfigPermitProhibitedCipherSuites = Net_Http_HTTP2ConfigPermitProhibitedCipherSuites
var HTTP2ConfigCountError = Net_Http_HTTP2ConfigCountError
var MaxBytesErrorLimit = Net_Http_MaxBytesErrorLimit
var ProtocolErrorErrorString = Net_Http_ProtocolErrorErrorString
var PushOptionsMethod = Net_Http_PushOptionsMethod
var PushOptionsHeader = Net_Http_PushOptionsHeader
var RequestMethod = Net_Http_RequestMethod
var RequestURL = Net_Http_RequestURL
var RequestProto = Net_Http_RequestProto
var RequestProtoMajor = Net_Http_RequestProtoMajor
var RequestProtoMinor = Net_Http_RequestProtoMinor
var RequestHeader = Net_Http_RequestHeader
var RequestBody = Net_Http_RequestBody
var RequestGetBody = Net_Http_RequestGetBody
var RequestContentLength = Net_Http_RequestContentLength
var RequestTransferEncoding = Net_Http_RequestTransferEncoding
var RequestClose = Net_Http_RequestClose
var RequestHost = Net_Http_RequestHost
var RequestForm = Net_Http_RequestForm
var RequestPostForm = Net_Http_RequestPostForm
var RequestMultipartForm = Net_Http_RequestMultipartForm
var RequestTrailer = Net_Http_RequestTrailer
var RequestRemoteAddr = Net_Http_RequestRemoteAddr
var RequestRequestURI = Net_Http_RequestRequestURI
var RequestTLS = Net_Http_RequestTLS
var RequestCancel = Net_Http_RequestCancel
var RequestResponse = Net_Http_RequestResponse
var RequestPattern = Net_Http_RequestPattern
var ResponseStatus = Net_Http_ResponseStatus
var ResponseStatusCode = Net_Http_ResponseStatusCode
var ResponseProto = Net_Http_ResponseProto
var ResponseProtoMajor = Net_Http_ResponseProtoMajor
var ResponseProtoMinor = Net_Http_ResponseProtoMinor
var ResponseHeader = Net_Http_ResponseHeader
var ResponseBody = Net_Http_ResponseBody
var ResponseContentLength = Net_Http_ResponseContentLength
var ResponseTransferEncoding = Net_Http_ResponseTransferEncoding
var ResponseClose = Net_Http_ResponseClose
var ResponseUncompressed = Net_Http_ResponseUncompressed
var ResponseTrailer = Net_Http_ResponseTrailer
var ResponseRequest = Net_Http_ResponseRequest
var ResponseTLS = Net_Http_ResponseTLS
var ServerAddr = Net_Http_ServerAddr
var ServerHandler = Net_Http_ServerHandler
var ServerDisableGeneralOptionsHandler = Net_Http_ServerDisableGeneralOptionsHandler
var ServerTLSConfig = Net_Http_ServerTLSConfig
var ServerReadTimeout = Net_Http_ServerReadTimeout
var ServerReadHeaderTimeout = Net_Http_ServerReadHeaderTimeout
var ServerWriteTimeout = Net_Http_ServerWriteTimeout
var ServerIdleTimeout = Net_Http_ServerIdleTimeout
var ServerMaxHeaderBytes = Net_Http_ServerMaxHeaderBytes
var ServerTLSNextProto = Net_Http_ServerTLSNextProto
var ServerConnState = Net_Http_ServerConnState
var ServerErrorLog = Net_Http_ServerErrorLog
var ServerBaseContext = Net_Http_ServerBaseContext
var ServerConnContext = Net_Http_ServerConnContext
var ServerHTTP2 = Net_Http_ServerHTTP2
var ServerProtocols = Net_Http_ServerProtocols
var TransportProxy = Net_Http_TransportProxy
var TransportOnProxyConnectResponse = Net_Http_TransportOnProxyConnectResponse
var TransportDialContext = Net_Http_TransportDialContext
var TransportDial = Net_Http_TransportDial
var TransportDialTLSContext = Net_Http_TransportDialTLSContext
var TransportDialTLS = Net_Http_TransportDialTLS
var TransportTLSClientConfig = Net_Http_TransportTLSClientConfig
var TransportTLSHandshakeTimeout = Net_Http_TransportTLSHandshakeTimeout
var TransportDisableKeepAlives = Net_Http_TransportDisableKeepAlives
var TransportDisableCompression = Net_Http_TransportDisableCompression
var TransportMaxIdleConns = Net_Http_TransportMaxIdleConns
var TransportMaxIdleConnsPerHost = Net_Http_TransportMaxIdleConnsPerHost
var TransportMaxConnsPerHost = Net_Http_TransportMaxConnsPerHost
var TransportIdleConnTimeout = Net_Http_TransportIdleConnTimeout
var TransportResponseHeaderTimeout = Net_Http_TransportResponseHeaderTimeout
var TransportExpectContinueTimeout = Net_Http_TransportExpectContinueTimeout
var TransportTLSNextProto = Net_Http_TransportTLSNextProto
var TransportProxyConnectHeader = Net_Http_TransportProxyConnectHeader
var TransportGetProxyConnectHeader = Net_Http_TransportGetProxyConnectHeader
var TransportMaxResponseHeaderBytes = Net_Http_TransportMaxResponseHeaderBytes
var TransportWriteBufferSize = Net_Http_TransportWriteBufferSize
var TransportReadBufferSize = Net_Http_TransportReadBufferSize
var TransportForceAttemptHTTP2 = Net_Http_TransportForceAttemptHTTP2
var TransportHTTP2 = Net_Http_TransportHTTP2
var TransportProtocols = Net_Http_TransportProtocols
var ClientCloseIdleConnections = Net_Http_ClientCloseIdleConnections
var ClientDo = Net_Http_ClientDo
var ClientGet = Net_Http_ClientGet
var ClientHead = Net_Http_ClientHead
var ClientConnAvailable = Net_Http_ClientConnAvailable
var ClientConnClose = Net_Http_ClientConnClose
var ClientConnErr = Net_Http_ClientConnErr
var ClientConnInFlight = Net_Http_ClientConnInFlight
var ClientConnRelease = Net_Http_ClientConnRelease
var ClientConnReserve = Net_Http_ClientConnReserve
var ClientConnRoundTrip = Net_Http_ClientConnRoundTrip
var ConnStateString = Net_Http_ConnStateString
var CookieString = Net_Http_CookieString
var CookieValid = Net_Http_CookieValid
var CrossOriginProtectionAddInsecureBypassPattern = Net_Http_CrossOriginProtectionAddInsecureBypassPattern
var CrossOriginProtectionAddTrustedOrigin = Net_Http_CrossOriginProtectionAddTrustedOrigin
var CrossOriginProtectionCheck = Net_Http_CrossOriginProtectionCheck
var CrossOriginProtectionHandler = Net_Http_CrossOriginProtectionHandler
var CrossOriginProtectionSetDenyHandler = Net_Http_CrossOriginProtectionSetDenyHandler
var DirOpen = Net_Http_DirOpen
var FileClose = Net_Http_FileClose
var FileRead = Net_Http_FileRead
var FileReaddir = Net_Http_FileReaddir
var FileSeek = Net_Http_FileSeek
var FileSystemOpen = Net_Http_FileSystemOpen
var FlusherFlush = Net_Http_FlusherFlush
var HandlerServeHTTP = Net_Http_HandlerServeHTTP
var HandlerFuncServeHTTP = Net_Http_HandlerFuncServeHTTP
var HeaderAdd = Net_Http_HeaderAdd
var HeaderClone = Net_Http_HeaderClone
var HeaderDel = Net_Http_HeaderDel
var HeaderGet = Net_Http_HeaderGet
var HeaderSet = Net_Http_HeaderSet
var HeaderValues = Net_Http_HeaderValues
var MaxBytesErrorError = Net_Http_MaxBytesErrorError
var ProtocolErrorError = Net_Http_ProtocolErrorError
var ProtocolErrorIs = Net_Http_ProtocolErrorIs
var ProtocolsHTTP1 = Net_Http_ProtocolsHTTP1
var ProtocolsHTTP2 = Net_Http_ProtocolsHTTP2
var ProtocolsSetHTTP1 = Net_Http_ProtocolsSetHTTP1
var ProtocolsSetHTTP2 = Net_Http_ProtocolsSetHTTP2
var ProtocolsSetUnencryptedHTTP2 = Net_Http_ProtocolsSetUnencryptedHTTP2
var ProtocolsString = Net_Http_ProtocolsString
var ProtocolsUnencryptedHTTP2 = Net_Http_ProtocolsUnencryptedHTTP2
var PusherPush = Net_Http_PusherPush
var RequestAddCookie = Net_Http_RequestAddCookie
var RequestClone = Net_Http_RequestClone
var RequestContext = Net_Http_RequestContext
var RequestCookie = Net_Http_RequestCookie
var RequestCookies = Net_Http_RequestCookies
var RequestCookiesNamed = Net_Http_RequestCookiesNamed
var RequestFormValue = Net_Http_RequestFormValue
var RequestParseForm = Net_Http_RequestParseForm
var RequestParseMultipartForm = Net_Http_RequestParseMultipartForm
var RequestPathValue = Net_Http_RequestPathValue
var RequestPostFormValue = Net_Http_RequestPostFormValue
var RequestProtoAtLeast = Net_Http_RequestProtoAtLeast
var RequestReferer = Net_Http_RequestReferer
var RequestSetBasicAuth = Net_Http_RequestSetBasicAuth
var RequestSetPathValue = Net_Http_RequestSetPathValue
var RequestUserAgent = Net_Http_RequestUserAgent
var RequestWithContext = Net_Http_RequestWithContext
var ResponseCookies = Net_Http_ResponseCookies
var ResponseProtoAtLeast = Net_Http_ResponseProtoAtLeast
var ResponseControllerEnableFullDuplex = Net_Http_ResponseControllerEnableFullDuplex
var ResponseControllerFlush = Net_Http_ResponseControllerFlush
var ResponseWriterHeader = Net_Http_ResponseWriterHeader
var ResponseWriterWrite = Net_Http_ResponseWriterWrite
var ResponseWriterWriteHeader = Net_Http_ResponseWriterWriteHeader
var RoundTripperRoundTrip = Net_Http_RoundTripperRoundTrip
var ServeMuxHandle = Net_Http_ServeMuxHandle
var ServeMuxHandleFunc = Net_Http_ServeMuxHandleFunc
var ServeMuxHandler = Net_Http_ServeMuxHandler
var ServeMuxServeHTTP = Net_Http_ServeMuxServeHTTP
var ServerClose = Net_Http_ServerClose
var ServerListenAndServe = Net_Http_ServerListenAndServe
var ServerListenAndServeTLS = Net_Http_ServerListenAndServeTLS
var ServerSetKeepAlivesEnabled = Net_Http_ServerSetKeepAlivesEnabled
var ServerShutdown = Net_Http_ServerShutdown
var TransportCancelRequest = Net_Http_TransportCancelRequest
var TransportClone = Net_Http_TransportClone
var TransportCloseIdleConnections = Net_Http_TransportCloseIdleConnections
var TransportNewClientConn = Net_Http_TransportNewClientConn
var TransportRegisterProtocol = Net_Http_TransportRegisterProtocol
var TransportRoundTrip = Net_Http_TransportRoundTrip
var DefaultClient = Net_Http_DefaultClient
var DefaultServeMux = Net_Http_DefaultServeMux
var DefaultTransport = Net_Http_DefaultTransport
var ErrHeaderTooLong = Net_Http_ErrHeaderTooLong
var ErrMissingBoundary = Net_Http_ErrMissingBoundary
var ErrMissingContentLength = Net_Http_ErrMissingContentLength
var ErrNotMultipart = Net_Http_ErrNotMultipart
var ErrNotSupported = Net_Http_ErrNotSupported
var ErrShortBody = Net_Http_ErrShortBody
var ErrUnexpectedTrailer = Net_Http_ErrUnexpectedTrailer
var LocalAddrContextKey = Net_Http_LocalAddrContextKey
var NoBody = Net_Http_NoBody
var ServerContextKey = Net_Http_ServerContextKey
var MethodConnect = Net_Http_MethodConnect
var MethodDelete = Net_Http_MethodDelete
var MethodGet = Net_Http_MethodGet
var MethodHead = Net_Http_MethodHead
var MethodOptions = Net_Http_MethodOptions
var MethodPatch = Net_Http_MethodPatch
var MethodPost = Net_Http_MethodPost
var MethodPut = Net_Http_MethodPut
var MethodTrace = Net_Http_MethodTrace
var SameSiteDefaultMode = Net_Http_SameSiteDefaultMode
var SameSiteLaxMode = Net_Http_SameSiteLaxMode
var SameSiteNoneMode = Net_Http_SameSiteNoneMode
var SameSiteStrictMode = Net_Http_SameSiteStrictMode
var StateActive = Net_Http_StateActive
var StateClosed = Net_Http_StateClosed
var StateHijacked = Net_Http_StateHijacked
var StateIdle = Net_Http_StateIdle
var StateNew = Net_Http_StateNew
var TrailerPrefix = Net_Http_TrailerPrefix
var Net_Http_Client = map[string]any{"Tag": 0, "SkyName": "Client"}
var Net_Http_ClientConn = map[string]any{"Tag": 0, "SkyName": "ClientConn"}
var Net_Http_CloseNotifier = map[string]any{"Tag": 0, "SkyName": "CloseNotifier"}
var Net_Http_ConnState = map[string]any{"Tag": 0, "SkyName": "ConnState"}
var Net_Http_Cookie = map[string]any{"Tag": 0, "SkyName": "Cookie"}
var Net_Http_CookieJar = map[string]any{"Tag": 0, "SkyName": "CookieJar"}
var Net_Http_CrossOriginProtection = map[string]any{"Tag": 0, "SkyName": "CrossOriginProtection"}
var Net_Http_Dir = map[string]any{"Tag": 0, "SkyName": "Dir"}
var Net_Http_File = map[string]any{"Tag": 0, "SkyName": "File"}
var Net_Http_FileSystem = map[string]any{"Tag": 0, "SkyName": "FileSystem"}
var Net_Http_Flusher = map[string]any{"Tag": 0, "SkyName": "Flusher"}
var Net_Http_HTTP2Config = map[string]any{"Tag": 0, "SkyName": "HTTP2Config"}
var Net_Http_Handler = map[string]any{"Tag": 0, "SkyName": "Handler"}
var Net_Http_HandlerFunc = map[string]any{"Tag": 0, "SkyName": "HandlerFunc"}
var Net_Http_Header = map[string]any{"Tag": 0, "SkyName": "Header"}
var Net_Http_Hijacker = map[string]any{"Tag": 0, "SkyName": "Hijacker"}
var Net_Http_MaxBytesError = map[string]any{"Tag": 0, "SkyName": "MaxBytesError"}
var Net_Http_ProtocolError = map[string]any{"Tag": 0, "SkyName": "ProtocolError"}
var Net_Http_Protocols = map[string]any{"Tag": 0, "SkyName": "Protocols"}
var Net_Http_PushOptions = map[string]any{"Tag": 0, "SkyName": "PushOptions"}
var Net_Http_Pusher = map[string]any{"Tag": 0, "SkyName": "Pusher"}
var Net_Http_Request = map[string]any{"Tag": 0, "SkyName": "Request"}
var Net_Http_Response = map[string]any{"Tag": 0, "SkyName": "Response"}
var Net_Http_ResponseController = map[string]any{"Tag": 0, "SkyName": "ResponseController"}
var Net_Http_ResponseWriter = map[string]any{"Tag": 0, "SkyName": "ResponseWriter"}
var Net_Http_RoundTripper = map[string]any{"Tag": 0, "SkyName": "RoundTripper"}
var Net_Http_SameSite = map[string]any{"Tag": 0, "SkyName": "SameSite"}
var Net_Http_ServeMux = map[string]any{"Tag": 0, "SkyName": "ServeMux"}
var Net_Http_Server = map[string]any{"Tag": 0, "SkyName": "Server"}
var Net_Http_Transport = map[string]any{"Tag": 0, "SkyName": "Transport"}
var after = Time_After
var date = Time_Date
var fixedZone = Time_FixedZone
var loadLocation = Time_LoadLocation
var loadLocationFromTZData = Time_LoadLocationFromTZData
var newTicker = Time_NewTicker
var newTimer = Time_NewTimer
var now = Time_Now
var parse = Time_Parse
var parseDuration = Time_ParseDuration
var parseInLocation = Time_ParseInLocation
var since = Time_Since
var sleep = Time_Sleep
var tick = Time_Tick
var unix = Time_Unix
var unixMicro = Time_UnixMicro
var unixMilli = Time_UnixMilli
var until = Time_Until
var parseErrorLayout = Time_ParseErrorLayout
var parseErrorValue = Time_ParseErrorValue
var parseErrorLayoutElem = Time_ParseErrorLayoutElem
var parseErrorValueElem = Time_ParseErrorValueElem
var parseErrorMessage = Time_ParseErrorMessage
var tickerC = Time_TickerC
var timerC = Time_TimerC
var durationAbs = Time_DurationAbs
var durationHours = Time_DurationHours
var durationMicroseconds = Time_DurationMicroseconds
var durationMilliseconds = Time_DurationMilliseconds
var durationMinutes = Time_DurationMinutes
var durationNanoseconds = Time_DurationNanoseconds
var durationRound = Time_DurationRound
var durationSeconds = Time_DurationSeconds
var durationString = Time_DurationString
var durationTruncate = Time_DurationTruncate
var locationString = Time_LocationString
var monthString = Time_MonthString
var parseErrorError = Time_ParseErrorError
var tickerReset = Time_TickerReset
var tickerStop = Time_TickerStop
var timeAdd = Time_TimeAdd
var timeAddDate = Time_TimeAddDate
var timeAfter = Time_TimeAfter
var timeAppendBinary = Time_TimeAppendBinary
var timeAppendFormat = Time_TimeAppendFormat
var timeAppendText = Time_TimeAppendText
var timeBefore = Time_TimeBefore
var timeCompare = Time_TimeCompare
var timeDay = Time_TimeDay
var timeEqual = Time_TimeEqual
var timeFormat = Time_TimeFormat
var timeGoString = Time_TimeGoString
var timeGobDecode = Time_TimeGobDecode
var timeGobEncode = Time_TimeGobEncode
var timeHour = Time_TimeHour
var timeISOWeek = Time_TimeISOWeek
var timeIn = Time_TimeIn
var timeIsDST = Time_TimeIsDST
var timeIsZero = Time_TimeIsZero
var timeLocal = Time_TimeLocal
var timeLocation = Time_TimeLocation
var timeMarshalBinary = Time_TimeMarshalBinary
var timeMarshalJSON = Time_TimeMarshalJSON
var timeMarshalText = Time_TimeMarshalText
var timeMinute = Time_TimeMinute
var timeMonth = Time_TimeMonth
var timeNanosecond = Time_TimeNanosecond
var timeRound = Time_TimeRound
var timeSecond = Time_TimeSecond
var timeString = Time_TimeString
var timeSub = Time_TimeSub
var timeTruncate = Time_TimeTruncate
var timeUTC = Time_TimeUTC
var timeUnix = Time_TimeUnix
var timeUnixMicro = Time_TimeUnixMicro
var timeUnixMilli = Time_TimeUnixMilli
var timeUnixNano = Time_TimeUnixNano
var timeUnmarshalBinary = Time_TimeUnmarshalBinary
var timeUnmarshalJSON = Time_TimeUnmarshalJSON
var timeUnmarshalText = Time_TimeUnmarshalText
var timeWeekday = Time_TimeWeekday
var timeYear = Time_TimeYear
var timeYearDay = Time_TimeYearDay
var timeZone = Time_TimeZone
var timeZoneBounds = Time_TimeZoneBounds
var timerReset = Time_TimerReset
var timerStop = Time_TimerStop
var weekdayString = Time_WeekdayString
var local = Time_Local
var uTC = Time_UTC
var aNSIC = Time_ANSIC()
var april = Time_April()
var august = Time_August()
var dateOnly = Time_DateOnly()
var dateTime = Time_DateTime()
var december = Time_December()
var february = Time_February()
var friday = Time_Friday()
var hour = Time_Hour()
var january = Time_January()
var july = Time_July()
var june = Time_June()
var kitchen = Time_Kitchen()
var layout = Time_Layout()
var march = Time_March()
var may = Time_May()
var microsecond = Time_Microsecond()
var millisecond = Time_Millisecond()
var minute = Time_Minute()
var monday = Time_Monday()
var nanosecond = Time_Nanosecond()
var november = Time_November()
var october = Time_October()
var rFC1123 = Time_RFC1123()
var rFC1123Z = Time_RFC1123Z()
var rFC3339 = Time_RFC3339()
var rFC3339Nano = Time_RFC3339Nano()
var rFC822 = Time_RFC822()
var rFC822Z = Time_RFC822Z()
var rFC850 = Time_RFC850()
var rubyDate = Time_RubyDate()
var saturday = Time_Saturday()
var second = Time_Second()
var september = Time_September()
var stamp = Time_Stamp()
var stampMicro = Time_StampMicro()
var stampMilli = Time_StampMilli()
var stampNano = Time_StampNano()
var sunday = Time_Sunday()
var thursday = Time_Thursday()
var timeOnly = Time_TimeOnly()
var tuesday = Time_Tuesday()
var unixDate = Time_UnixDate()
var wednesday = Time_Wednesday()
var sum224 = Crypto_Sha256_Sum224
var sum256 = Crypto_Sha256_Sum256
var appendDecode = Encoding_Hex_AppendDecode
var appendEncode = Encoding_Hex_AppendEncode
var decode = Encoding_Hex_Decode
var decodeString = Encoding_Hex_DecodeString
var decodedLen = Encoding_Hex_DecodedLen
var dump = Encoding_Hex_Dump
var encode = Encoding_Hex_Encode
var encodeToString = Encoding_Hex_EncodeToString
var encodedLen = Encoding_Hex_EncodedLen
var invalidByteErrorError = Encoding_Hex_InvalidByteErrorError
var allowQuerySemicolons = Net_Http_AllowQuerySemicolons
var canonicalHeaderKey = Net_Http_CanonicalHeaderKey
var detectContentType = Net_Http_DetectContentType
var error_ = Net_Http_Error_
var fileServer = Net_Http_FileServer
var get = Net_Http_Get
var handle = Net_Http_Handle
var handleFunc = Net_Http_HandleFunc
var head = Net_Http_Head
var listenAndServe = Net_Http_ListenAndServe
var listenAndServeTLS = Net_Http_ListenAndServeTLS
var maxBytesHandler = Net_Http_MaxBytesHandler
var newCrossOriginProtection = Net_Http_NewCrossOriginProtection
var newFileTransport = Net_Http_NewFileTransport
var newResponseController = Net_Http_NewResponseController
var newServeMux = Net_Http_NewServeMux
var notFound = Net_Http_NotFound
var notFoundHandler = Net_Http_NotFoundHandler
var parseCookie = Net_Http_ParseCookie
var parseSetCookie = Net_Http_ParseSetCookie
var redirect = Net_Http_Redirect
var redirectHandler = Net_Http_RedirectHandler
var serveFile = Net_Http_ServeFile
var setCookie = Net_Http_SetCookie
var statusText = Net_Http_StatusText
var stripPrefix = Net_Http_StripPrefix
var clientTransport = Net_Http_ClientTransport
var clientCheckRedirect = Net_Http_ClientCheckRedirect
var clientJar = Net_Http_ClientJar
var clientTimeout = Net_Http_ClientTimeout
var cookieName = Net_Http_CookieName
var cookieValue = Net_Http_CookieValue
var cookieQuoted = Net_Http_CookieQuoted
var cookiePath = Net_Http_CookiePath
var cookieDomain = Net_Http_CookieDomain
var cookieExpires = Net_Http_CookieExpires
var cookieRawExpires = Net_Http_CookieRawExpires
var cookieMaxAge = Net_Http_CookieMaxAge
var cookieSecure = Net_Http_CookieSecure
var cookieHttpOnly = Net_Http_CookieHttpOnly
var cookieSameSite = Net_Http_CookieSameSite
var cookiePartitioned = Net_Http_CookiePartitioned
var cookieRaw = Net_Http_CookieRaw
var cookieUnparsed = Net_Http_CookieUnparsed
var hTTP2ConfigMaxConcurrentStreams = Net_Http_HTTP2ConfigMaxConcurrentStreams
var hTTP2ConfigStrictMaxConcurrentRequests = Net_Http_HTTP2ConfigStrictMaxConcurrentRequests
var hTTP2ConfigMaxDecoderHeaderTableSize = Net_Http_HTTP2ConfigMaxDecoderHeaderTableSize
var hTTP2ConfigMaxEncoderHeaderTableSize = Net_Http_HTTP2ConfigMaxEncoderHeaderTableSize
var hTTP2ConfigMaxReadFrameSize = Net_Http_HTTP2ConfigMaxReadFrameSize
var hTTP2ConfigMaxReceiveBufferPerConnection = Net_Http_HTTP2ConfigMaxReceiveBufferPerConnection
var hTTP2ConfigMaxReceiveBufferPerStream = Net_Http_HTTP2ConfigMaxReceiveBufferPerStream
var hTTP2ConfigSendPingTimeout = Net_Http_HTTP2ConfigSendPingTimeout
var hTTP2ConfigPingTimeout = Net_Http_HTTP2ConfigPingTimeout
var hTTP2ConfigWriteByteTimeout = Net_Http_HTTP2ConfigWriteByteTimeout
var hTTP2ConfigPermitProhibitedCipherSuites = Net_Http_HTTP2ConfigPermitProhibitedCipherSuites
var hTTP2ConfigCountError = Net_Http_HTTP2ConfigCountError
var maxBytesErrorLimit = Net_Http_MaxBytesErrorLimit
var protocolErrorErrorString = Net_Http_ProtocolErrorErrorString
var pushOptionsMethod = Net_Http_PushOptionsMethod
var pushOptionsHeader = Net_Http_PushOptionsHeader
var requestMethod = Net_Http_RequestMethod
var requestURL = Net_Http_RequestURL
var requestProto = Net_Http_RequestProto
var requestProtoMajor = Net_Http_RequestProtoMajor
var requestProtoMinor = Net_Http_RequestProtoMinor
var requestHeader = Net_Http_RequestHeader
var requestBody = Net_Http_RequestBody
var requestGetBody = Net_Http_RequestGetBody
var requestContentLength = Net_Http_RequestContentLength
var requestTransferEncoding = Net_Http_RequestTransferEncoding
var requestClose = Net_Http_RequestClose
var requestHost = Net_Http_RequestHost
var requestForm = Net_Http_RequestForm
var requestPostForm = Net_Http_RequestPostForm
var requestMultipartForm = Net_Http_RequestMultipartForm
var requestTrailer = Net_Http_RequestTrailer
var requestRemoteAddr = Net_Http_RequestRemoteAddr
var requestRequestURI = Net_Http_RequestRequestURI
var requestTLS = Net_Http_RequestTLS
var requestCancel = Net_Http_RequestCancel
var requestResponse = Net_Http_RequestResponse
var requestPattern = Net_Http_RequestPattern
var responseStatus = Net_Http_ResponseStatus
var responseStatusCode = Net_Http_ResponseStatusCode
var responseProto = Net_Http_ResponseProto
var responseProtoMajor = Net_Http_ResponseProtoMajor
var responseProtoMinor = Net_Http_ResponseProtoMinor
var responseHeader = Net_Http_ResponseHeader
var responseBody = Net_Http_ResponseBody
var responseContentLength = Net_Http_ResponseContentLength
var responseTransferEncoding = Net_Http_ResponseTransferEncoding
var responseClose = Net_Http_ResponseClose
var responseUncompressed = Net_Http_ResponseUncompressed
var responseTrailer = Net_Http_ResponseTrailer
var responseRequest = Net_Http_ResponseRequest
var responseTLS = Net_Http_ResponseTLS
var serverAddr = Net_Http_ServerAddr
var serverHandler = Net_Http_ServerHandler
var serverDisableGeneralOptionsHandler = Net_Http_ServerDisableGeneralOptionsHandler
var serverTLSConfig = Net_Http_ServerTLSConfig
var serverReadTimeout = Net_Http_ServerReadTimeout
var serverReadHeaderTimeout = Net_Http_ServerReadHeaderTimeout
var serverWriteTimeout = Net_Http_ServerWriteTimeout
var serverIdleTimeout = Net_Http_ServerIdleTimeout
var serverMaxHeaderBytes = Net_Http_ServerMaxHeaderBytes
var serverTLSNextProto = Net_Http_ServerTLSNextProto
var serverConnState = Net_Http_ServerConnState
var serverErrorLog = Net_Http_ServerErrorLog
var serverBaseContext = Net_Http_ServerBaseContext
var serverConnContext = Net_Http_ServerConnContext
var serverHTTP2 = Net_Http_ServerHTTP2
var serverProtocols = Net_Http_ServerProtocols
var transportProxy = Net_Http_TransportProxy
var transportOnProxyConnectResponse = Net_Http_TransportOnProxyConnectResponse
var transportDialContext = Net_Http_TransportDialContext
var transportDial = Net_Http_TransportDial
var transportDialTLSContext = Net_Http_TransportDialTLSContext
var transportDialTLS = Net_Http_TransportDialTLS
var transportTLSClientConfig = Net_Http_TransportTLSClientConfig
var transportTLSHandshakeTimeout = Net_Http_TransportTLSHandshakeTimeout
var transportDisableKeepAlives = Net_Http_TransportDisableKeepAlives
var transportDisableCompression = Net_Http_TransportDisableCompression
var transportMaxIdleConns = Net_Http_TransportMaxIdleConns
var transportMaxIdleConnsPerHost = Net_Http_TransportMaxIdleConnsPerHost
var transportMaxConnsPerHost = Net_Http_TransportMaxConnsPerHost
var transportIdleConnTimeout = Net_Http_TransportIdleConnTimeout
var transportResponseHeaderTimeout = Net_Http_TransportResponseHeaderTimeout
var transportExpectContinueTimeout = Net_Http_TransportExpectContinueTimeout
var transportTLSNextProto = Net_Http_TransportTLSNextProto
var transportProxyConnectHeader = Net_Http_TransportProxyConnectHeader
var transportGetProxyConnectHeader = Net_Http_TransportGetProxyConnectHeader
var transportMaxResponseHeaderBytes = Net_Http_TransportMaxResponseHeaderBytes
var transportWriteBufferSize = Net_Http_TransportWriteBufferSize
var transportReadBufferSize = Net_Http_TransportReadBufferSize
var transportForceAttemptHTTP2 = Net_Http_TransportForceAttemptHTTP2
var transportHTTP2 = Net_Http_TransportHTTP2
var transportProtocols = Net_Http_TransportProtocols
var clientCloseIdleConnections = Net_Http_ClientCloseIdleConnections
var clientDo = Net_Http_ClientDo
var clientGet = Net_Http_ClientGet
var clientHead = Net_Http_ClientHead
var clientConnAvailable = Net_Http_ClientConnAvailable
var clientConnClose = Net_Http_ClientConnClose
var clientConnErr = Net_Http_ClientConnErr
var clientConnInFlight = Net_Http_ClientConnInFlight
var clientConnRelease = Net_Http_ClientConnRelease
var clientConnReserve = Net_Http_ClientConnReserve
var clientConnRoundTrip = Net_Http_ClientConnRoundTrip
var connStateString = Net_Http_ConnStateString
var cookieString = Net_Http_CookieString
var cookieValid = Net_Http_CookieValid
var crossOriginProtectionAddInsecureBypassPattern = Net_Http_CrossOriginProtectionAddInsecureBypassPattern
var crossOriginProtectionAddTrustedOrigin = Net_Http_CrossOriginProtectionAddTrustedOrigin
var crossOriginProtectionCheck = Net_Http_CrossOriginProtectionCheck
var crossOriginProtectionHandler = Net_Http_CrossOriginProtectionHandler
var crossOriginProtectionSetDenyHandler = Net_Http_CrossOriginProtectionSetDenyHandler
var dirOpen = Net_Http_DirOpen
var fileClose = Net_Http_FileClose
var fileRead = Net_Http_FileRead
var fileReaddir = Net_Http_FileReaddir
var fileSeek = Net_Http_FileSeek
var fileSystemOpen = Net_Http_FileSystemOpen
var flusherFlush = Net_Http_FlusherFlush
var handlerServeHTTP = Net_Http_HandlerServeHTTP
var handlerFuncServeHTTP = Net_Http_HandlerFuncServeHTTP
var headerAdd = Net_Http_HeaderAdd
var headerClone = Net_Http_HeaderClone
var headerDel = Net_Http_HeaderDel
var headerGet = Net_Http_HeaderGet
var headerSet = Net_Http_HeaderSet
var headerValues = Net_Http_HeaderValues
var maxBytesErrorError = Net_Http_MaxBytesErrorError
var protocolErrorError = Net_Http_ProtocolErrorError
var protocolErrorIs = Net_Http_ProtocolErrorIs
var protocolsHTTP1 = Net_Http_ProtocolsHTTP1
var protocolsHTTP2 = Net_Http_ProtocolsHTTP2
var protocolsSetHTTP1 = Net_Http_ProtocolsSetHTTP1
var protocolsSetHTTP2 = Net_Http_ProtocolsSetHTTP2
var protocolsSetUnencryptedHTTP2 = Net_Http_ProtocolsSetUnencryptedHTTP2
var protocolsString = Net_Http_ProtocolsString
var protocolsUnencryptedHTTP2 = Net_Http_ProtocolsUnencryptedHTTP2
var pusherPush = Net_Http_PusherPush
var requestAddCookie = Net_Http_RequestAddCookie
var requestClone = Net_Http_RequestClone
var requestContext = Net_Http_RequestContext
var requestCookie = Net_Http_RequestCookie
var requestCookies = Net_Http_RequestCookies
var requestCookiesNamed = Net_Http_RequestCookiesNamed
var requestFormValue = Net_Http_RequestFormValue
var requestParseForm = Net_Http_RequestParseForm
var requestParseMultipartForm = Net_Http_RequestParseMultipartForm
var requestPathValue = Net_Http_RequestPathValue
var requestPostFormValue = Net_Http_RequestPostFormValue
var requestProtoAtLeast = Net_Http_RequestProtoAtLeast
var requestReferer = Net_Http_RequestReferer
var requestSetBasicAuth = Net_Http_RequestSetBasicAuth
var requestSetPathValue = Net_Http_RequestSetPathValue
var requestUserAgent = Net_Http_RequestUserAgent
var requestWithContext = Net_Http_RequestWithContext
var responseCookies = Net_Http_ResponseCookies
var responseProtoAtLeast = Net_Http_ResponseProtoAtLeast
var responseControllerEnableFullDuplex = Net_Http_ResponseControllerEnableFullDuplex
var responseControllerFlush = Net_Http_ResponseControllerFlush
var responseWriterHeader = Net_Http_ResponseWriterHeader
var responseWriterWrite = Net_Http_ResponseWriterWrite
var responseWriterWriteHeader = Net_Http_ResponseWriterWriteHeader
var roundTripperRoundTrip = Net_Http_RoundTripperRoundTrip
var serveMuxHandle = Net_Http_ServeMuxHandle
var serveMuxHandleFunc = Net_Http_ServeMuxHandleFunc
var serveMuxHandler = Net_Http_ServeMuxHandler
var serveMuxServeHTTP = Net_Http_ServeMuxServeHTTP
var serverClose = Net_Http_ServerClose
var serverListenAndServe = Net_Http_ServerListenAndServe
var serverListenAndServeTLS = Net_Http_ServerListenAndServeTLS
var serverSetKeepAlivesEnabled = Net_Http_ServerSetKeepAlivesEnabled
var serverShutdown = Net_Http_ServerShutdown
var transportCancelRequest = Net_Http_TransportCancelRequest
var transportClone = Net_Http_TransportClone
var transportCloseIdleConnections = Net_Http_TransportCloseIdleConnections
var transportNewClientConn = Net_Http_TransportNewClientConn
var transportRegisterProtocol = Net_Http_TransportRegisterProtocol
var transportRoundTrip = Net_Http_TransportRoundTrip
var defaultClient = Net_Http_DefaultClient
var defaultServeMux = Net_Http_DefaultServeMux
var defaultTransport = Net_Http_DefaultTransport
var errHeaderTooLong = Net_Http_ErrHeaderTooLong
var errMissingBoundary = Net_Http_ErrMissingBoundary
var errMissingContentLength = Net_Http_ErrMissingContentLength
var errNotMultipart = Net_Http_ErrNotMultipart
var errNotSupported = Net_Http_ErrNotSupported
var errShortBody = Net_Http_ErrShortBody
var errUnexpectedTrailer = Net_Http_ErrUnexpectedTrailer
var localAddrContextKey = Net_Http_LocalAddrContextKey
var noBody = Net_Http_NoBody
var serverContextKey = Net_Http_ServerContextKey
var methodConnect = Net_Http_MethodConnect()
var methodDelete = Net_Http_MethodDelete()
var methodGet = Net_Http_MethodGet()
var methodHead = Net_Http_MethodHead()
var methodOptions = Net_Http_MethodOptions()
var methodPatch = Net_Http_MethodPatch()
var methodPost = Net_Http_MethodPost()
var methodPut = Net_Http_MethodPut()
var methodTrace = Net_Http_MethodTrace()
var sameSiteDefaultMode = Net_Http_SameSiteDefaultMode()
var sameSiteLaxMode = Net_Http_SameSiteLaxMode()
var sameSiteNoneMode = Net_Http_SameSiteNoneMode()
var sameSiteStrictMode = Net_Http_SameSiteStrictMode()
var stateActive = Net_Http_StateActive()
var stateClosed = Net_Http_StateClosed()
var stateHijacked = Net_Http_StateHijacked()
var stateIdle = Net_Http_StateIdle()
var stateNew = Net_Http_StateNew()
var trailerPrefix = Net_Http_TrailerPrefix()

func SkyOk(v any) SkyResult { return SkyResult{Tag: 0, SkyName: "Ok", OkValue: v} }

func SkyErr(v any) SkyResult { return SkyResult{Tag: 1, SkyName: "Err", ErrValue: v} }

func sky_asInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	default:
		return 0
	}
}

func sky_asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func sky_asBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func sky_asBytes(v any) []byte {
	if b, ok := v.([]byte); ok {
		return b
	}
	if s, ok := v.(string); ok {
		return []byte(s)
	}
	return nil
}

func sky_asError(v any) error {
	if e, ok := v.(error); ok {
		return e
	}
	return fmt.Errorf("%v", v)
}

func sky_stringToBytes(s any) any { return []byte(sky_asString(s)) }

func sky_asContext(v any) context.Context {
	if c, ok := v.(context.Context); ok {
		return c
	}
	return context.Background()
}

func sky_asInt64(v any) int64 { return int64(sky_asInt(v)) }

func sky_asHttpHandler(v any) func(net_http.ResponseWriter, *net_http.Request) {
	if fn2, ok := v.(func(any, any) any); ok {
		return func(w net_http.ResponseWriter, r *net_http.Request) { fn2(w, r) }
	}
	fn := v.(func(any) any)
	return func(w net_http.ResponseWriter, r *net_http.Request) { fn(w).(func(any) any)(r) }
}

func sky_println(args ...any) any {
	ss := make([]any, len(args))
	for i, a := range args {
		ss[i] = sky_asString(a)
	}
	fmt.Println(ss...)
	return struct{}{}
}

func sky_asSkyResult(v any) SkyResult {
	if r, ok := v.(SkyResult); ok {
		return r
	}
	return SkyResult{}
}

func sky_js(v any) any { return v }

func sky_call(f any, arg any) any {
	if fn, ok := f.(func(any) any); ok {
		return fn(arg)
	}
	if s, ok := f.(string); ok {
		if args, ok := arg.([]any); ok {
			parts := make([]string, len(args))
			for i, a := range args {
				parts[i] = sky_asString(a)
			}
			return s + "(" + strings.Join(parts, ", ") + ")"
		}
		return s + " " + sky_asString(arg)
	}
	panic(fmt.Sprintf("sky_call: cannot call %T", f))
}

func sky_taskSucceed(value any) any { return func() any { return SkyOk(value) } }

func sky_taskAndThen(fn any) any {
	return func(task any) any {
		return func() any {
			r := sky_runTask(task)
			if sky_asSkyResult(r).Tag == 0 {
				next := fn.(func(any) any)(sky_asSkyResult(r).OkValue)
				return sky_runTask(next)
			}
			return r
		}
	}
}

func sky_taskPerform(task any) any {
	r := sky_runTask(task)
	if sr, ok := r.(SkyResult); ok {
		if sr.Tag == 0 {
			return sr.OkValue
		}
		return r
	}
	return r
}

func sky_runTask(task any) any {
	if t, ok := task.(func() any); ok {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "Task panic: %v\n", r)
				panic(r)
			}
		}()
		return t()
	}
	if r, ok := task.(SkyResult); ok {
		return r
	}
	return SkyOk(task)
}

func sky_runMainTask(result any) {
	if _, ok := result.(func() any); ok {
		r := sky_runTask(result)
		if sky_asSkyResult(r).Tag == 1 {
			fmt.Fprintln(os.Stderr, sky_asSkyResult(r).ErrValue)
			os.Exit(1)
		}
	}
}

func sky_timeNow(u any) any { return time.Now().UnixMilli() }

func sky_liveApp(config any) any { return sky_liveAppImpl(config) }

func Time_After(arg0 any) any {
	return Sky_time_After(arg0)
}

func Time_Date(arg0 any, arg1 any, arg2 any, arg3 any, arg4 any, arg5 any, arg6 any, arg7 any) any {
	return Sky_time_Date(arg0, arg1, arg2, arg3, arg4, arg5, arg6, arg7)
}

func Time_FixedZone(arg0 any, arg1 any) any {
	return Sky_time_FixedZone(arg0, arg1)
}

func Time_LoadLocation(arg0 any) any {
	return Sky_time_LoadLocation(arg0)
}

func Time_LoadLocationFromTZData(arg0 any, arg1 any) any {
	return Sky_time_LoadLocationFromTZData(arg0, arg1)
}

func Time_NewTicker(arg0 any) any {
	return Sky_time_NewTicker(arg0)
}

func Time_NewTimer(arg0 any) any {
	return Sky_time_NewTimer(arg0)
}

func Time_Now(_ any) any {
	return Sky_time_Now(struct{}{})
}

func Time_Parse(arg0 any, arg1 any) any {
	return Sky_time_Parse(arg0, arg1)
}

func Time_ParseDuration(arg0 any) any {
	return Sky_time_ParseDuration(arg0)
}

func Time_ParseInLocation(arg0 any, arg1 any, arg2 any) any {
	return Sky_time_ParseInLocation(arg0, arg1, arg2)
}

func Time_Since(arg0 any) any {
	return Sky_time_Since(arg0)
}

func Time_Sleep(arg0 any) any {
	return Sky_time_Sleep(arg0)
}

func Time_Tick(arg0 any) any {
	return Sky_time_Tick(arg0)
}

func Time_Unix(arg0 any, arg1 any) any {
	return Sky_time_Unix(arg0, arg1)
}

func Time_UnixMicro(arg0 any) any {
	return Sky_time_UnixMicro(arg0)
}

func Time_UnixMilli(arg0 any) any {
	return Sky_time_UnixMilli(arg0)
}

func Time_Until(arg0 any) any {
	return Sky_time_Until(arg0)
}

func Time_ParseErrorLayout(receiver any) any {
	return Sky_time_FIELD_ParseError_Layout(receiver)
}

func Time_ParseErrorValue(receiver any) any {
	return Sky_time_FIELD_ParseError_Value(receiver)
}

func Time_ParseErrorLayoutElem(receiver any) any {
	return Sky_time_FIELD_ParseError_LayoutElem(receiver)
}

func Time_ParseErrorValueElem(receiver any) any {
	return Sky_time_FIELD_ParseError_ValueElem(receiver)
}

func Time_ParseErrorMessage(receiver any) any {
	return Sky_time_FIELD_ParseError_Message(receiver)
}

func Time_TickerC(receiver any) any {
	return Sky_time_FIELD_Ticker_C(receiver)
}

func Time_TimerC(receiver any) any {
	return Sky_time_FIELD_Timer_C(receiver)
}

func Time_DurationAbs(receiver any) any {
	return Sky_time_DurationAbs(receiver)
}

func Time_DurationHours(receiver any) any {
	return Sky_time_DurationHours(receiver)
}

func Time_DurationMicroseconds(receiver any) any {
	return Sky_time_DurationMicroseconds(receiver)
}

func Time_DurationMilliseconds(receiver any) any {
	return Sky_time_DurationMilliseconds(receiver)
}

func Time_DurationMinutes(receiver any) any {
	return Sky_time_DurationMinutes(receiver)
}

func Time_DurationNanoseconds(receiver any) any {
	return Sky_time_DurationNanoseconds(receiver)
}

func Time_DurationRound(receiver any, arg0 any) any {
	return Sky_time_DurationRound(receiver, arg0)
}

func Time_DurationSeconds(receiver any) any {
	return Sky_time_DurationSeconds(receiver)
}

func Time_DurationString(receiver any) any {
	return Sky_time_DurationString(receiver)
}

func Time_DurationTruncate(receiver any, arg0 any) any {
	return Sky_time_DurationTruncate(receiver, arg0)
}

func Time_LocationString(receiver any) any {
	return Sky_time_LocationString(receiver)
}

func Time_MonthString(receiver any) any {
	return Sky_time_MonthString(receiver)
}

func Time_ParseErrorError(receiver any) any {
	return Sky_time_ParseErrorError(receiver)
}

func Time_TickerReset(receiver any, arg0 any) any {
	return Sky_time_TickerReset(receiver, arg0)
}

func Time_TickerStop(receiver any) any {
	return Sky_time_TickerStop(receiver)
}

func Time_TimeAdd(receiver any, arg0 any) any {
	return Sky_time_TimeAdd(receiver, arg0)
}

func Time_TimeAddDate(receiver any, arg0 any, arg1 any, arg2 any) any {
	return Sky_time_TimeAddDate(receiver, arg0, arg1, arg2)
}

func Time_TimeAfter(receiver any, arg0 any) any {
	return Sky_time_TimeAfter(receiver, arg0)
}

func Time_TimeAppendBinary(receiver any, arg0 any) any {
	return Sky_time_TimeAppendBinary(receiver, arg0)
}

func Time_TimeAppendFormat(receiver any, arg0 any, arg1 any) any {
	return Sky_time_TimeAppendFormat(receiver, arg0, arg1)
}

func Time_TimeAppendText(receiver any, arg0 any) any {
	return Sky_time_TimeAppendText(receiver, arg0)
}

func Time_TimeBefore(receiver any, arg0 any) any {
	return Sky_time_TimeBefore(receiver, arg0)
}

func Time_TimeCompare(receiver any, arg0 any) any {
	return Sky_time_TimeCompare(receiver, arg0)
}

func Time_TimeDay(receiver any) any {
	return Sky_time_TimeDay(receiver)
}

func Time_TimeEqual(receiver any, arg0 any) any {
	return Sky_time_TimeEqual(receiver, arg0)
}

func Time_TimeFormat(receiver any, arg0 any) any {
	return Sky_time_TimeFormat(receiver, arg0)
}

func Time_TimeGoString(receiver any) any {
	return Sky_time_TimeGoString(receiver)
}

func Time_TimeGobDecode(receiver any, arg0 any) any {
	return Sky_time_TimeGobDecode(receiver, arg0)
}

func Time_TimeGobEncode(receiver any) any {
	return Sky_time_TimeGobEncode(receiver)
}

func Time_TimeHour(receiver any) any {
	return Sky_time_TimeHour(receiver)
}

func Time_TimeISOWeek(receiver any) any {
	return Sky_time_TimeISOWeek(receiver)
}

func Time_TimeIn(receiver any, arg0 any) any {
	return Sky_time_TimeIn(receiver, arg0)
}

func Time_TimeIsDST(receiver any) any {
	return Sky_time_TimeIsDST(receiver)
}

func Time_TimeIsZero(receiver any) any {
	return Sky_time_TimeIsZero(receiver)
}

func Time_TimeLocal(receiver any) any {
	return Sky_time_TimeLocal(receiver)
}

func Time_TimeLocation(receiver any) any {
	return Sky_time_TimeLocation(receiver)
}

func Time_TimeMarshalBinary(receiver any) any {
	return Sky_time_TimeMarshalBinary(receiver)
}

func Time_TimeMarshalJSON(receiver any) any {
	return Sky_time_TimeMarshalJSON(receiver)
}

func Time_TimeMarshalText(receiver any) any {
	return Sky_time_TimeMarshalText(receiver)
}

func Time_TimeMinute(receiver any) any {
	return Sky_time_TimeMinute(receiver)
}

func Time_TimeMonth(receiver any) any {
	return Sky_time_TimeMonth(receiver)
}

func Time_TimeNanosecond(receiver any) any {
	return Sky_time_TimeNanosecond(receiver)
}

func Time_TimeRound(receiver any, arg0 any) any {
	return Sky_time_TimeRound(receiver, arg0)
}

func Time_TimeSecond(receiver any) any {
	return Sky_time_TimeSecond(receiver)
}

func Time_TimeString(receiver any) any {
	return Sky_time_TimeString(receiver)
}

func Time_TimeSub(receiver any, arg0 any) any {
	return Sky_time_TimeSub(receiver, arg0)
}

func Time_TimeTruncate(receiver any, arg0 any) any {
	return Sky_time_TimeTruncate(receiver, arg0)
}

func Time_TimeUTC(receiver any) any {
	return Sky_time_TimeUTC(receiver)
}

func Time_TimeUnix(receiver any) any {
	return Sky_time_TimeUnix(receiver)
}

func Time_TimeUnixMicro(receiver any) any {
	return Sky_time_TimeUnixMicro(receiver)
}

func Time_TimeUnixMilli(receiver any) any {
	return Sky_time_TimeUnixMilli(receiver)
}

func Time_TimeUnixNano(receiver any) any {
	return Sky_time_TimeUnixNano(receiver)
}

func Time_TimeUnmarshalBinary(receiver any, arg0 any) any {
	return Sky_time_TimeUnmarshalBinary(receiver, arg0)
}

func Time_TimeUnmarshalJSON(receiver any, arg0 any) any {
	return Sky_time_TimeUnmarshalJSON(receiver, arg0)
}

func Time_TimeUnmarshalText(receiver any, arg0 any) any {
	return Sky_time_TimeUnmarshalText(receiver, arg0)
}

func Time_TimeWeekday(receiver any) any {
	return Sky_time_TimeWeekday(receiver)
}

func Time_TimeYear(receiver any) any {
	return Sky_time_TimeYear(receiver)
}

func Time_TimeYearDay(receiver any) any {
	return Sky_time_TimeYearDay(receiver)
}

func Time_TimeZone(receiver any) any {
	return Sky_time_TimeZone(receiver)
}

func Time_TimeZoneBounds(receiver any) any {
	return Sky_time_TimeZoneBounds(receiver)
}

func Time_TimerReset(receiver any, arg0 any) any {
	return Sky_time_TimerReset(receiver, arg0)
}

func Time_TimerStop(receiver any) any {
	return Sky_time_TimerStop(receiver)
}

func Time_WeekdayString(receiver any) any {
	return Sky_time_WeekdayString(receiver)
}

func Time_Local(_ any) any {
	return Sky_time_Local
}

func Time_UTC(_ any) any {
	return Sky_time_UTC
}

func Time_ANSIC() any {
	return "Mon Jan _2 15:04:05 2006"
}

func Time_April() any {
	return Sky_time_April
}

func Time_August() any {
	return Sky_time_August
}

func Time_DateOnly() any {
	return "2006-01-02"
}

func Time_DateTime() any {
	return "2006-01-02 15:04:05"
}

func Time_December() any {
	return Sky_time_December
}

func Time_February() any {
	return Sky_time_February
}

func Time_Friday() any {
	return Sky_time_Friday
}

func Time_Hour() any {
	return Sky_time_Hour
}

func Time_January() any {
	return Sky_time_January
}

func Time_July() any {
	return Sky_time_July
}

func Time_June() any {
	return Sky_time_June
}

func Time_Kitchen() any {
	return "3:04PM"
}

func Time_Layout() any {
	return "01/02 03:04:05PM '06 -0700"
}

func Time_March() any {
	return Sky_time_March
}

func Time_May() any {
	return Sky_time_May
}

func Time_Microsecond() any {
	return Sky_time_Microsecond
}

func Time_Millisecond() any {
	return Sky_time_Millisecond
}

func Time_Minute() any {
	return Sky_time_Minute
}

func Time_Monday() any {
	return Sky_time_Monday
}

func Time_Nanosecond() any {
	return Sky_time_Nanosecond
}

func Time_November() any {
	return Sky_time_November
}

func Time_October() any {
	return Sky_time_October
}

func Time_RFC1123() any {
	return "Mon, 02 Jan 2006 15:04:05 MST"
}

func Time_RFC1123Z() any {
	return "Mon, 02 Jan 2006 15:04:05 -0700"
}

func Time_RFC3339() any {
	return "2006-01-02T15:04:05Z07:00"
}

func Time_RFC3339Nano() any {
	return "2006-01-02T15:04:05.999999999Z07:00"
}

func Time_RFC822() any {
	return "02 Jan 06 15:04 MST"
}

func Time_RFC822Z() any {
	return "02 Jan 06 15:04 -0700"
}

func Time_RFC850() any {
	return "Monday, 02-Jan-06 15:04:05 MST"
}

func Time_RubyDate() any {
	return "Mon Jan 02 15:04:05 -0700 2006"
}

func Time_Saturday() any {
	return Sky_time_Saturday
}

func Time_Second() any {
	return Sky_time_Second
}

func Time_September() any {
	return Sky_time_September
}

func Time_Stamp() any {
	return "Jan _2 15:04:05"
}

func Time_StampMicro() any {
	return "Jan _2 15:04:05.000000"
}

func Time_StampMilli() any {
	return "Jan _2 15:04:05.000"
}

func Time_StampNano() any {
	return "Jan _2 15:04:05.000000000"
}

func Time_Sunday() any {
	return Sky_time_Sunday
}

func Time_Thursday() any {
	return Sky_time_Thursday
}

func Time_TimeOnly() any {
	return "15:04:05"
}

func Time_Tuesday() any {
	return Sky_time_Tuesday
}

func Time_UnixDate() any {
	return "Mon Jan _2 15:04:05 MST 2006"
}

func Time_Wednesday() any {
	return Sky_time_Wednesday
}

func Crypto_Sha256_Sum224(arg0 any) any {
	return Sky_crypto_sha256_Sum224(arg0)
}

func Crypto_Sha256_Sum256(arg0 any) any {
	return Sky_crypto_sha256_Sum256(arg0)
}

func Encoding_Hex_AppendDecode(arg0 any, arg1 any) any {
	return Sky_encoding_hex_AppendDecode(arg0, arg1)
}

func Encoding_Hex_AppendEncode(arg0 any, arg1 any) any {
	return Sky_encoding_hex_AppendEncode(arg0, arg1)
}

func Encoding_Hex_Decode(arg0 any, arg1 any) any {
	return Sky_encoding_hex_Decode(arg0, arg1)
}

func Encoding_Hex_DecodeString(arg0 any) any {
	return Sky_encoding_hex_DecodeString(arg0)
}

func Encoding_Hex_DecodedLen(arg0 any) any {
	return Sky_encoding_hex_DecodedLen(arg0)
}

func Encoding_Hex_Dump(arg0 any) any {
	return Sky_encoding_hex_Dump(arg0)
}

func Encoding_Hex_Encode(arg0 any, arg1 any) any {
	return Sky_encoding_hex_Encode(arg0, arg1)
}

func Encoding_Hex_EncodeToString(arg0 any) any {
	return Sky_encoding_hex_EncodeToString(arg0)
}

func Encoding_Hex_EncodedLen(arg0 any) any {
	return Sky_encoding_hex_EncodedLen(arg0)
}

func Encoding_Hex_InvalidByteErrorError(receiver any) any {
	return Sky_encoding_hex_InvalidByteErrorError(receiver)
}

func Net_Http_AllowQuerySemicolons(arg0 any) any {
	return Sky_net_http_AllowQuerySemicolons(arg0)
}

func Net_Http_CanonicalHeaderKey(arg0 any) any {
	return Sky_net_http_CanonicalHeaderKey(arg0)
}

func Net_Http_DetectContentType(arg0 any) any {
	return Sky_net_http_DetectContentType(arg0)
}

func Net_Http_Error_(arg0 any, arg1 any, arg2 any) any {
	return Sky_net_http_Error(arg0, arg1, arg2)
}

func Net_Http_FileServer(arg0 any) any {
	return Sky_net_http_FileServer(arg0)
}

func Net_Http_Get(arg0 any) any {
	return Sky_net_http_Get(arg0)
}

func Net_Http_Handle(arg0 any, arg1 any) any {
	return Sky_net_http_Handle(arg0, arg1)
}

func Net_Http_HandleFunc(arg0 any, arg1 any) any {
	return Sky_net_http_HandleFunc(arg0, arg1)
}

func Net_Http_Head(arg0 any) any {
	return Sky_net_http_Head(arg0)
}

func Net_Http_ListenAndServe(arg0 any, arg1 any) any {
	return Sky_net_http_ListenAndServe(arg0, arg1)
}

func Net_Http_ListenAndServeTLS(arg0 any, arg1 any, arg2 any, arg3 any) any {
	return Sky_net_http_ListenAndServeTLS(arg0, arg1, arg2, arg3)
}

func Net_Http_MaxBytesHandler(arg0 any, arg1 any) any {
	return Sky_net_http_MaxBytesHandler(arg0, arg1)
}

func Net_Http_NewCrossOriginProtection(_ any) any {
	return Sky_net_http_NewCrossOriginProtection(struct{}{})
}

func Net_Http_NewFileTransport(arg0 any) any {
	return Sky_net_http_NewFileTransport(arg0)
}

func Net_Http_NewResponseController(arg0 any) any {
	return Sky_net_http_NewResponseController(arg0)
}

func Net_Http_NewServeMux(_ any) any {
	return Sky_net_http_NewServeMux(struct{}{})
}

func Net_Http_NotFound(arg0 any, arg1 any) any {
	return Sky_net_http_NotFound(arg0, arg1)
}

func Net_Http_NotFoundHandler(_ any) any {
	return Sky_net_http_NotFoundHandler(struct{}{})
}

func Net_Http_ParseCookie(arg0 any) any {
	return Sky_net_http_ParseCookie(arg0)
}

func Net_Http_ParseSetCookie(arg0 any) any {
	return Sky_net_http_ParseSetCookie(arg0)
}

func Net_Http_Redirect(arg0 any, arg1 any, arg2 any, arg3 any) any {
	return Sky_net_http_Redirect(arg0, arg1, arg2, arg3)
}

func Net_Http_RedirectHandler(arg0 any, arg1 any) any {
	return Sky_net_http_RedirectHandler(arg0, arg1)
}

func Net_Http_ServeFile(arg0 any, arg1 any, arg2 any) any {
	return Sky_net_http_ServeFile(arg0, arg1, arg2)
}

func Net_Http_SetCookie(arg0 any, arg1 any) any {
	return Sky_net_http_SetCookie(arg0, arg1)
}

func Net_Http_StatusText(arg0 any) any {
	return Sky_net_http_StatusText(arg0)
}

func Net_Http_StripPrefix(arg0 any, arg1 any) any {
	return Sky_net_http_StripPrefix(arg0, arg1)
}

func Net_Http_ClientTransport(receiver any) any {
	return Sky_net_http_FIELD_Client_Transport(receiver)
}

func Net_Http_ClientCheckRedirect(receiver any) any {
	return Sky_net_http_FIELD_Client_CheckRedirect(receiver)
}

func Net_Http_ClientJar(receiver any) any {
	return Sky_net_http_FIELD_Client_Jar(receiver)
}

func Net_Http_ClientTimeout(receiver any) any {
	return Sky_net_http_FIELD_Client_Timeout(receiver)
}

func Net_Http_CookieName(receiver any) any {
	return Sky_net_http_FIELD_Cookie_Name(receiver)
}

func Net_Http_CookieValue(receiver any) any {
	return Sky_net_http_FIELD_Cookie_Value(receiver)
}

func Net_Http_CookieQuoted(receiver any) any {
	return Sky_net_http_FIELD_Cookie_Quoted(receiver)
}

func Net_Http_CookiePath(receiver any) any {
	return Sky_net_http_FIELD_Cookie_Path(receiver)
}

func Net_Http_CookieDomain(receiver any) any {
	return Sky_net_http_FIELD_Cookie_Domain(receiver)
}

func Net_Http_CookieExpires(receiver any) any {
	return Sky_net_http_FIELD_Cookie_Expires(receiver)
}

func Net_Http_CookieRawExpires(receiver any) any {
	return Sky_net_http_FIELD_Cookie_RawExpires(receiver)
}

func Net_Http_CookieMaxAge(receiver any) any {
	return Sky_net_http_FIELD_Cookie_MaxAge(receiver)
}

func Net_Http_CookieSecure(receiver any) any {
	return Sky_net_http_FIELD_Cookie_Secure(receiver)
}

func Net_Http_CookieHttpOnly(receiver any) any {
	return Sky_net_http_FIELD_Cookie_HttpOnly(receiver)
}

func Net_Http_CookieSameSite(receiver any) any {
	return Sky_net_http_FIELD_Cookie_SameSite(receiver)
}

func Net_Http_CookiePartitioned(receiver any) any {
	return Sky_net_http_FIELD_Cookie_Partitioned(receiver)
}

func Net_Http_CookieRaw(receiver any) any {
	return Sky_net_http_FIELD_Cookie_Raw(receiver)
}

func Net_Http_CookieUnparsed(receiver any) any {
	return Sky_net_http_FIELD_Cookie_Unparsed(receiver)
}

func Net_Http_HTTP2ConfigMaxConcurrentStreams(receiver any) any {
	return Sky_net_http_FIELD_HTTP2Config_MaxConcurrentStreams(receiver)
}

func Net_Http_HTTP2ConfigStrictMaxConcurrentRequests(receiver any) any {
	return Sky_net_http_FIELD_HTTP2Config_StrictMaxConcurrentRequests(receiver)
}

func Net_Http_HTTP2ConfigMaxDecoderHeaderTableSize(receiver any) any {
	return Sky_net_http_FIELD_HTTP2Config_MaxDecoderHeaderTableSize(receiver)
}

func Net_Http_HTTP2ConfigMaxEncoderHeaderTableSize(receiver any) any {
	return Sky_net_http_FIELD_HTTP2Config_MaxEncoderHeaderTableSize(receiver)
}

func Net_Http_HTTP2ConfigMaxReadFrameSize(receiver any) any {
	return Sky_net_http_FIELD_HTTP2Config_MaxReadFrameSize(receiver)
}

func Net_Http_HTTP2ConfigMaxReceiveBufferPerConnection(receiver any) any {
	return Sky_net_http_FIELD_HTTP2Config_MaxReceiveBufferPerConnection(receiver)
}

func Net_Http_HTTP2ConfigMaxReceiveBufferPerStream(receiver any) any {
	return Sky_net_http_FIELD_HTTP2Config_MaxReceiveBufferPerStream(receiver)
}

func Net_Http_HTTP2ConfigSendPingTimeout(receiver any) any {
	return Sky_net_http_FIELD_HTTP2Config_SendPingTimeout(receiver)
}

func Net_Http_HTTP2ConfigPingTimeout(receiver any) any {
	return Sky_net_http_FIELD_HTTP2Config_PingTimeout(receiver)
}

func Net_Http_HTTP2ConfigWriteByteTimeout(receiver any) any {
	return Sky_net_http_FIELD_HTTP2Config_WriteByteTimeout(receiver)
}

func Net_Http_HTTP2ConfigPermitProhibitedCipherSuites(receiver any) any {
	return Sky_net_http_FIELD_HTTP2Config_PermitProhibitedCipherSuites(receiver)
}

func Net_Http_HTTP2ConfigCountError(receiver any) any {
	return Sky_net_http_FIELD_HTTP2Config_CountError(receiver)
}

func Net_Http_MaxBytesErrorLimit(receiver any) any {
	return Sky_net_http_FIELD_MaxBytesError_Limit(receiver)
}

func Net_Http_ProtocolErrorErrorString(receiver any) any {
	return Sky_net_http_FIELD_ProtocolError_ErrorString(receiver)
}

func Net_Http_PushOptionsMethod(receiver any) any {
	return Sky_net_http_FIELD_PushOptions_Method(receiver)
}

func Net_Http_PushOptionsHeader(receiver any) any {
	return Sky_net_http_FIELD_PushOptions_Header(receiver)
}

func Net_Http_RequestMethod(receiver any) any {
	return Sky_net_http_FIELD_Request_Method(receiver)
}

func Net_Http_RequestURL(receiver any) any {
	return Sky_net_http_FIELD_Request_URL(receiver)
}

func Net_Http_RequestProto(receiver any) any {
	return Sky_net_http_FIELD_Request_Proto(receiver)
}

func Net_Http_RequestProtoMajor(receiver any) any {
	return Sky_net_http_FIELD_Request_ProtoMajor(receiver)
}

func Net_Http_RequestProtoMinor(receiver any) any {
	return Sky_net_http_FIELD_Request_ProtoMinor(receiver)
}

func Net_Http_RequestHeader(receiver any) any {
	return Sky_net_http_FIELD_Request_Header(receiver)
}

func Net_Http_RequestBody(receiver any) any {
	return Sky_net_http_FIELD_Request_Body(receiver)
}

func Net_Http_RequestGetBody(receiver any) any {
	return Sky_net_http_FIELD_Request_GetBody(receiver)
}

func Net_Http_RequestContentLength(receiver any) any {
	return Sky_net_http_FIELD_Request_ContentLength(receiver)
}

func Net_Http_RequestTransferEncoding(receiver any) any {
	return Sky_net_http_FIELD_Request_TransferEncoding(receiver)
}

func Net_Http_RequestClose(receiver any) any {
	return Sky_net_http_FIELD_Request_Close(receiver)
}

func Net_Http_RequestHost(receiver any) any {
	return Sky_net_http_FIELD_Request_Host(receiver)
}

func Net_Http_RequestForm(receiver any) any {
	return Sky_net_http_FIELD_Request_Form(receiver)
}

func Net_Http_RequestPostForm(receiver any) any {
	return Sky_net_http_FIELD_Request_PostForm(receiver)
}

func Net_Http_RequestMultipartForm(receiver any) any {
	return Sky_net_http_FIELD_Request_MultipartForm(receiver)
}

func Net_Http_RequestTrailer(receiver any) any {
	return Sky_net_http_FIELD_Request_Trailer(receiver)
}

func Net_Http_RequestRemoteAddr(receiver any) any {
	return Sky_net_http_FIELD_Request_RemoteAddr(receiver)
}

func Net_Http_RequestRequestURI(receiver any) any {
	return Sky_net_http_FIELD_Request_RequestURI(receiver)
}

func Net_Http_RequestTLS(receiver any) any {
	return Sky_net_http_FIELD_Request_TLS(receiver)
}

func Net_Http_RequestCancel(receiver any) any {
	return Sky_net_http_FIELD_Request_Cancel(receiver)
}

func Net_Http_RequestResponse(receiver any) any {
	return Sky_net_http_FIELD_Request_Response(receiver)
}

func Net_Http_RequestPattern(receiver any) any {
	return Sky_net_http_FIELD_Request_Pattern(receiver)
}

func Net_Http_ResponseStatus(receiver any) any {
	return Sky_net_http_FIELD_Response_Status(receiver)
}

func Net_Http_ResponseStatusCode(receiver any) any {
	return Sky_net_http_FIELD_Response_StatusCode(receiver)
}

func Net_Http_ResponseProto(receiver any) any {
	return Sky_net_http_FIELD_Response_Proto(receiver)
}

func Net_Http_ResponseProtoMajor(receiver any) any {
	return Sky_net_http_FIELD_Response_ProtoMajor(receiver)
}

func Net_Http_ResponseProtoMinor(receiver any) any {
	return Sky_net_http_FIELD_Response_ProtoMinor(receiver)
}

func Net_Http_ResponseHeader(receiver any) any {
	return Sky_net_http_FIELD_Response_Header(receiver)
}

func Net_Http_ResponseBody(receiver any) any {
	return Sky_net_http_FIELD_Response_Body(receiver)
}

func Net_Http_ResponseContentLength(receiver any) any {
	return Sky_net_http_FIELD_Response_ContentLength(receiver)
}

func Net_Http_ResponseTransferEncoding(receiver any) any {
	return Sky_net_http_FIELD_Response_TransferEncoding(receiver)
}

func Net_Http_ResponseClose(receiver any) any {
	return Sky_net_http_FIELD_Response_Close(receiver)
}

func Net_Http_ResponseUncompressed(receiver any) any {
	return Sky_net_http_FIELD_Response_Uncompressed(receiver)
}

func Net_Http_ResponseTrailer(receiver any) any {
	return Sky_net_http_FIELD_Response_Trailer(receiver)
}

func Net_Http_ResponseRequest(receiver any) any {
	return Sky_net_http_FIELD_Response_Request(receiver)
}

func Net_Http_ResponseTLS(receiver any) any {
	return Sky_net_http_FIELD_Response_TLS(receiver)
}

func Net_Http_ServerAddr(receiver any) any {
	return Sky_net_http_FIELD_Server_Addr(receiver)
}

func Net_Http_ServerHandler(receiver any) any {
	return Sky_net_http_FIELD_Server_Handler(receiver)
}

func Net_Http_ServerDisableGeneralOptionsHandler(receiver any) any {
	return Sky_net_http_FIELD_Server_DisableGeneralOptionsHandler(receiver)
}

func Net_Http_ServerTLSConfig(receiver any) any {
	return Sky_net_http_FIELD_Server_TLSConfig(receiver)
}

func Net_Http_ServerReadTimeout(receiver any) any {
	return Sky_net_http_FIELD_Server_ReadTimeout(receiver)
}

func Net_Http_ServerReadHeaderTimeout(receiver any) any {
	return Sky_net_http_FIELD_Server_ReadHeaderTimeout(receiver)
}

func Net_Http_ServerWriteTimeout(receiver any) any {
	return Sky_net_http_FIELD_Server_WriteTimeout(receiver)
}

func Net_Http_ServerIdleTimeout(receiver any) any {
	return Sky_net_http_FIELD_Server_IdleTimeout(receiver)
}

func Net_Http_ServerMaxHeaderBytes(receiver any) any {
	return Sky_net_http_FIELD_Server_MaxHeaderBytes(receiver)
}

func Net_Http_ServerTLSNextProto(receiver any) any {
	return Sky_net_http_FIELD_Server_TLSNextProto(receiver)
}

func Net_Http_ServerConnState(receiver any) any {
	return Sky_net_http_FIELD_Server_ConnState(receiver)
}

func Net_Http_ServerErrorLog(receiver any) any {
	return Sky_net_http_FIELD_Server_ErrorLog(receiver)
}

func Net_Http_ServerBaseContext(receiver any) any {
	return Sky_net_http_FIELD_Server_BaseContext(receiver)
}

func Net_Http_ServerConnContext(receiver any) any {
	return Sky_net_http_FIELD_Server_ConnContext(receiver)
}

func Net_Http_ServerHTTP2(receiver any) any {
	return Sky_net_http_FIELD_Server_HTTP2(receiver)
}

func Net_Http_ServerProtocols(receiver any) any {
	return Sky_net_http_FIELD_Server_Protocols(receiver)
}

func Net_Http_TransportProxy(receiver any) any {
	return Sky_net_http_FIELD_Transport_Proxy(receiver)
}

func Net_Http_TransportOnProxyConnectResponse(receiver any) any {
	return Sky_net_http_FIELD_Transport_OnProxyConnectResponse(receiver)
}

func Net_Http_TransportDialContext(receiver any) any {
	return Sky_net_http_FIELD_Transport_DialContext(receiver)
}

func Net_Http_TransportDial(receiver any) any {
	return Sky_net_http_FIELD_Transport_Dial(receiver)
}

func Net_Http_TransportDialTLSContext(receiver any) any {
	return Sky_net_http_FIELD_Transport_DialTLSContext(receiver)
}

func Net_Http_TransportDialTLS(receiver any) any {
	return Sky_net_http_FIELD_Transport_DialTLS(receiver)
}

func Net_Http_TransportTLSClientConfig(receiver any) any {
	return Sky_net_http_FIELD_Transport_TLSClientConfig(receiver)
}

func Net_Http_TransportTLSHandshakeTimeout(receiver any) any {
	return Sky_net_http_FIELD_Transport_TLSHandshakeTimeout(receiver)
}

func Net_Http_TransportDisableKeepAlives(receiver any) any {
	return Sky_net_http_FIELD_Transport_DisableKeepAlives(receiver)
}

func Net_Http_TransportDisableCompression(receiver any) any {
	return Sky_net_http_FIELD_Transport_DisableCompression(receiver)
}

func Net_Http_TransportMaxIdleConns(receiver any) any {
	return Sky_net_http_FIELD_Transport_MaxIdleConns(receiver)
}

func Net_Http_TransportMaxIdleConnsPerHost(receiver any) any {
	return Sky_net_http_FIELD_Transport_MaxIdleConnsPerHost(receiver)
}

func Net_Http_TransportMaxConnsPerHost(receiver any) any {
	return Sky_net_http_FIELD_Transport_MaxConnsPerHost(receiver)
}

func Net_Http_TransportIdleConnTimeout(receiver any) any {
	return Sky_net_http_FIELD_Transport_IdleConnTimeout(receiver)
}

func Net_Http_TransportResponseHeaderTimeout(receiver any) any {
	return Sky_net_http_FIELD_Transport_ResponseHeaderTimeout(receiver)
}

func Net_Http_TransportExpectContinueTimeout(receiver any) any {
	return Sky_net_http_FIELD_Transport_ExpectContinueTimeout(receiver)
}

func Net_Http_TransportTLSNextProto(receiver any) any {
	return Sky_net_http_FIELD_Transport_TLSNextProto(receiver)
}

func Net_Http_TransportProxyConnectHeader(receiver any) any {
	return Sky_net_http_FIELD_Transport_ProxyConnectHeader(receiver)
}

func Net_Http_TransportGetProxyConnectHeader(receiver any) any {
	return Sky_net_http_FIELD_Transport_GetProxyConnectHeader(receiver)
}

func Net_Http_TransportMaxResponseHeaderBytes(receiver any) any {
	return Sky_net_http_FIELD_Transport_MaxResponseHeaderBytes(receiver)
}

func Net_Http_TransportWriteBufferSize(receiver any) any {
	return Sky_net_http_FIELD_Transport_WriteBufferSize(receiver)
}

func Net_Http_TransportReadBufferSize(receiver any) any {
	return Sky_net_http_FIELD_Transport_ReadBufferSize(receiver)
}

func Net_Http_TransportForceAttemptHTTP2(receiver any) any {
	return Sky_net_http_FIELD_Transport_ForceAttemptHTTP2(receiver)
}

func Net_Http_TransportHTTP2(receiver any) any {
	return Sky_net_http_FIELD_Transport_HTTP2(receiver)
}

func Net_Http_TransportProtocols(receiver any) any {
	return Sky_net_http_FIELD_Transport_Protocols(receiver)
}

func Net_Http_ClientCloseIdleConnections(receiver any) any {
	return Sky_net_http_ClientCloseIdleConnections(receiver)
}

func Net_Http_ClientDo(receiver any, arg0 any) any {
	return Sky_net_http_ClientDo(receiver, arg0)
}

func Net_Http_ClientGet(receiver any, arg0 any) any {
	return Sky_net_http_ClientGet(receiver, arg0)
}

func Net_Http_ClientHead(receiver any, arg0 any) any {
	return Sky_net_http_ClientHead(receiver, arg0)
}

func Net_Http_ClientConnAvailable(receiver any) any {
	return Sky_net_http_ClientConnAvailable(receiver)
}

func Net_Http_ClientConnClose(receiver any) any {
	return Sky_net_http_ClientConnClose(receiver)
}

func Net_Http_ClientConnErr(receiver any) any {
	return Sky_net_http_ClientConnErr(receiver)
}

func Net_Http_ClientConnInFlight(receiver any) any {
	return Sky_net_http_ClientConnInFlight(receiver)
}

func Net_Http_ClientConnRelease(receiver any) any {
	return Sky_net_http_ClientConnRelease(receiver)
}

func Net_Http_ClientConnReserve(receiver any) any {
	return Sky_net_http_ClientConnReserve(receiver)
}

func Net_Http_ClientConnRoundTrip(receiver any, arg0 any) any {
	return Sky_net_http_ClientConnRoundTrip(receiver, arg0)
}

func Net_Http_ConnStateString(receiver any) any {
	return Sky_net_http_ConnStateString(receiver)
}

func Net_Http_CookieString(receiver any) any {
	return Sky_net_http_CookieString(receiver)
}

func Net_Http_CookieValid(receiver any) any {
	return Sky_net_http_CookieValid(receiver)
}

func Net_Http_CrossOriginProtectionAddInsecureBypassPattern(receiver any, arg0 any) any {
	return Sky_net_http_CrossOriginProtectionAddInsecureBypassPattern(receiver, arg0)
}

func Net_Http_CrossOriginProtectionAddTrustedOrigin(receiver any, arg0 any) any {
	return Sky_net_http_CrossOriginProtectionAddTrustedOrigin(receiver, arg0)
}

func Net_Http_CrossOriginProtectionCheck(receiver any, arg0 any) any {
	return Sky_net_http_CrossOriginProtectionCheck(receiver, arg0)
}

func Net_Http_CrossOriginProtectionHandler(receiver any, arg0 any) any {
	return Sky_net_http_CrossOriginProtectionHandler(receiver, arg0)
}

func Net_Http_CrossOriginProtectionSetDenyHandler(receiver any, arg0 any) any {
	return Sky_net_http_CrossOriginProtectionSetDenyHandler(receiver, arg0)
}

func Net_Http_DirOpen(receiver any, arg0 any) any {
	return Sky_net_http_DirOpen(receiver, arg0)
}

func Net_Http_FileClose(receiver any) any {
	return Sky_net_http_FileClose(receiver)
}

func Net_Http_FileRead(receiver any, arg0 any) any {
	return Sky_net_http_FileRead(receiver, arg0)
}

func Net_Http_FileReaddir(receiver any, arg0 any) any {
	return Sky_net_http_FileReaddir(receiver, arg0)
}

func Net_Http_FileSeek(receiver any, arg0 any, arg1 any) any {
	return Sky_net_http_FileSeek(receiver, arg0, arg1)
}

func Net_Http_FileSystemOpen(receiver any, arg0 any) any {
	return Sky_net_http_FileSystemOpen(receiver, arg0)
}

func Net_Http_FlusherFlush(receiver any) any {
	return Sky_net_http_FlusherFlush(receiver)
}

func Net_Http_HandlerServeHTTP(receiver any, arg0 any, arg1 any) any {
	return Sky_net_http_HandlerServeHTTP(receiver, arg0, arg1)
}

func Net_Http_HandlerFuncServeHTTP(receiver any, arg0 any, arg1 any) any {
	return Sky_net_http_HandlerFuncServeHTTP(receiver, arg0, arg1)
}

func Net_Http_HeaderAdd(receiver any, arg0 any, arg1 any) any {
	return Sky_net_http_HeaderAdd(receiver, arg0, arg1)
}

func Net_Http_HeaderClone(receiver any) any {
	return Sky_net_http_HeaderClone(receiver)
}

func Net_Http_HeaderDel(receiver any, arg0 any) any {
	return Sky_net_http_HeaderDel(receiver, arg0)
}

func Net_Http_HeaderGet(receiver any, arg0 any) any {
	return Sky_net_http_HeaderGet(receiver, arg0)
}

func Net_Http_HeaderSet(receiver any, arg0 any, arg1 any) any {
	return Sky_net_http_HeaderSet(receiver, arg0, arg1)
}

func Net_Http_HeaderValues(receiver any, arg0 any) any {
	return Sky_net_http_HeaderValues(receiver, arg0)
}

func Net_Http_MaxBytesErrorError(receiver any) any {
	return Sky_net_http_MaxBytesErrorError(receiver)
}

func Net_Http_ProtocolErrorError(receiver any) any {
	return Sky_net_http_ProtocolErrorError(receiver)
}

func Net_Http_ProtocolErrorIs(receiver any, arg0 any) any {
	return Sky_net_http_ProtocolErrorIs(receiver, arg0)
}

func Net_Http_ProtocolsHTTP1(receiver any) any {
	return Sky_net_http_ProtocolsHTTP1(receiver)
}

func Net_Http_ProtocolsHTTP2(receiver any) any {
	return Sky_net_http_ProtocolsHTTP2(receiver)
}

func Net_Http_ProtocolsSetHTTP1(receiver any, arg0 any) any {
	return Sky_net_http_ProtocolsSetHTTP1(receiver, arg0)
}

func Net_Http_ProtocolsSetHTTP2(receiver any, arg0 any) any {
	return Sky_net_http_ProtocolsSetHTTP2(receiver, arg0)
}

func Net_Http_ProtocolsSetUnencryptedHTTP2(receiver any, arg0 any) any {
	return Sky_net_http_ProtocolsSetUnencryptedHTTP2(receiver, arg0)
}

func Net_Http_ProtocolsString(receiver any) any {
	return Sky_net_http_ProtocolsString(receiver)
}

func Net_Http_ProtocolsUnencryptedHTTP2(receiver any) any {
	return Sky_net_http_ProtocolsUnencryptedHTTP2(receiver)
}

func Net_Http_PusherPush(receiver any, arg0 any, arg1 any) any {
	return Sky_net_http_PusherPush(receiver, arg0, arg1)
}

func Net_Http_RequestAddCookie(receiver any, arg0 any) any {
	return Sky_net_http_RequestAddCookie(receiver, arg0)
}

func Net_Http_RequestClone(receiver any, arg0 any) any {
	return Sky_net_http_RequestClone(receiver, arg0)
}

func Net_Http_RequestContext(receiver any) any {
	return Sky_net_http_RequestContext(receiver)
}

func Net_Http_RequestCookie(receiver any, arg0 any) any {
	return Sky_net_http_RequestCookie(receiver, arg0)
}

func Net_Http_RequestCookies(receiver any) any {
	return Sky_net_http_RequestCookies(receiver)
}

func Net_Http_RequestCookiesNamed(receiver any, arg0 any) any {
	return Sky_net_http_RequestCookiesNamed(receiver, arg0)
}

func Net_Http_RequestFormValue(receiver any, arg0 any) any {
	return Sky_net_http_RequestFormValue(receiver, arg0)
}

func Net_Http_RequestParseForm(receiver any) any {
	return Sky_net_http_RequestParseForm(receiver)
}

func Net_Http_RequestParseMultipartForm(receiver any, arg0 any) any {
	return Sky_net_http_RequestParseMultipartForm(receiver, arg0)
}

func Net_Http_RequestPathValue(receiver any, arg0 any) any {
	return Sky_net_http_RequestPathValue(receiver, arg0)
}

func Net_Http_RequestPostFormValue(receiver any, arg0 any) any {
	return Sky_net_http_RequestPostFormValue(receiver, arg0)
}

func Net_Http_RequestProtoAtLeast(receiver any, arg0 any, arg1 any) any {
	return Sky_net_http_RequestProtoAtLeast(receiver, arg0, arg1)
}

func Net_Http_RequestReferer(receiver any) any {
	return Sky_net_http_RequestReferer(receiver)
}

func Net_Http_RequestSetBasicAuth(receiver any, arg0 any, arg1 any) any {
	return Sky_net_http_RequestSetBasicAuth(receiver, arg0, arg1)
}

func Net_Http_RequestSetPathValue(receiver any, arg0 any, arg1 any) any {
	return Sky_net_http_RequestSetPathValue(receiver, arg0, arg1)
}

func Net_Http_RequestUserAgent(receiver any) any {
	return Sky_net_http_RequestUserAgent(receiver)
}

func Net_Http_RequestWithContext(receiver any, arg0 any) any {
	return Sky_net_http_RequestWithContext(receiver, arg0)
}

func Net_Http_ResponseCookies(receiver any) any {
	return Sky_net_http_ResponseCookies(receiver)
}

func Net_Http_ResponseProtoAtLeast(receiver any, arg0 any, arg1 any) any {
	return Sky_net_http_ResponseProtoAtLeast(receiver, arg0, arg1)
}

func Net_Http_ResponseControllerEnableFullDuplex(receiver any) any {
	return Sky_net_http_ResponseControllerEnableFullDuplex(receiver)
}

func Net_Http_ResponseControllerFlush(receiver any) any {
	return Sky_net_http_ResponseControllerFlush(receiver)
}

func Net_Http_ResponseWriterHeader(receiver any) any {
	return Sky_net_http_ResponseWriterHeader(receiver)
}

func Net_Http_ResponseWriterWrite(receiver any, arg0 any) any {
	return Sky_net_http_ResponseWriterWrite(receiver, arg0)
}

func Net_Http_ResponseWriterWriteHeader(receiver any, arg0 any) any {
	return Sky_net_http_ResponseWriterWriteHeader(receiver, arg0)
}

func Net_Http_RoundTripperRoundTrip(receiver any, arg0 any) any {
	return Sky_net_http_RoundTripperRoundTrip(receiver, arg0)
}

func Net_Http_ServeMuxHandle(receiver any, arg0 any, arg1 any) any {
	return Sky_net_http_ServeMuxHandle(receiver, arg0, arg1)
}

func Net_Http_ServeMuxHandleFunc(receiver any, arg0 any, arg1 any) any {
	return Sky_net_http_ServeMuxHandleFunc(receiver, arg0, arg1)
}

func Net_Http_ServeMuxHandler(receiver any, arg0 any) any {
	return Sky_net_http_ServeMuxHandler(receiver, arg0)
}

func Net_Http_ServeMuxServeHTTP(receiver any, arg0 any, arg1 any) any {
	return Sky_net_http_ServeMuxServeHTTP(receiver, arg0, arg1)
}

func Net_Http_ServerClose(receiver any) any {
	return Sky_net_http_ServerClose(receiver)
}

func Net_Http_ServerListenAndServe(receiver any) any {
	return Sky_net_http_ServerListenAndServe(receiver)
}

func Net_Http_ServerListenAndServeTLS(receiver any, arg0 any, arg1 any) any {
	return Sky_net_http_ServerListenAndServeTLS(receiver, arg0, arg1)
}

func Net_Http_ServerSetKeepAlivesEnabled(receiver any, arg0 any) any {
	return Sky_net_http_ServerSetKeepAlivesEnabled(receiver, arg0)
}

func Net_Http_ServerShutdown(receiver any, arg0 any) any {
	return Sky_net_http_ServerShutdown(receiver, arg0)
}

func Net_Http_TransportCancelRequest(receiver any, arg0 any) any {
	return Sky_net_http_TransportCancelRequest(receiver, arg0)
}

func Net_Http_TransportClone(receiver any) any {
	return Sky_net_http_TransportClone(receiver)
}

func Net_Http_TransportCloseIdleConnections(receiver any) any {
	return Sky_net_http_TransportCloseIdleConnections(receiver)
}

func Net_Http_TransportNewClientConn(receiver any, arg0 any, arg1 any, arg2 any) any {
	return Sky_net_http_TransportNewClientConn(receiver, arg0, arg1, arg2)
}

func Net_Http_TransportRegisterProtocol(receiver any, arg0 any, arg1 any) any {
	return Sky_net_http_TransportRegisterProtocol(receiver, arg0, arg1)
}

func Net_Http_TransportRoundTrip(receiver any, arg0 any) any {
	return Sky_net_http_TransportRoundTrip(receiver, arg0)
}

func Net_Http_DefaultClient(_ any) any {
	return Sky_net_http_DefaultClient
}

func Net_Http_DefaultServeMux(_ any) any {
	return Sky_net_http_DefaultServeMux
}

func Net_Http_DefaultTransport(_ any) any {
	return Sky_net_http_DefaultTransport
}

func Net_Http_ErrHeaderTooLong(_ any) any {
	return Sky_net_http_ErrHeaderTooLong
}

func Net_Http_ErrMissingBoundary(_ any) any {
	return Sky_net_http_ErrMissingBoundary
}

func Net_Http_ErrMissingContentLength(_ any) any {
	return Sky_net_http_ErrMissingContentLength
}

func Net_Http_ErrNotMultipart(_ any) any {
	return Sky_net_http_ErrNotMultipart
}

func Net_Http_ErrNotSupported(_ any) any {
	return Sky_net_http_ErrNotSupported
}

func Net_Http_ErrShortBody(_ any) any {
	return Sky_net_http_ErrShortBody
}

func Net_Http_ErrUnexpectedTrailer(_ any) any {
	return Sky_net_http_ErrUnexpectedTrailer
}

func Net_Http_LocalAddrContextKey(_ any) any {
	return Sky_net_http_LocalAddrContextKey
}

func Net_Http_NoBody(_ any) any {
	return Sky_net_http_NoBody
}

func Net_Http_ServerContextKey(_ any) any {
	return Sky_net_http_ServerContextKey
}

func Net_Http_MethodConnect() any {
	return "CONNECT"
}

func Net_Http_MethodDelete() any {
	return "DELETE"
}

func Net_Http_MethodGet() any {
	return "GET"
}

func Net_Http_MethodHead() any {
	return "HEAD"
}

func Net_Http_MethodOptions() any {
	return "OPTIONS"
}

func Net_Http_MethodPatch() any {
	return "PATCH"
}

func Net_Http_MethodPost() any {
	return "POST"
}

func Net_Http_MethodPut() any {
	return "PUT"
}

func Net_Http_MethodTrace() any {
	return "TRACE"
}

func Net_Http_SameSiteDefaultMode() any {
	return Sky_net_http_SameSiteDefaultMode
}

func Net_Http_SameSiteLaxMode() any {
	return Sky_net_http_SameSiteLaxMode
}

func Net_Http_SameSiteNoneMode() any {
	return Sky_net_http_SameSiteNoneMode
}

func Net_Http_SameSiteStrictMode() any {
	return Sky_net_http_SameSiteStrictMode
}

func Net_Http_StateActive() any {
	return Sky_net_http_StateActive
}

func Net_Http_StateClosed() any {
	return Sky_net_http_StateClosed
}

func Net_Http_StateHijacked() any {
	return Sky_net_http_StateHijacked
}

func Net_Http_StateIdle() any {
	return Sky_net_http_StateIdle
}

func Net_Http_StateNew() any {
	return Sky_net_http_StateNew
}

func Net_Http_TrailerPrefix() any {
	return "Trailer:"
}

func main() {
	sky_runMainTask(sky_call(sky_taskPerform, sky_call(sky_taskAndThen(func(now any) any {
		return func() any {
			sky_println("Current time:", now)
			data := "Hello, Sky!"
			_ = data
			return sky_call(sky_taskAndThen(func(hash any) any {
				return sky_call(sky_taskAndThen(func(hashStr any) any {
					return func() any {
						sky_println("SHA256 of 'Hello, Sky!':", hashStr)
						sky_println("Sending HTTP request...")
						return sky_call(sky_taskAndThen(func(resp any) any {
							return sky_taskSucceed(sky_println("HTTP status:", Net_Http_ResponseStatusCode(resp)))
						}), Net_Http_Get("https://httpbin.org/get"))
					}()
				}), Encoding_Hex_EncodeToString(hash))
			}), Crypto_Sha256_Sum256(sky_stringToBytes(data)))
		}()
	}), sky_timeNow(struct{}{}))))
}
