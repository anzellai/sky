package rt

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"runtime"
)

// logMsgDecodeError — structured message to stderr when a client-sent
// argument doesn't fit the Msg constructor's parameter. Gives the
// developer enough to find the mis-bound handler in their view.
func logMsgDecodeError(fn any, arg any, raw json.RawMessage) {
	rv := reflect.ValueOf(fn)
	expected := "<unknown>"
	if rv.Kind() == reflect.Func && rv.Type().NumIn() > 0 {
		expected = rv.Type().In(0).String()
	}
	fnName := ""
	if rv.Kind() == reflect.Func {
		fnName = runtime.FuncForPC(rv.Pointer()).Name()
	}
	fmt.Fprintf(os.Stderr,
		"[sky.live] Msg decode error: %s expected %s but got %T (%v); "+
			"raw=%s. Likely fix: check the view binding — e.g. onInput on a "+
			"radio sends [checked:bool], not the value. Use onClick with a "+
			"fully-applied Msg per radio instead.\n",
		fnName, expected, arg, arg, string(raw))
}

// logFormDecodeError reports a form submit the handler's record cannot take
// (form_decode.go). The event is dropped and the model is not touched.
func logFormDecodeError(e *FormDecodeError) {
	fmt.Fprintf(os.Stderr,
		"[sky.live] form submit decode error (FormDecode): %s. The submit is "+
			"dropped. Give each record field an input with the same Ui.name, and "+
			"make its value parse as the field's type.\n", e.Error())
}
