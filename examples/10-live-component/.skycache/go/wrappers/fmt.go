package sky_wrappers

import (
	"fmt"
	"io"
)

func Sky_fmt_Append(arg0 any, arg1 any) []byte {
	_arg0 := arg0.([]byte)
	var _arg1 []any
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(any))
	}
	return fmt.Append(_arg0, _arg1...)
}

func Sky_fmt_Appendf(arg0 any, arg1 any, arg2 any) []byte {
	_arg0 := arg0.([]byte)
	_arg1 := arg1.(string)
	var _arg2 []any
	for _, v := range arg2.([]any) {
		_arg2 = append(_arg2, v.(any))
	}
	return fmt.Appendf(_arg0, _arg1, _arg2...)
}

func Sky_fmt_Appendln(arg0 any, arg1 any) []byte {
	_arg0 := arg0.([]byte)
	var _arg1 []any
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(any))
	}
	return fmt.Appendln(_arg0, _arg1...)
}

func Sky_fmt_Errorf(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	var _arg1 []any
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(any))
	}
	err := fmt.Errorf(_arg0, _arg1...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_fmt_FormatString(arg0 any, arg1 any) string {
	_arg0 := arg0.(fmt.State)
	_arg1 := arg1.(rune)
	return fmt.FormatString(_arg0, _arg1)
}

func Sky_fmt_Fprint(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(io.Writer)
	var _arg1 []any
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(any))
	}
	res, err := fmt.Fprint(_arg0, _arg1...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_fmt_Fprintf(arg0 any, arg1 any, arg2 any) SkyResult {
	_arg0 := arg0.(io.Writer)
	_arg1 := arg1.(string)
	var _arg2 []any
	for _, v := range arg2.([]any) {
		_arg2 = append(_arg2, v.(any))
	}
	res, err := fmt.Fprintf(_arg0, _arg1, _arg2...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_fmt_Fprintln(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(io.Writer)
	var _arg1 []any
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(any))
	}
	res, err := fmt.Fprintln(_arg0, _arg1...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_fmt_Fscan(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(io.Reader)
	var _arg1 []any
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(any))
	}
	res, err := fmt.Fscan(_arg0, _arg1...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_fmt_Fscanf(arg0 any, arg1 any, arg2 any) SkyResult {
	_arg0 := arg0.(io.Reader)
	_arg1 := arg1.(string)
	var _arg2 []any
	for _, v := range arg2.([]any) {
		_arg2 = append(_arg2, v.(any))
	}
	res, err := fmt.Fscanf(_arg0, _arg1, _arg2...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_fmt_Fscanln(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(io.Reader)
	var _arg1 []any
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(any))
	}
	res, err := fmt.Fscanln(_arg0, _arg1...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_fmt_Print(arg0 any) SkyResult {
	var _arg0 []any
	for _, v := range arg0.([]any) {
		_arg0 = append(_arg0, v.(any))
	}
	res, err := fmt.Print(_arg0...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_fmt_Printf(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	var _arg1 []any
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(any))
	}
	res, err := fmt.Printf(_arg0, _arg1...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_fmt_Println(arg0 any) SkyResult {
	var _arg0 []any
	for _, v := range arg0.([]any) {
		_arg0 = append(_arg0, v.(any))
	}
	res, err := fmt.Println(_arg0...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_fmt_Scan(arg0 any) SkyResult {
	var _arg0 []any
	for _, v := range arg0.([]any) {
		_arg0 = append(_arg0, v.(any))
	}
	res, err := fmt.Scan(_arg0...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_fmt_Scanf(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	var _arg1 []any
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(any))
	}
	res, err := fmt.Scanf(_arg0, _arg1...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_fmt_Scanln(arg0 any) SkyResult {
	var _arg0 []any
	for _, v := range arg0.([]any) {
		_arg0 = append(_arg0, v.(any))
	}
	res, err := fmt.Scanln(_arg0...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_fmt_Sprint(arg0 any) string {
	var _arg0 []any
	for _, v := range arg0.([]any) {
		_arg0 = append(_arg0, v.(any))
	}
	return fmt.Sprint(_arg0...)
}

func Sky_fmt_Sprintf(arg0 any, arg1 any) string {
	_arg0 := arg0.(string)
	var _arg1 []any
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(any))
	}
	return fmt.Sprintf(_arg0, _arg1...)
}

func Sky_fmt_Sprintln(arg0 any) string {
	var _arg0 []any
	for _, v := range arg0.([]any) {
		_arg0 = append(_arg0, v.(any))
	}
	return fmt.Sprintln(_arg0...)
}

func Sky_fmt_Sscan(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	var _arg1 []any
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(any))
	}
	res, err := fmt.Sscan(_arg0, _arg1...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_fmt_Sscanf(arg0 any, arg1 any, arg2 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	var _arg2 []any
	for _, v := range arg2.([]any) {
		_arg2 = append(_arg2, v.(any))
	}
	res, err := fmt.Sscanf(_arg0, _arg1, _arg2...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_fmt_Sscanln(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	var _arg1 []any
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(any))
	}
	res, err := fmt.Sscanln(_arg0, _arg1...)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_fmt_FormatterFormat(this any, arg0 any, arg1 any) any {
	_this := this.(fmt.Formatter)
	_arg0 := arg0.(fmt.State)
	_arg1 := arg1.(rune)
	_this.Format(_arg0, _arg1)
	return struct{}{}
}

func Sky_fmt_GoStringerGoString(this any) string {
	_this := this.(fmt.GoStringer)

	return _this.GoString()
}

func Sky_fmt_ScanStateRead(this any, arg0 any) SkyResult {
	_this := this.(fmt.ScanState)
	_arg0 := arg0.([]byte)
	res, err := _this.Read(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_fmt_ScanStateReadRune(this any) (rune, int, error) {
	_this := this.(fmt.ScanState)

	return _this.ReadRune()
}

func Sky_fmt_ScanStateSkipSpace(this any) any {
	_this := this.(fmt.ScanState)

	_this.SkipSpace()
	return struct{}{}
}

func Sky_fmt_ScanStateToken(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(fmt.ScanState)
	_arg0 := arg0.(bool)
	_skyFn1 := arg1.(func(any) any)
	_arg1 := func(p0 rune) bool {
		return _skyFn1(p0).(bool)
	}
	res, err := _this.Token(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_fmt_ScanStateUnreadRune(this any) SkyResult {
	_this := this.(fmt.ScanState)

	err := _this.UnreadRune()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_fmt_ScanStateWidth(this any) (int, bool) {
	_this := this.(fmt.ScanState)

	return _this.Width()
}

func Sky_fmt_ScannerScan(this any, arg0 any, arg1 any) SkyResult {
	_this := this.(fmt.Scanner)
	_arg0 := arg0.(fmt.ScanState)
	_arg1 := arg1.(rune)
	err := _this.Scan(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_fmt_StateFlag(this any, arg0 any) bool {
	_this := this.(fmt.State)
	_arg0 := arg0.(int)
	return _this.Flag(_arg0)
}

func Sky_fmt_StatePrecision(this any) (int, bool) {
	_this := this.(fmt.State)

	return _this.Precision()
}

func Sky_fmt_StateWidth(this any) (int, bool) {
	_this := this.(fmt.State)

	return _this.Width()
}

func Sky_fmt_StateWrite(this any, arg0 any) SkyResult {
	_this := this.(fmt.State)
	_arg0 := arg0.([]byte)
	res, err := _this.Write(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_fmt_StringerString(this any) string {
	_this := this.(fmt.Stringer)

	return _this.String()
}

