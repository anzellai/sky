package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	skylive_rt "sky-app/skylive_rt"
)

var _ = time.Second
var _ json.RawMessage

func init() {
	sky_liveAppImpl = sky_liveAppLive
}

// skyLiveEnv reads config with priority: defaults < sky.toml < env vars < .env
func skyLiveEnv(key string, fallback string) string {
	// Start with fallback (compiled default / sky.toml value)
	val := fallback

	// Override with system env var
	if ev := os.Getenv(key); ev != "" {
		val = ev
	}

	// Override with .env file (highest priority)
	if dotVal, ok := skyDotEnv[key]; ok {
		val = dotVal
	}

	return val
}

// skyDotEnv holds parsed .env values (loaded once at init)
var skyDotEnv = loadDotEnv()

func loadDotEnv() map[string]string {
	result := make(map[string]string)
	f, err := os.Open(".env")
	if err != nil {
		return result
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			// Strip surrounding quotes
			if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
				val = val[1 : len(val)-1]
			}
			result[key] = val
		}
	}
	return result
}

// skyTomlLive reads [live] section from sky.toml
func skyTomlLive(key string, fallback string) string {
	data, err := os.ReadFile("sky.toml")
	if err != nil {
		return fallback
	}
	lines := strings.Split(string(data), "\n")
	inLive := false
	inSession := false
	inStatic := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[live]" {
			inLive = true
			inSession = false
			inStatic = false
			continue
		}
		if trimmed == "[live.session]" {
			inLive = true
			inSession = true
			inStatic = false
			continue
		}
		if trimmed == "[live.static]" {
			inLive = true
			inSession = false
			inStatic = true
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			inLive = false
			inSession = false
			inStatic = false
			continue
		}
		if !inLive {
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			continue
		}
		k := strings.TrimSpace(parts[0])
		v := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
		tomlKey := ""
		if inSession {
			switch k {
			case "store":
				tomlKey = "SKY_LIVE_SESSION_STORE"
			case "path":
				tomlKey = "SKY_LIVE_SESSION_PATH"
			case "url":
				tomlKey = "SKY_LIVE_SESSION_URL"
			}
		} else if inStatic {
			if k == "dir" {
				tomlKey = "SKY_LIVE_STATIC_DIR"
			}
		} else {
			switch k {
			case "port":
				tomlKey = "SKY_LIVE_PORT"
			case "input":
				tomlKey = "SKY_LIVE_INPUT"
			case "poll_interval":
				tomlKey = "SKY_LIVE_POLL_INTERVAL"
			case "ttl":
				tomlKey = "SKY_LIVE_TTL"
			}
		}
		if tomlKey == key {
			return v
		}
	}
	return fallback
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

	// Config priority: defaults < sky.toml < env vars < .env
	portStr := skyLiveEnv("SKY_LIVE_PORT", skyTomlLive("SKY_LIVE_PORT", "4000"))
	port, _ := strconv.Atoi(portStr)
	if port == 0 {
		port = 4000
	}

	inputMode := skyLiveEnv("SKY_LIVE_INPUT", skyTomlLive("SKY_LIVE_INPUT", "debounce"))
	storeType := skyLiveEnv("SKY_LIVE_SESSION_STORE", skyTomlLive("SKY_LIVE_SESSION_STORE", "memory"))
	storePath := skyLiveEnv("SKY_LIVE_SESSION_PATH", skyTomlLive("SKY_LIVE_SESSION_PATH", ""))
	storeURL := skyLiveEnv("SKY_LIVE_SESSION_URL", skyTomlLive("SKY_LIVE_SESSION_URL", ""))
	staticDir := skyLiveEnv("SKY_LIVE_STATIC_DIR", skyTomlLive("SKY_LIVE_STATIC_DIR", ""))
	ttlStr := skyLiveEnv("SKY_LIVE_TTL", skyTomlLive("SKY_LIVE_TTL", "30m"))
	ttl, err := time.ParseDuration(ttlStr)
	if err != nil {
		ttl = 30 * time.Minute
	}
	pollStr := skyLiveEnv("SKY_LIVE_POLL_INTERVAL", skyTomlLive("SKY_LIVE_POLL_INTERVAL", "0"))
	pollInterval, _ := strconv.Atoi(pollStr)

	// Resolve store path
	finalStorePath := storePath
	if storeURL != "" {
		finalStorePath = storeURL
	}

	pageDefs := make([]skylive_rt.PageDef, 0)
	for _, r := range routes {
		rm := sky_asMap(r)
		pageDefs = append(pageDefs, skylive_rt.PageDef{Pattern: sky_asString(rm["path"]), Page: rm["page"]})
	}

	liveConfig := skylive_rt.LiveConfig{
		Port:         port,
		TTL:          ttl,
		StoreType:    storeType,
		StorePath:    finalStorePath,
		InputMode:    inputMode,
		PollInterval: pollInterval,
		StaticDir:    staticDir,
	}

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
		FixModel: func(model any) any { return model },
		Routes:   pageDefs,
		NotFound: notFound,
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
