//go:build !js

package rt

import (
	"net/http"
	"reflect"
	"strings"
)

// App.withPersistExclude / Live.withRefreshOnLoad (v0.29.0): model fields a
// full page load of an existing Sky.Live session refreshes from `init`.
// Values the server owns (site settings, a feature flag, the server clock)
// would otherwise keep the copy the session took on its first page, across
// every later page and across a restart of a persistent session store. The
// other fields keep the session's value. (On a Sky.Spa client the same
// builder leaves the fields out of localStorage: spa_persist.go.)

// Live_withRefreshOnLoad stores the field-name list under "PersistExclude".
func Live_withRefreshOnLoad(fields, cfg any) any {
	return liveCfgSet(cfg, "PersistExclude", fields)
}

// refreshPersistExcluded runs `init` for this request (its command is not
// run: the session is not new) and copies the excluded fields from its model
// into the session's model.
func (app *liveApp) refreshPersistExcluded(model any, r *http.Request, sessKey string) any {
	if len(app.persistExclude) == 0 || app.init == nil {
		return model
	}
	_, initParams := applyRouteWithParams(app, model, r.URL.Path)
	req := map[string]any{
		"sessionKey": sessKey,
		"path":       r.URL.Path,
		"query":      r.URL.RawQuery,
		"params":     initParams,
		"method":     r.Method,
		"host":       r.Host,
		"headers":    requestHeadersToDict(r),
		"cookies":    cookiesToDict(r.Cookies()),
	}
	fresh := tupleFirst(sky_call(app.init, req))
	return copySkyFields(model, fresh, app.persistExclude)
}

// copySkyFields returns a copy of `dst` with the named Sky fields taken from
// `src` (two values of one record type: a typed record struct, matched by its
// `sky:"name,…"` tag or its Go field name, or a map[string]any). A name the
// record does not have is skipped, and a pair of different shapes returns
// `dst` unchanged. `dst` itself is never written.
func copySkyFields(dst, src any, names []string) any {
	if len(names) == 0 || dst == nil || src == nil {
		return dst
	}
	if dm, ok := dst.(map[string]any); ok {
		sm, ok := src.(map[string]any)
		if !ok {
			return dst
		}
		out := make(map[string]any, len(dm))
		for k, v := range dm {
			out[k] = v
		}
		for _, n := range names {
			for _, k := range []string{n, upperFirstASCII(n)} {
				if v, ok := sm[k]; ok {
					out[k] = v
				}
			}
		}
		return out
	}
	dv, sv := reflect.ValueOf(dst), reflect.ValueOf(src)
	if dv.Kind() != reflect.Struct || sv.Type() != dv.Type() {
		return dst
	}
	out := reflect.New(dv.Type()).Elem()
	out.Set(dv)
	for _, n := range names {
		if i := skyStructFieldIndex(dv.Type(), n); i >= 0 && out.Field(i).CanSet() {
			out.Field(i).Set(sv.Field(i))
		}
	}
	return out.Interface()
}

// skyStructFieldIndex is the index of the struct field that holds the Sky
// field `name`, or -1.
func skyStructFieldIndex(t reflect.Type, name string) int {
	upper := upperFirstASCII(name)
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if tag, ok := f.Tag.Lookup("sky"); ok {
			if strings.SplitN(tag, ",", 2)[0] == name {
				return i
			}
		}
		if f.Name == upper {
			return i
		}
	}
	return -1
}

func upperFirstASCII(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}
