package sky_sky_core_time

import (
	"fmt"
	"time"
)

type SkyResult struct {
	Tag      int
	SkyName  string
	OkValue  any
	ErrValue any
}

func ok(v any) SkyResult  { return SkyResult{Tag: 0, SkyName: "Ok", OkValue: v} }
func err(v any) SkyResult { return SkyResult{Tag: 1, SkyName: "Err", ErrValue: v} }

// Now returns a Task (thunk) that produces current time in Unix millis.
func Now(_ any) any {
	return func() any {
		return ok(int(time.Now().UnixMilli()))
	}
}

// Format a Unix millis timestamp using Go time layout.
func Format(layout, millis any) any {
	ms := asInt(millis)
	t := time.UnixMilli(int64(ms))
	return t.Format(asString(layout))
}

// Parse a time string using Go time layout. Returns Result.
func Parse(layout, timeStr any) any {
	t, e := time.Parse(asString(layout), asString(timeStr))
	if e != nil {
		return err(e.Error())
	}
	return ok(int(t.UnixMilli()))
}

func UnixSeconds(_ any) any {
	return func() any { return ok(int(time.Now().Unix())) }
}

func UnixMillis(_ any) any {
	return func() any { return ok(int(time.Now().UnixMilli())) }
}

func Sleep(millis any) any {
	return func() any {
		time.Sleep(time.Duration(asInt(millis)) * time.Millisecond)
		return ok(struct{}{})
	}
}

func Since(millis any) any {
	ms := asInt(millis)
	now := time.Now().UnixMilli()
	return int(now - int64(ms))
}

func Year(millis any) any   { return time.UnixMilli(int64(asInt(millis))).Year() }
func Month(millis any) any  { return int(time.UnixMilli(int64(asInt(millis))).Month()) }
func Day(millis any) any    { return time.UnixMilli(int64(asInt(millis))).Day() }
func Hour(millis any) any   { return time.UnixMilli(int64(asInt(millis))).Hour() }
func Minute(millis any) any { return time.UnixMilli(int64(asInt(millis))).Minute() }
func Second(millis any) any { return time.UnixMilli(int64(asInt(millis))).Second() }

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
