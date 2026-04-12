package sky_wrappers

import (
	"net/url"
)

func Sky_net_url_JoinPath(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	var _arg1 []string
	for _, v := range sky_asList(arg1) {
		_arg1 = append(_arg1, v.(string))
	}
	res, err := url.JoinPath(_arg0, _arg1...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_url_Parse(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := url.Parse(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_url_ParseQuery(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := url.ParseQuery(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_url_ParseRequestURI(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := url.ParseRequestURI(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_url_PathEscape(arg0 any) string {
	_arg0 := arg0.(string)
	return url.PathEscape(_arg0)
}

func Sky_net_url_PathUnescape(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := url.PathUnescape(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_url_QueryEscape(arg0 any) string {
	_arg0 := arg0.(string)
	return url.QueryEscape(_arg0)
}

func Sky_net_url_QueryUnescape(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := url.QueryUnescape(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_url_User(arg0 any) *url.Userinfo {
	_arg0 := arg0.(string)
	return url.User(_arg0)
}

func Sky_net_url_UserPassword(arg0 any, arg1 any) *url.Userinfo {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return url.UserPassword(_arg0, _arg1)
}

func Sky_net_url_ErrorError(this any) string {
	var _this *url.Error
	if _p, ok := this.(*url.Error); ok { _this = _p } else { _v := this.(url.Error); _this = &_v }

	return _this.Error()
}

func Sky_net_url_ErrorTemporary(this any) bool {
	var _this *url.Error
	if _p, ok := this.(*url.Error); ok { _this = _p } else { _v := this.(url.Error); _this = &_v }

	return _this.Temporary()
}

func Sky_net_url_ErrorTimeout(this any) bool {
	var _this *url.Error
	if _p, ok := this.(*url.Error); ok { _this = _p } else { _v := this.(url.Error); _this = &_v }

	return _this.Timeout()
}

func Sky_net_url_ErrorUnwrap(this any) SkyResult {
	var _this *url.Error
	if _p, ok := this.(*url.Error); ok { _this = _p } else { _v := this.(url.Error); _this = &_v }

	err := _this.Unwrap()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_url_ErrorOp(this any) string {
	var _this *url.Error
	if _p, ok := this.(*url.Error); ok { _this = _p } else { _v := this.(url.Error); _this = &_v }

	return _this.Op
}

func Sky_net_url_ErrorURL(this any) string {
	var _this *url.Error
	if _p, ok := this.(*url.Error); ok { _this = _p } else { _v := this.(url.Error); _this = &_v }

	return _this.URL
}

func Sky_net_url_ErrorErr(this any) error {
	var _this *url.Error
	if _p, ok := this.(*url.Error); ok { _this = _p } else { _v := this.(url.Error); _this = &_v }

	return _this.Err
}

func Sky_net_url_EscapeErrorError(this any) string {
	_this := this.(url.EscapeError)

	return _this.Error()
}

func Sky_net_url_InvalidHostErrorError(this any) string {
	_this := this.(url.InvalidHostError)

	return _this.Error()
}

func Sky_net_url_URLAppendBinary(this any, arg0 any) SkyResult {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }
	_arg0 := arg0.([]byte)
	res, err := _this.AppendBinary(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_url_URLEscapedFragment(this any) string {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }

	return _this.EscapedFragment()
}

func Sky_net_url_URLEscapedPath(this any) string {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }

	return _this.EscapedPath()
}

func Sky_net_url_URLHostname(this any) string {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }

	return _this.Hostname()
}

func Sky_net_url_URLIsAbs(this any) bool {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }

	return _this.IsAbs()
}

func Sky_net_url_URLJoinPath(this any, arg0 any) *url.URL {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }
	var _arg0 []string
	for _, v := range sky_asList(arg0) {
		_arg0 = append(_arg0, v.(string))
	}
	return _this.JoinPath(_arg0...)
}

func Sky_net_url_URLMarshalBinary(this any) SkyResult {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }

	res, err := _this.MarshalBinary()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_url_URLParse(this any, arg0 any) SkyResult {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }
	_arg0 := arg0.(string)
	res, err := _this.Parse(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_net_url_URLPort(this any) string {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }

	return _this.Port()
}

func Sky_net_url_URLQuery(this any) url.Values {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }

	return _this.Query()
}

func Sky_net_url_URLRedacted(this any) string {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }

	return _this.Redacted()
}

func Sky_net_url_URLRequestURI(this any) string {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }

	return _this.RequestURI()
}

func Sky_net_url_URLResolveReference(this any, arg0 any) *url.URL {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }
	var _arg0 *url.URL
	if arg0 != nil && arg0 != "nil" { _arg0 = arg0.(*url.URL) }
	return _this.ResolveReference(_arg0)
}

func Sky_net_url_URLString(this any) string {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }

	return _this.String()
}

func Sky_net_url_URLUnmarshalBinary(this any, arg0 any) SkyResult {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }
	_arg0 := arg0.([]byte)
	err := _this.UnmarshalBinary(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_net_url_URLScheme(this any) string {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }

	return _this.Scheme
}

func Sky_net_url_URLOpaque(this any) string {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }

	return _this.Opaque
}

func Sky_net_url_URLUser(this any) *url.Userinfo {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }

	return _this.User
}

func Sky_net_url_URLHost(this any) string {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }

	return _this.Host
}

func Sky_net_url_URLPath(this any) string {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }

	return _this.Path
}

func Sky_net_url_URLFragment(this any) string {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }

	return _this.Fragment
}

func Sky_net_url_URLRawQuery(this any) string {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }

	return _this.RawQuery
}

func Sky_net_url_URLRawPath(this any) string {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }

	return _this.RawPath
}

func Sky_net_url_URLRawFragment(this any) string {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }

	return _this.RawFragment
}

func Sky_net_url_URLForceQuery(this any) bool {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }

	return _this.ForceQuery
}

func Sky_net_url_URLOmitHost(this any) bool {
	var _this *url.URL
	if _p, ok := this.(*url.URL); ok { _this = _p } else { _v := this.(url.URL); _this = &_v }

	return _this.OmitHost
}

func Sky_net_url_UserinfoPassword(this any) any {
	var _this *url.Userinfo
	if _p, ok := this.(*url.Userinfo); ok { _this = _p } else { _v := this.(url.Userinfo); _this = &_v }

	_val, _ok := _this.Password()
	if !_ok {
		return SkyNothing()
	}
	return SkyJust(_val)
}

func Sky_net_url_UserinfoString(this any) string {
	var _this *url.Userinfo
	if _p, ok := this.(*url.Userinfo); ok { _this = _p } else { _v := this.(url.Userinfo); _this = &_v }

	return _this.String()
}

func Sky_net_url_UserinfoUsername(this any) string {
	var _this *url.Userinfo
	if _p, ok := this.(*url.Userinfo); ok { _this = _p } else { _v := this.(url.Userinfo); _this = &_v }

	return _this.Username()
}

func Sky_net_url_ValuesAdd(this any, arg0 any, arg1 any) any {
	_this := this.(url.Values)
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	_this.Add(_arg0, _arg1)
	return struct{}{}
}

func Sky_net_url_ValuesDel(this any, arg0 any) any {
	_this := this.(url.Values)
	_arg0 := arg0.(string)
	_this.Del(_arg0)
	return struct{}{}
}

func Sky_net_url_ValuesEncode(this any) string {
	_this := this.(url.Values)

	return _this.Encode()
}

func Sky_net_url_ValuesGet(this any, arg0 any) string {
	_this := this.(url.Values)
	_arg0 := arg0.(string)
	return _this.Get(_arg0)
}

func Sky_net_url_ValuesHas(this any, arg0 any) bool {
	_this := this.(url.Values)
	_arg0 := arg0.(string)
	return _this.Has(_arg0)
}

func Sky_net_url_ValuesSet(this any, arg0 any, arg1 any) any {
	_this := this.(url.Values)
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	_this.Set(_arg0, _arg1)
	return struct{}{}
}

