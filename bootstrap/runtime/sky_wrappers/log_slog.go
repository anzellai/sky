package sky_wrappers

import (
	"log/slog"
	"context"
	"time"
	"io"
	"log"
)

func Sky_log_slog_Any(arg0 any, arg1 any) slog.Attr {
	_arg0 := arg0.(string)
	_arg1 := arg1.(any)
	return slog.Any(_arg0, _arg1)
}

func Sky_log_slog_AnyValue(arg0 any) slog.Value {
	_arg0 := arg0.(any)
	return slog.AnyValue(_arg0)
}

func Sky_log_slog_Bool(arg0 any, arg1 any) slog.Attr {
	_arg0 := arg0.(string)
	_arg1 := arg1.(bool)
	return slog.Bool(_arg0, _arg1)
}

func Sky_log_slog_BoolValue(arg0 any) slog.Value {
	_arg0 := arg0.(bool)
	return slog.BoolValue(_arg0)
}

func Sky_log_slog_Debug(arg0 any, arg1 any) any {
	_arg0 := arg0.(string)
	var _arg1 []any
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(any))
	}
	slog.Debug(_arg0, _arg1...)
	return struct{}{}
}

func Sky_log_slog_DebugContext(arg0 any, arg1 any, arg2 any) any {
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	var _arg2 []any
	for _, v := range arg2.([]any) {
		_arg2 = append(_arg2, v.(any))
	}
	slog.DebugContext(_arg0, _arg1, _arg2...)
	return struct{}{}
}

func Sky_log_slog_Default() *slog.Logger {
	return slog.Default()
}

func Sky_log_slog_Duration(arg0 any, arg1 any) slog.Attr {
	_arg0 := arg0.(string)
	_arg1 := arg1.(time.Duration)
	return slog.Duration(_arg0, _arg1)
}

func Sky_log_slog_DurationValue(arg0 any) slog.Value {
	_arg0 := arg0.(time.Duration)
	return slog.DurationValue(_arg0)
}

func Sky_log_slog_Error(arg0 any, arg1 any) any {
	_arg0 := arg0.(string)
	var _arg1 []any
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(any))
	}
	slog.Error(_arg0, _arg1...)
	return struct{}{}
}

func Sky_log_slog_ErrorContext(arg0 any, arg1 any, arg2 any) any {
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	var _arg2 []any
	for _, v := range arg2.([]any) {
		_arg2 = append(_arg2, v.(any))
	}
	slog.ErrorContext(_arg0, _arg1, _arg2...)
	return struct{}{}
}

func Sky_log_slog_Float64(arg0 any, arg1 any) slog.Attr {
	_arg0 := arg0.(string)
	_arg1 := arg1.(float64)
	return slog.Float64(_arg0, _arg1)
}

func Sky_log_slog_Float64Value(arg0 any) slog.Value {
	_arg0 := arg0.(float64)
	return slog.Float64Value(_arg0)
}

func Sky_log_slog_Group(arg0 any, arg1 any) slog.Attr {
	_arg0 := arg0.(string)
	var _arg1 []any
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(any))
	}
	return slog.Group(_arg0, _arg1...)
}

func Sky_log_slog_GroupAttrs(arg0 any, arg1 any) slog.Attr {
	_arg0 := arg0.(string)
	var _arg1 []slog.Attr
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(slog.Attr))
	}
	return slog.GroupAttrs(_arg0, _arg1...)
}

func Sky_log_slog_GroupValue(arg0 any) slog.Value {
	var _arg0 []slog.Attr
	for _, v := range arg0.([]any) {
		_arg0 = append(_arg0, v.(slog.Attr))
	}
	return slog.GroupValue(_arg0...)
}

func Sky_log_slog_Info(arg0 any, arg1 any) any {
	_arg0 := arg0.(string)
	var _arg1 []any
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(any))
	}
	slog.Info(_arg0, _arg1...)
	return struct{}{}
}

func Sky_log_slog_InfoContext(arg0 any, arg1 any, arg2 any) any {
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	var _arg2 []any
	for _, v := range arg2.([]any) {
		_arg2 = append(_arg2, v.(any))
	}
	slog.InfoContext(_arg0, _arg1, _arg2...)
	return struct{}{}
}

func Sky_log_slog_Int(arg0 any, arg1 any) slog.Attr {
	_arg0 := arg0.(string)
	_arg1 := arg1.(int)
	return slog.Int(_arg0, _arg1)
}

func Sky_log_slog_Int64(arg0 any, arg1 any) slog.Attr {
	_arg0 := arg0.(string)
	_arg1 := arg1.(int64)
	return slog.Int64(_arg0, _arg1)
}

func Sky_log_slog_Int64Value(arg0 any) slog.Value {
	_arg0 := arg0.(int64)
	return slog.Int64Value(_arg0)
}

func Sky_log_slog_IntValue(arg0 any) slog.Value {
	_arg0 := arg0.(int)
	return slog.IntValue(_arg0)
}

func Sky_log_slog_Log(arg0 any, arg1 any, arg2 any, arg3 any) any {
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(slog.Level)
	_arg2 := arg2.(string)
	var _arg3 []any
	for _, v := range arg3.([]any) {
		_arg3 = append(_arg3, v.(any))
	}
	slog.Log(_arg0, _arg1, _arg2, _arg3...)
	return struct{}{}
}

func Sky_log_slog_LogAttrs(arg0 any, arg1 any, arg2 any, arg3 any) any {
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(slog.Level)
	_arg2 := arg2.(string)
	var _arg3 []slog.Attr
	for _, v := range arg3.([]any) {
		_arg3 = append(_arg3, v.(slog.Attr))
	}
	slog.LogAttrs(_arg0, _arg1, _arg2, _arg3...)
	return struct{}{}
}

func Sky_log_slog_New(arg0 any) *slog.Logger {
	_arg0 := arg0.(slog.Handler)
	return slog.New(_arg0)
}

func Sky_log_slog_NewJSONHandler(arg0 any, arg1 any) *slog.JSONHandler {
	_arg0 := arg0.(io.Writer)
	_arg1 := arg1.(*slog.HandlerOptions)
	return slog.NewJSONHandler(_arg0, _arg1)
}

func Sky_log_slog_NewLogLogger(arg0 any, arg1 any) *log.Logger {
	_arg0 := arg0.(slog.Handler)
	_arg1 := arg1.(slog.Level)
	return slog.NewLogLogger(_arg0, _arg1)
}

func Sky_log_slog_NewMultiHandler(arg0 any) *slog.MultiHandler {
	var _arg0 []slog.Handler
	for _, v := range arg0.([]any) {
		_arg0 = append(_arg0, v.(slog.Handler))
	}
	return slog.NewMultiHandler(_arg0...)
}

func Sky_log_slog_NewRecord(arg0 any, arg1 any, arg2 any, arg3 any) slog.Record {
	_arg0 := arg0.(time.Time)
	_arg1 := arg1.(slog.Level)
	_arg2 := arg2.(string)
	_arg3 := arg3.(uintptr)
	return slog.NewRecord(_arg0, _arg1, _arg2, _arg3)
}

func Sky_log_slog_NewTextHandler(arg0 any, arg1 any) *slog.TextHandler {
	_arg0 := arg0.(io.Writer)
	_arg1 := arg1.(*slog.HandlerOptions)
	return slog.NewTextHandler(_arg0, _arg1)
}

func Sky_log_slog_SetDefault(arg0 any) any {
	_arg0 := arg0.(*slog.Logger)
	slog.SetDefault(_arg0)
	return struct{}{}
}

func Sky_log_slog_SetLogLoggerLevel(arg0 any) slog.Level {
	_arg0 := arg0.(slog.Level)
	return slog.SetLogLoggerLevel(_arg0)
}

func Sky_log_slog_String(arg0 any, arg1 any) slog.Attr {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return slog.String(_arg0, _arg1)
}

func Sky_log_slog_StringValue(arg0 any) slog.Value {
	_arg0 := arg0.(string)
	return slog.StringValue(_arg0)
}

func Sky_log_slog_Time(arg0 any, arg1 any) slog.Attr {
	_arg0 := arg0.(string)
	_arg1 := arg1.(time.Time)
	return slog.Time(_arg0, _arg1)
}

func Sky_log_slog_TimeValue(arg0 any) slog.Value {
	_arg0 := arg0.(time.Time)
	return slog.TimeValue(_arg0)
}

func Sky_log_slog_Uint64(arg0 any, arg1 any) slog.Attr {
	_arg0 := arg0.(string)
	_arg1 := arg1.(uint64)
	return slog.Uint64(_arg0, _arg1)
}

func Sky_log_slog_Uint64Value(arg0 any) slog.Value {
	_arg0 := arg0.(uint64)
	return slog.Uint64Value(_arg0)
}

func Sky_log_slog_Warn(arg0 any, arg1 any) any {
	_arg0 := arg0.(string)
	var _arg1 []any
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(any))
	}
	slog.Warn(_arg0, _arg1...)
	return struct{}{}
}

func Sky_log_slog_WarnContext(arg0 any, arg1 any, arg2 any) any {
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	var _arg2 []any
	for _, v := range arg2.([]any) {
		_arg2 = append(_arg2, v.(any))
	}
	slog.WarnContext(_arg0, _arg1, _arg2...)
	return struct{}{}
}

func Sky_log_slog_With(arg0 any) *slog.Logger {
	var _arg0 []any
	for _, v := range arg0.([]any) {
		_arg0 = append(_arg0, v.(any))
	}
	return slog.With(_arg0...)
}

func Sky_log_slog_DiscardHandler() any {
	return slog.DiscardHandler
}

func Sky_log_slog_KindAny() any {
	return slog.KindAny
}

func Sky_log_slog_KindBool() any {
	return slog.KindBool
}

func Sky_log_slog_KindDuration() any {
	return slog.KindDuration
}

func Sky_log_slog_KindFloat64() any {
	return slog.KindFloat64
}

func Sky_log_slog_KindGroup() any {
	return slog.KindGroup
}

func Sky_log_slog_KindInt64() any {
	return slog.KindInt64
}

func Sky_log_slog_KindLogValuer() any {
	return slog.KindLogValuer
}

func Sky_log_slog_KindTime() any {
	return slog.KindTime
}

func Sky_log_slog_KindUint64() any {
	return slog.KindUint64
}

func Sky_log_slog_LevelDebug() any {
	return slog.LevelDebug
}

func Sky_log_slog_LevelError() any {
	return slog.LevelError
}

func Sky_log_slog_LevelInfo() any {
	return slog.LevelInfo
}

func Sky_log_slog_LevelKey() any {
	return slog.LevelKey
}

func Sky_log_slog_LevelWarn() any {
	return slog.LevelWarn
}

func Sky_log_slog_MessageKey() any {
	return slog.MessageKey
}

func Sky_log_slog_SourceKey() any {
	return slog.SourceKey
}

func Sky_log_slog_TimeKey() any {
	return slog.TimeKey
}

func Sky_log_slog_AttrEqual(this any, arg0 any) bool {
	_this := this.(*slog.Attr)
	_arg0 := arg0.(slog.Attr)
	return _this.Equal(_arg0)
}

func Sky_log_slog_AttrString(this any) string {
	_this := this.(*slog.Attr)

	return _this.String()
}

func Sky_log_slog_AttrKey(this any) string {
	_this := this.(*slog.Attr)

	return _this.Key
}

func Sky_log_slog_AttrValue(this any) slog.Value {
	_this := this.(*slog.Attr)

	return _this.Value
}

func Sky_log_slog_HandlerEnabled(this any, arg0 any, arg1 any) bool {
	_this := this.(slog.Handler)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(slog.Level)
	return _this.Enabled(_arg0, _arg1)
}

func Sky_log_slog_HandlerHandle(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(slog.Handler)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(slog.Record)
	err := _this.Handle(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}


func Sky_log_slog_HandlerWithGroup(this any, arg0 any) slog.Handler {
	_this := this.(slog.Handler)
	_arg0 := arg0.(string)
	return _this.WithGroup(_arg0)
}

func Sky_log_slog_HandlerOptionsAddSource(this any) bool {
	_this := this.(*slog.HandlerOptions)

	return _this.AddSource
}

func Sky_log_slog_HandlerOptionsLevel(this any) slog.Leveler {
	_this := this.(*slog.HandlerOptions)

	return _this.Level
}

func Sky_log_slog_HandlerOptionsReplaceAttr(this any) func(groups []string, a slog.Attr) slog.Attr {
	_this := this.(*slog.HandlerOptions)

	return _this.ReplaceAttr
}

func Sky_log_slog_JSONHandlerEnabled(this any, arg0 any, arg1 any) bool {
	_this := this.(*slog.JSONHandler)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(slog.Level)
	return _this.Enabled(_arg0, _arg1)
}

func Sky_log_slog_JSONHandlerHandle(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(*slog.JSONHandler)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(slog.Record)
	err := _this.Handle(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}


func Sky_log_slog_JSONHandlerWithGroup(this any, arg0 any) slog.Handler {
	_this := this.(*slog.JSONHandler)
	_arg0 := arg0.(string)
	return _this.WithGroup(_arg0)
}

func Sky_log_slog_LevelAppendText(this any, arg0 any) SkyResult {
	_this := this.(slog.Level)
	_arg0 := arg0.([]byte)
	res, err := _this.AppendText(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_log_slog_LevelLevel(this any) slog.Level {
	_this := this.(slog.Level)

	return _this.Level()
}

func Sky_log_slog_LevelMarshalJSON(this any) SkyResult {
	_this := this.(slog.Level)

	res, err := _this.MarshalJSON()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_log_slog_LevelMarshalText(this any) SkyResult {
	_this := this.(slog.Level)

	res, err := _this.MarshalText()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_log_slog_LevelString(this any) string {
	_this := this.(slog.Level)

	return _this.String()
}

func Sky_log_slog_LevelUnmarshalJSON(this any, arg0 any) SkyResult {
	_this := this.(slog.Level)
	_arg0 := arg0.([]byte)
	err := _this.UnmarshalJSON(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_log_slog_LevelUnmarshalText(this any, arg0 any) SkyResult {
	_this := this.(slog.Level)
	_arg0 := arg0.([]byte)
	err := _this.UnmarshalText(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_log_slog_LevelVarAppendText(this any, arg0 any) SkyResult {
	_this := this.(*slog.LevelVar)
	_arg0 := arg0.([]byte)
	res, err := _this.AppendText(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_log_slog_LevelVarLevel(this any) slog.Level {
	_this := this.(*slog.LevelVar)

	return _this.Level()
}

func Sky_log_slog_LevelVarMarshalText(this any) SkyResult {
	_this := this.(*slog.LevelVar)

	res, err := _this.MarshalText()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_log_slog_LevelVarSet(this any, arg0 any) any {
	_this := this.(*slog.LevelVar)
	_arg0 := arg0.(slog.Level)
	_this.Set(_arg0)
	return struct{}{}
}

func Sky_log_slog_LevelVarString(this any) string {
	_this := this.(*slog.LevelVar)

	return _this.String()
}

func Sky_log_slog_LevelVarUnmarshalText(this any, arg0 any) SkyResult {
	_this := this.(*slog.LevelVar)
	_arg0 := arg0.([]byte)
	err := _this.UnmarshalText(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_log_slog_LevelerLevel(this any) slog.Level {
	_this := this.(slog.Leveler)

	return _this.Level()
}

func Sky_log_slog_LogValuerLogValue(this any) slog.Value {
	_this := this.(slog.LogValuer)

	return _this.LogValue()
}

func Sky_log_slog_LoggerDebug(this any, arg0 any, arg1 any) any {
	_this := this.(*slog.Logger)
	_arg0 := arg0.(string)
	var _arg1 []any
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(any))
	}
	_this.Debug(_arg0, _arg1...)
	return struct{}{}
}

func Sky_log_slog_LoggerDebugContext(this any, arg0 any, arg1 any, arg2 any) any {
	_this := this.(*slog.Logger)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	var _arg2 []any
	for _, v := range arg2.([]any) {
		_arg2 = append(_arg2, v.(any))
	}
	_this.DebugContext(_arg0, _arg1, _arg2...)
	return struct{}{}
}

func Sky_log_slog_LoggerEnabled(this any, arg0 any, arg1 any) bool {
	_this := this.(*slog.Logger)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(slog.Level)
	return _this.Enabled(_arg0, _arg1)
}

func Sky_log_slog_LoggerError(this any, arg0 any, arg1 any) any {
	_this := this.(*slog.Logger)
	_arg0 := arg0.(string)
	var _arg1 []any
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(any))
	}
	_this.Error(_arg0, _arg1...)
	return struct{}{}
}

func Sky_log_slog_LoggerErrorContext(this any, arg0 any, arg1 any, arg2 any) any {
	_this := this.(*slog.Logger)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	var _arg2 []any
	for _, v := range arg2.([]any) {
		_arg2 = append(_arg2, v.(any))
	}
	_this.ErrorContext(_arg0, _arg1, _arg2...)
	return struct{}{}
}

func Sky_log_slog_LoggerHandler(this any) slog.Handler {
	_this := this.(*slog.Logger)

	return _this.Handler()
}

func Sky_log_slog_LoggerInfo(this any, arg0 any, arg1 any) any {
	_this := this.(*slog.Logger)
	_arg0 := arg0.(string)
	var _arg1 []any
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(any))
	}
	_this.Info(_arg0, _arg1...)
	return struct{}{}
}

func Sky_log_slog_LoggerInfoContext(this any, arg0 any, arg1 any, arg2 any) any {
	_this := this.(*slog.Logger)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	var _arg2 []any
	for _, v := range arg2.([]any) {
		_arg2 = append(_arg2, v.(any))
	}
	_this.InfoContext(_arg0, _arg1, _arg2...)
	return struct{}{}
}

func Sky_log_slog_LoggerLog(this any, arg0 any, arg1 any, arg2 any, arg3 any) any {
	_this := this.(*slog.Logger)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(slog.Level)
	_arg2 := arg2.(string)
	var _arg3 []any
	for _, v := range arg3.([]any) {
		_arg3 = append(_arg3, v.(any))
	}
	_this.Log(_arg0, _arg1, _arg2, _arg3...)
	return struct{}{}
}

func Sky_log_slog_LoggerLogAttrs(this any, arg0 any, arg1 any, arg2 any, arg3 any) any {
	_this := this.(*slog.Logger)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(slog.Level)
	_arg2 := arg2.(string)
	var _arg3 []slog.Attr
	for _, v := range arg3.([]any) {
		_arg3 = append(_arg3, v.(slog.Attr))
	}
	_this.LogAttrs(_arg0, _arg1, _arg2, _arg3...)
	return struct{}{}
}

func Sky_log_slog_LoggerWarn(this any, arg0 any, arg1 any) any {
	_this := this.(*slog.Logger)
	_arg0 := arg0.(string)
	var _arg1 []any
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(any))
	}
	_this.Warn(_arg0, _arg1...)
	return struct{}{}
}

func Sky_log_slog_LoggerWarnContext(this any, arg0 any, arg1 any, arg2 any) any {
	_this := this.(*slog.Logger)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	var _arg2 []any
	for _, v := range arg2.([]any) {
		_arg2 = append(_arg2, v.(any))
	}
	_this.WarnContext(_arg0, _arg1, _arg2...)
	return struct{}{}
}

func Sky_log_slog_LoggerWith(this any, arg0 any) *slog.Logger {
	_this := this.(*slog.Logger)
	var _arg0 []any
	for _, v := range arg0.([]any) {
		_arg0 = append(_arg0, v.(any))
	}
	return _this.With(_arg0...)
}

func Sky_log_slog_LoggerWithGroup(this any, arg0 any) *slog.Logger {
	_this := this.(*slog.Logger)
	_arg0 := arg0.(string)
	return _this.WithGroup(_arg0)
}

func Sky_log_slog_MultiHandlerEnabled(this any, arg0 any, arg1 any) bool {
	_this := this.(*slog.MultiHandler)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(slog.Level)
	return _this.Enabled(_arg0, _arg1)
}

func Sky_log_slog_MultiHandlerHandle(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(*slog.MultiHandler)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(slog.Record)
	err := _this.Handle(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}


func Sky_log_slog_MultiHandlerWithGroup(this any, arg0 any) slog.Handler {
	_this := this.(*slog.MultiHandler)
	_arg0 := arg0.(string)
	return _this.WithGroup(_arg0)
}

func Sky_log_slog_RecordAdd(this any, arg0 any) any {
	_this := this.(*slog.Record)
	var _arg0 []any
	for _, v := range arg0.([]any) {
		_arg0 = append(_arg0, v.(any))
	}
	_this.Add(_arg0...)
	return struct{}{}
}

func Sky_log_slog_RecordAddAttrs(this any, arg0 any) any {
	_this := this.(*slog.Record)
	var _arg0 []slog.Attr
	for _, v := range arg0.([]any) {
		_arg0 = append(_arg0, v.(slog.Attr))
	}
	_this.AddAttrs(_arg0...)
	return struct{}{}
}

func Sky_log_slog_RecordAttrs(this any, arg0 any) any {
	_this := this.(*slog.Record)
	_skyFn0 := arg0.(func(any) any)
	_arg0 := func(p0 slog.Attr) bool {
		return _skyFn0(p0).(bool)
	}
	_this.Attrs(_arg0)
	return struct{}{}
}

func Sky_log_slog_RecordClone(this any) slog.Record {
	_this := this.(*slog.Record)

	return _this.Clone()
}

func Sky_log_slog_RecordNumAttrs(this any) int {
	_this := this.(*slog.Record)

	return _this.NumAttrs()
}

func Sky_log_slog_RecordSource(this any) *slog.Source {
	_this := this.(*slog.Record)

	return _this.Source()
}

func Sky_log_slog_RecordTime(this any) time.Time {
	_this := this.(*slog.Record)

	return _this.Time
}

func Sky_log_slog_RecordMessage(this any) string {
	_this := this.(*slog.Record)

	return _this.Message
}

func Sky_log_slog_RecordLevel(this any) slog.Level {
	_this := this.(*slog.Record)

	return _this.Level
}

func Sky_log_slog_RecordPC(this any) uintptr {
	_this := this.(*slog.Record)

	return _this.PC
}

func Sky_log_slog_SourceFunction(this any) string {
	_this := this.(*slog.Source)

	return _this.Function
}

func Sky_log_slog_SourceFile(this any) string {
	_this := this.(*slog.Source)

	return _this.File
}

func Sky_log_slog_SourceLine(this any) int {
	_this := this.(*slog.Source)

	return _this.Line
}

func Sky_log_slog_TextHandlerEnabled(this any, arg0 any, arg1 any) bool {
	_this := this.(*slog.TextHandler)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(slog.Level)
	return _this.Enabled(_arg0, _arg1)
}

func Sky_log_slog_TextHandlerHandle(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(*slog.TextHandler)
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(slog.Record)
	err := _this.Handle(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}


func Sky_log_slog_TextHandlerWithGroup(this any, arg0 any) slog.Handler {
	_this := this.(*slog.TextHandler)
	_arg0 := arg0.(string)
	return _this.WithGroup(_arg0)
}

func Sky_log_slog_ValueAny(this any) any {
	_this := this.(*slog.Value)

	return _this.Any()
}

func Sky_log_slog_ValueBool(this any) bool {
	_this := this.(*slog.Value)

	return _this.Bool()
}

func Sky_log_slog_ValueDuration(this any) time.Duration {
	_this := this.(*slog.Value)

	return _this.Duration()
}

func Sky_log_slog_ValueEqual(this any, arg0 any) bool {
	_this := this.(*slog.Value)
	_arg0 := arg0.(slog.Value)
	return _this.Equal(_arg0)
}

func Sky_log_slog_ValueFloat64(this any) float64 {
	_this := this.(*slog.Value)

	return _this.Float64()
}

func Sky_log_slog_ValueGroup(this any) []slog.Attr {
	_this := this.(*slog.Value)

	return _this.Group()
}

func Sky_log_slog_ValueInt64(this any) int64 {
	_this := this.(*slog.Value)

	return _this.Int64()
}

func Sky_log_slog_ValueKind(this any) slog.Kind {
	_this := this.(*slog.Value)

	return _this.Kind()
}

func Sky_log_slog_ValueLogValuer(this any) slog.LogValuer {
	_this := this.(*slog.Value)

	return _this.LogValuer()
}

func Sky_log_slog_ValueResolve(this any) slog.Value {
	_this := this.(*slog.Value)

	return _this.Resolve()
}

func Sky_log_slog_ValueString(this any) string {
	_this := this.(*slog.Value)

	return _this.String()
}

func Sky_log_slog_ValueTime(this any) time.Time {
	_this := this.(*slog.Value)

	return _this.Time()
}

func Sky_log_slog_ValueUint64(this any) uint64 {
	_this := this.(*slog.Value)

	return _this.Uint64()
}

