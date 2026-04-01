package sky_wrappers

import (
	"time"
)

func Sky_time_After(arg0 any) <-chan time.Time {
	_arg0 := arg0.(time.Duration)
	return time.After(_arg0)
}

func Sky_time_AfterFunc(arg0 any, arg1 any) *time.Timer {
	_arg0 := arg0.(time.Duration)
	_skyFn1 := arg1.(func(any) any)
	_arg1 := func() {
		_skyFn1(nil)
	}
	return time.AfterFunc(_arg0, _arg1)
}

func Sky_time_Date(arg0 any, arg1 any, arg2 any, arg3 any, arg4 any, arg5 any, arg6 any, arg7 any) time.Time {
	_arg0 := arg0.(int)
	_arg1 := arg1.(time.Month)
	_arg2 := arg2.(int)
	_arg3 := arg3.(int)
	_arg4 := arg4.(int)
	_arg5 := arg5.(int)
	_arg6 := arg6.(int)
	_arg7 := arg7.(*time.Location)
	return time.Date(_arg0, _arg1, _arg2, _arg3, _arg4, _arg5, _arg6, _arg7)
}

func Sky_time_FixedZone(arg0 any, arg1 any) *time.Location {
	_arg0 := arg0.(string)
	_arg1 := arg1.(int)
	return time.FixedZone(_arg0, _arg1)
}

func Sky_time_LoadLocation(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := time.LoadLocation(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_time_LoadLocationFromTZData(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.([]byte)
	res, err := time.LoadLocationFromTZData(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_time_NewTicker(arg0 any) *time.Ticker {
	_arg0 := arg0.(time.Duration)
	return time.NewTicker(_arg0)
}

func Sky_time_NewTimer(arg0 any) *time.Timer {
	_arg0 := arg0.(time.Duration)
	return time.NewTimer(_arg0)
}

func Sky_time_Now() time.Time {
	return time.Now()
}

func Sky_time_Parse(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	res, err := time.Parse(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_time_ParseDuration(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := time.ParseDuration(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_time_ParseInLocation(arg0 any, arg1 any, arg2 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	_arg2 := arg2.(*time.Location)
	res, err := time.ParseInLocation(_arg0, _arg1, _arg2)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_time_Since(arg0 any) time.Duration {
	_arg0 := arg0.(time.Time)
	return time.Since(_arg0)
}

func Sky_time_Sleep(arg0 any) any {
	_arg0 := arg0.(time.Duration)
	time.Sleep(_arg0)
	return struct{}{}
}

func Sky_time_Tick(arg0 any) <-chan time.Time {
	_arg0 := arg0.(time.Duration)
	return time.Tick(_arg0)
}

func Sky_time_Unix(arg0 any, arg1 any) time.Time {
	_arg0 := arg0.(int64)
	_arg1 := arg1.(int64)
	return time.Unix(_arg0, _arg1)
}

func Sky_time_UnixMicro(arg0 any) time.Time {
	_arg0 := arg0.(int64)
	return time.UnixMicro(_arg0)
}

func Sky_time_UnixMilli(arg0 any) time.Time {
	_arg0 := arg0.(int64)
	return time.UnixMilli(_arg0)
}

func Sky_time_Until(arg0 any) time.Duration {
	_arg0 := arg0.(time.Time)
	return time.Until(_arg0)
}

func Sky_time_Local() any {
	return time.Local
}

func Sky_time_UTC() any {
	return time.UTC
}

func Sky_time_ANSIC() any {
	return time.ANSIC
}

func Sky_time_April() any {
	return time.April
}

func Sky_time_August() any {
	return time.August
}

func Sky_time_DateOnly() any {
	return time.DateOnly
}

func Sky_time_DateTime() any {
	return time.DateTime
}

func Sky_time_December() any {
	return time.December
}

func Sky_time_February() any {
	return time.February
}

func Sky_time_Friday() any {
	return time.Friday
}

func Sky_time_Hour() any {
	return time.Hour
}

func Sky_time_January() any {
	return time.January
}

func Sky_time_July() any {
	return time.July
}

func Sky_time_June() any {
	return time.June
}

func Sky_time_Kitchen() any {
	return time.Kitchen
}

func Sky_time_Layout() any {
	return time.Layout
}

func Sky_time_March() any {
	return time.March
}

func Sky_time_May() any {
	return time.May
}

func Sky_time_Microsecond() any {
	return time.Microsecond
}

func Sky_time_Millisecond() any {
	return time.Millisecond
}

func Sky_time_Minute() any {
	return time.Minute
}

func Sky_time_Monday() any {
	return time.Monday
}

func Sky_time_Nanosecond() any {
	return time.Nanosecond
}

func Sky_time_November() any {
	return time.November
}

func Sky_time_October() any {
	return time.October
}

func Sky_time_RFC1123() any {
	return time.RFC1123
}

func Sky_time_RFC1123Z() any {
	return time.RFC1123Z
}

func Sky_time_RFC3339() any {
	return time.RFC3339
}

func Sky_time_RFC3339Nano() any {
	return time.RFC3339Nano
}

func Sky_time_RFC822() any {
	return time.RFC822
}

func Sky_time_RFC822Z() any {
	return time.RFC822Z
}

func Sky_time_RFC850() any {
	return time.RFC850
}

func Sky_time_RubyDate() any {
	return time.RubyDate
}

func Sky_time_Saturday() any {
	return time.Saturday
}

func Sky_time_Second() any {
	return time.Second
}

func Sky_time_September() any {
	return time.September
}

func Sky_time_Stamp() any {
	return time.Stamp
}

func Sky_time_StampMicro() any {
	return time.StampMicro
}

func Sky_time_StampMilli() any {
	return time.StampMilli
}

func Sky_time_StampNano() any {
	return time.StampNano
}

func Sky_time_Sunday() any {
	return time.Sunday
}

func Sky_time_Thursday() any {
	return time.Thursday
}

func Sky_time_TimeOnly() any {
	return time.TimeOnly
}

func Sky_time_Tuesday() any {
	return time.Tuesday
}

func Sky_time_UnixDate() any {
	return time.UnixDate
}

func Sky_time_Wednesday() any {
	return time.Wednesday
}

func Sky_time_DurationAbs(this any) time.Duration {
	_this := this.(time.Duration)

	return _this.Abs()
}

func Sky_time_DurationHours(this any) float64 {
	_this := this.(time.Duration)

	return _this.Hours()
}

func Sky_time_DurationMicroseconds(this any) int64 {
	_this := this.(time.Duration)

	return _this.Microseconds()
}

func Sky_time_DurationMilliseconds(this any) int64 {
	_this := this.(time.Duration)

	return _this.Milliseconds()
}

func Sky_time_DurationMinutes(this any) float64 {
	_this := this.(time.Duration)

	return _this.Minutes()
}

func Sky_time_DurationNanoseconds(this any) int64 {
	_this := this.(time.Duration)

	return _this.Nanoseconds()
}

func Sky_time_DurationRound(this any, arg0 any) time.Duration {
	_this := this.(time.Duration)
	_arg0 := arg0.(time.Duration)
	return _this.Round(_arg0)
}

func Sky_time_DurationSeconds(this any) float64 {
	_this := this.(time.Duration)

	return _this.Seconds()
}

func Sky_time_DurationString(this any) string {
	_this := this.(time.Duration)

	return _this.String()
}

func Sky_time_DurationTruncate(this any, arg0 any) time.Duration {
	_this := this.(time.Duration)
	_arg0 := arg0.(time.Duration)
	return _this.Truncate(_arg0)
}

func Sky_time_LocationString(this any) string {
	_this := this.(*time.Location)

	return _this.String()
}

func Sky_time_MonthString(this any) string {
	_this := this.(time.Month)

	return _this.String()
}

func Sky_time_ParseErrorError(this any) string {
	_this := this.(*time.ParseError)

	return _this.Error()
}

func Sky_time_ParseErrorLayout(this any) string {
	_this := this.(*time.ParseError)

	return _this.Layout
}

func Sky_time_ParseErrorValue(this any) string {
	_this := this.(*time.ParseError)

	return _this.Value
}

func Sky_time_ParseErrorLayoutElem(this any) string {
	_this := this.(*time.ParseError)

	return _this.LayoutElem
}

func Sky_time_ParseErrorValueElem(this any) string {
	_this := this.(*time.ParseError)

	return _this.ValueElem
}

func Sky_time_ParseErrorMessage(this any) string {
	_this := this.(*time.ParseError)

	return _this.Message
}

func Sky_time_TickerReset(this any, arg0 any) any {
	_this := this.(*time.Ticker)
	_arg0 := arg0.(time.Duration)
	_this.Reset(_arg0)
	return struct{}{}
}

func Sky_time_TickerStop(this any) any {
	_this := this.(*time.Ticker)

	_this.Stop()
	return struct{}{}
}

func Sky_time_TickerC(this any) <-chan time.Time {
	_this := this.(*time.Ticker)

	return _this.C
}

func Sky_time_TimeAdd(this any, arg0 any) time.Time {
	_this := this.(*time.Time)
	_arg0 := arg0.(time.Duration)
	return _this.Add(_arg0)
}

func Sky_time_TimeAddDate(this any, arg0 any, arg1 any, arg2 any) time.Time {
	_this := this.(*time.Time)
	_arg0 := arg0.(int)
	_arg1 := arg1.(int)
	_arg2 := arg2.(int)
	return _this.AddDate(_arg0, _arg1, _arg2)
}

func Sky_time_TimeAfter(this any, arg0 any) bool {
	_this := this.(*time.Time)
	_arg0 := arg0.(time.Time)
	return _this.After(_arg0)
}

func Sky_time_TimeAppendBinary(this any, arg0 any) SkyResult {
	_this := this.(*time.Time)
	_arg0 := arg0.([]byte)
	res, err := _this.AppendBinary(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_time_TimeAppendFormat(this any, arg0 any, arg1 any) []byte {
	_this := this.(*time.Time)
	_arg0 := arg0.([]byte)
	_arg1 := arg1.(string)
	return _this.AppendFormat(_arg0, _arg1)
}

func Sky_time_TimeAppendText(this any, arg0 any) SkyResult {
	_this := this.(*time.Time)
	_arg0 := arg0.([]byte)
	res, err := _this.AppendText(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_time_TimeBefore(this any, arg0 any) bool {
	_this := this.(*time.Time)
	_arg0 := arg0.(time.Time)
	return _this.Before(_arg0)
}

func Sky_time_TimeClock(this any) any {
	_this := this.(*time.Time)

	_r0, _r1, _r2 := _this.Clock()
	return SkyTuple3{V0: _r0, V1: _r1, V2: _r2}
}

func Sky_time_TimeCompare(this any, arg0 any) int {
	_this := this.(*time.Time)
	_arg0 := arg0.(time.Time)
	return _this.Compare(_arg0)
}

func Sky_time_TimeDate(this any) any {
	_this := this.(*time.Time)

	_r0, _r1, _r2 := _this.Date()
	return SkyTuple3{V0: _r0, V1: _r1, V2: _r2}
}

func Sky_time_TimeDay(this any) int {
	_this := this.(*time.Time)

	return _this.Day()
}

func Sky_time_TimeEqual(this any, arg0 any) bool {
	_this := this.(*time.Time)
	_arg0 := arg0.(time.Time)
	return _this.Equal(_arg0)
}

func Sky_time_TimeFormat(this any, arg0 any) string {
	_this := this.(*time.Time)
	_arg0 := arg0.(string)
	return _this.Format(_arg0)
}

func Sky_time_TimeGoString(this any) string {
	_this := this.(*time.Time)

	return _this.GoString()
}

func Sky_time_TimeGobDecode(this any, arg0 any) SkyResult {
	_this := this.(*time.Time)
	_arg0 := arg0.([]byte)
	err := _this.GobDecode(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_time_TimeGobEncode(this any) SkyResult {
	_this := this.(*time.Time)

	res, err := _this.GobEncode()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_time_TimeHour(this any) int {
	_this := this.(*time.Time)

	return _this.Hour()
}

func Sky_time_TimeISOWeek(this any) any {
	_this := this.(*time.Time)

	_r0, _r1 := _this.ISOWeek()
	return SkyTuple2{V0: _r0, V1: _r1}
}

func Sky_time_TimeIn(this any, arg0 any) time.Time {
	_this := this.(*time.Time)
	_arg0 := arg0.(*time.Location)
	return _this.In(_arg0)
}

func Sky_time_TimeIsDST(this any) bool {
	_this := this.(*time.Time)

	return _this.IsDST()
}

func Sky_time_TimeIsZero(this any) bool {
	_this := this.(*time.Time)

	return _this.IsZero()
}

func Sky_time_TimeLocal(this any) time.Time {
	_this := this.(*time.Time)

	return _this.Local()
}

func Sky_time_TimeLocation(this any) *time.Location {
	_this := this.(*time.Time)

	return _this.Location()
}

func Sky_time_TimeMarshalBinary(this any) SkyResult {
	_this := this.(*time.Time)

	res, err := _this.MarshalBinary()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_time_TimeMarshalJSON(this any) SkyResult {
	_this := this.(*time.Time)

	res, err := _this.MarshalJSON()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_time_TimeMarshalText(this any) SkyResult {
	_this := this.(*time.Time)

	res, err := _this.MarshalText()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_time_TimeMinute(this any) int {
	_this := this.(*time.Time)

	return _this.Minute()
}

func Sky_time_TimeMonth(this any) time.Month {
	_this := this.(*time.Time)

	return _this.Month()
}

func Sky_time_TimeNanosecond(this any) int {
	_this := this.(*time.Time)

	return _this.Nanosecond()
}

func Sky_time_TimeRound(this any, arg0 any) time.Time {
	_this := this.(*time.Time)
	_arg0 := arg0.(time.Duration)
	return _this.Round(_arg0)
}

func Sky_time_TimeSecond(this any) int {
	_this := this.(*time.Time)

	return _this.Second()
}

func Sky_time_TimeString(this any) string {
	_this := this.(*time.Time)

	return _this.String()
}

func Sky_time_TimeSub(this any, arg0 any) time.Duration {
	_this := this.(*time.Time)
	_arg0 := arg0.(time.Time)
	return _this.Sub(_arg0)
}

func Sky_time_TimeTruncate(this any, arg0 any) time.Time {
	_this := this.(*time.Time)
	_arg0 := arg0.(time.Duration)
	return _this.Truncate(_arg0)
}

func Sky_time_TimeUTC(this any) time.Time {
	_this := this.(*time.Time)

	return _this.UTC()
}

func Sky_time_TimeUnix(this any) int64 {
	_this := this.(*time.Time)

	return _this.Unix()
}

func Sky_time_TimeUnixMicro(this any) int64 {
	_this := this.(*time.Time)

	return _this.UnixMicro()
}

func Sky_time_TimeUnixMilli(this any) int64 {
	_this := this.(*time.Time)

	return _this.UnixMilli()
}

func Sky_time_TimeUnixNano(this any) int64 {
	_this := this.(*time.Time)

	return _this.UnixNano()
}

func Sky_time_TimeUnmarshalBinary(this any, arg0 any) SkyResult {
	_this := this.(*time.Time)
	_arg0 := arg0.([]byte)
	err := _this.UnmarshalBinary(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_time_TimeUnmarshalJSON(this any, arg0 any) SkyResult {
	_this := this.(*time.Time)
	_arg0 := arg0.([]byte)
	err := _this.UnmarshalJSON(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_time_TimeUnmarshalText(this any, arg0 any) SkyResult {
	_this := this.(*time.Time)
	_arg0 := arg0.([]byte)
	err := _this.UnmarshalText(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_time_TimeWeekday(this any) time.Weekday {
	_this := this.(*time.Time)

	return _this.Weekday()
}

func Sky_time_TimeYear(this any) int {
	_this := this.(*time.Time)

	return _this.Year()
}

func Sky_time_TimeYearDay(this any) int {
	_this := this.(*time.Time)

	return _this.YearDay()
}

func Sky_time_TimeZone(this any) any {
	_this := this.(*time.Time)

	_r0, _r1 := _this.Zone()
	return SkyTuple2{V0: _r0, V1: _r1}
}

func Sky_time_TimeZoneBounds(this any) any {
	_this := this.(*time.Time)

	_r0, _r1 := _this.ZoneBounds()
	return SkyTuple2{V0: _r0, V1: _r1}
}

func Sky_time_TimerReset(this any, arg0 any) bool {
	_this := this.(*time.Timer)
	_arg0 := arg0.(time.Duration)
	return _this.Reset(_arg0)
}

func Sky_time_TimerStop(this any) bool {
	_this := this.(*time.Timer)

	return _this.Stop()
}

func Sky_time_TimerC(this any) <-chan time.Time {
	_this := this.(*time.Timer)

	return _this.C
}

func Sky_time_WeekdayString(this any) string {
	_this := this.(time.Weekday)

	return _this.String()
}

