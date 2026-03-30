// hash:12-6
var Counter_Increment = map[string]any{"Tag": 0, "SkyName": "Increment"}

var Counter_Decrement = map[string]any{"Tag": 1, "SkyName": "Decrement"}

var Counter_Reset = map[string]any{"Tag": 2, "SkyName": "Reset"}

func Counter_Init_() any {
	return map[string]any{"count": 0, "label": "Counter"}
}

func Counter_InitWith(label any) any {
	return map[string]any{"count": 0, "label": label}
}

func Counter_Update(msg any, counter any) any {
	return func() any { return func() any { __subject := msg; if sky_asMap(__subject)["SkyName"] == "Increment" { return SkyTuple2{V0: sky_recordUpdate(counter, map[string]any{"count": sky_numBinop("+", sky_asMap(counter)["count"], 1)}), V1: sky_cmdNone()} };  if sky_asMap(__subject)["SkyName"] == "Decrement" { return SkyTuple2{V0: sky_recordUpdate(counter, map[string]any{"count": sky_numBinop("-", sky_asMap(counter)["count"], 1)}), V1: sky_cmdNone()} };  if sky_asMap(__subject)["SkyName"] == "Reset" { return SkyTuple2{V0: sky_recordUpdate(counter, map[string]any{"count": 0}), V1: sky_cmdNone()} };  panic("non-exhaustive case expression") }() }()
}

func Counter_View(toMsg any, counter any) any {
	return sky_call(sky_call(sky_htmlEl("div"), []any{sky_call(sky_attrSimple("class"), "counter-component")}), []any{sky_call(sky_call(sky_htmlEl("h3"), []any{}), []any{sky_htmlText(sky_asMap(counter)["label"])}), sky_call(sky_call(sky_htmlEl("div"), []any{sky_call(sky_attrSimple("class"), "counter-display")}), []any{sky_call(sky_call(sky_htmlEl("span"), []any{sky_call(sky_attrSimple("class"), "count")}), []any{sky_htmlText(sky_stringFromInt(sky_asMap(counter)["count"]))})}), sky_call(sky_call(sky_htmlEl("div"), []any{sky_call(sky_attrSimple("class"), "counter-buttons")}), []any{sky_call(sky_call(sky_htmlEl("button"), []any{sky_call(sky_evtHandler("click"), sky_call(toMsg, map[string]any{"Tag": 1, "SkyName": "Decrement"})), sky_call(sky_attrSimple("class"), "btn")}), []any{sky_htmlText("-")}), sky_call(sky_call(sky_htmlEl("button"), []any{sky_call(sky_evtHandler("click"), sky_call(toMsg, map[string]any{"Tag": 2, "SkyName": "Reset"})), sky_call(sky_attrSimple("class"), "btn btn-sm")}), []any{sky_htmlText("Reset")}), sky_call(sky_call(sky_htmlEl("button"), []any{sky_call(sky_evtHandler("click"), sky_call(toMsg, map[string]any{"Tag": 0, "SkyName": "Increment"})), sky_call(sky_attrSimple("class"), "btn")}), []any{sky_htmlText("+")})})})
}

func Counter_GetCount(counter any) any {
	return sky_asMap(counter)["coun"]
}