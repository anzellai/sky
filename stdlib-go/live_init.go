package main

import (
	"encoding/json"
	"fmt"
	"time"

	skylive_rt "sky-app/skylive_rt"
)

var _ = time.Second
var _ = fmt.Sprintf
var _ json.RawMessage

func init() {
	sky_liveAppImpl = sky_liveAppLive
}

func sky_liveAppLive(config any) any {
	c := sky_asMap(config)
	initFn := c["init"]
	updateFn := c["update"]
	viewFn := c["view"]
	subsFn := c["subscriptions"]
	guardFn := c["guard"]
	routes := sky_asList(c["routes"])
	notFound := c["notFound"]
	port := 4000
	if p, ok := c["port"]; ok {
		port = sky_asInt(p)
	}
	pageDefs := make([]skylive_rt.PageDef, 0)
	for _, r := range routes {
		rm := sky_asMap(r)
		pageDefs = append(pageDefs, skylive_rt.PageDef{Pattern: sky_asString(rm["path"]), Page: rm["page"]})
	}
	liveConfig := skylive_rt.LiveConfig{Port: port, TTL: 30 * time.Minute, StoreType: "memory", InputMode: "debounce"}
	liveApp := skylive_rt.LiveApp{
		Init: func(req map[string]any, page any) (any, []any) {
			result := initFn.(func(any) any)(req)
			if t, ok := result.(SkyTuple2); ok {
				return t.V0, nil
			}
			return result, nil
		},
		Update: func(msg any, model any) (any, []any) {
			result := updateFn.(func(any) any)(msg).(func(any) any)(model)
			if t, ok := result.(SkyTuple2); ok {
				return t.V0, nil
			}
			return result, nil
		},
		View: func(model any) *skylive_rt.VNode {
			result := viewFn.(func(any) any)(model)
			return skylive_rt.MapToVNode(result)
		},
		DecodeMsg: func(name string, args []json.RawMessage) (any, error) {
			msg := map[string]any{"SkyName": name, "Tag": 0}
			for i, a := range args {
				var v any
				json.Unmarshal(a, &v)
				msg[fmt.Sprintf("V%d", i)] = v
			}
			return msg, nil
		},
		URLForPage: func(page any) string {
			pm := sky_asMap(page)
			if n, ok := pm["SkyName"].(string); ok {
				for _, pd := range pageDefs {
					if sky_asMap(pd.Page)["SkyName"] == n {
						return pd.Pattern
					}
				}
			}
			return "/"
		},
		TitleForPage: func(page any) string {
			pm := sky_asMap(page)
			if n, ok := pm["SkyName"].(string); ok {
				return n
			}
			return "Sky.Live"
		},
		FixModel:   func(model any) any { return model },
		Routes:     pageDefs,
		NotFound:   notFound,
		BuildNavigateMsg: func(page any) any {
			return map[string]any{"SkyName": "Navigate", "Tag": 99, "V0": page}
		},
		Subscriptions: func(model any) any {
			if subsFn == nil {
				return nil
			}
			return subsFn.(func(any) any)(model)
		},
		MsgTagToName: func(tag int) string { return fmt.Sprintf("Msg%d", tag) },
		Guard: func(msg any, model any) error {
			if guardFn == nil {
				return nil
			}
			result := guardFn.(func(any) any)(msg).(func(any) any)(model)
			if sr, ok := result.(SkyResult); ok && sr.Tag == 1 {
				return fmt.Errorf("%v", sr.ErrValue)
			}
			return nil
		},
	}
	skylive_rt.StartServer(liveConfig, liveApp)
	return nil
}
