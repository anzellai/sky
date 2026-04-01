package sky_wrappers

import (
	"strings"
	"unicode"
	"io"
)

func Sky_strings_Clone(arg0 any) string {
	_arg0 := arg0.(string)
	return strings.Clone(_arg0)
}

func Sky_strings_Compare(arg0 any, arg1 any) int {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return strings.Compare(_arg0, _arg1)
}

func Sky_strings_Contains(arg0 any, arg1 any) bool {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return strings.Contains(_arg0, _arg1)
}

func Sky_strings_ContainsAny(arg0 any, arg1 any) bool {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return strings.ContainsAny(_arg0, _arg1)
}

func Sky_strings_ContainsFunc(arg0 any, arg1 any) bool {
	_arg0 := arg0.(string)
	_skyFn1 := arg1.(func(any) any)
	_arg1 := func(p0 rune) bool {
		return _skyFn1(p0).(bool)
	}
	return strings.ContainsFunc(_arg0, _arg1)
}

func Sky_strings_ContainsRune(arg0 any, arg1 any) bool {
	_arg0 := arg0.(string)
	_arg1 := arg1.(rune)
	return strings.ContainsRune(_arg0, _arg1)
}

func Sky_strings_Count(arg0 any, arg1 any) int {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return strings.Count(_arg0, _arg1)
}

func Sky_strings_Cut(arg0 any, arg1 any) any {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	_r0, _r1, _r2 := strings.Cut(_arg0, _arg1)
	return SkyTuple3{V0: _r0, V1: _r1, V2: _r2}
}

func Sky_strings_CutPrefix(arg0 any, arg1 any) any {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	_val, _ok := strings.CutPrefix(_arg0, _arg1)
	if !_ok {
		return SkyNothing()
	}
	return SkyJust(_val)
}

func Sky_strings_CutSuffix(arg0 any, arg1 any) any {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	_val, _ok := strings.CutSuffix(_arg0, _arg1)
	if !_ok {
		return SkyNothing()
	}
	return SkyJust(_val)
}

func Sky_strings_EqualFold(arg0 any, arg1 any) bool {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return strings.EqualFold(_arg0, _arg1)
}

func Sky_strings_Fields(arg0 any) []string {
	_arg0 := arg0.(string)
	return strings.Fields(_arg0)
}

func Sky_strings_FieldsFunc(arg0 any, arg1 any) []string {
	_arg0 := arg0.(string)
	_skyFn1 := arg1.(func(any) any)
	_arg1 := func(p0 rune) bool {
		return _skyFn1(p0).(bool)
	}
	return strings.FieldsFunc(_arg0, _arg1)
}

func Sky_strings_HasPrefix(arg0 any, arg1 any) bool {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return strings.HasPrefix(_arg0, _arg1)
}

func Sky_strings_HasSuffix(arg0 any, arg1 any) bool {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return strings.HasSuffix(_arg0, _arg1)
}

func Sky_strings_Index(arg0 any, arg1 any) int {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return strings.Index(_arg0, _arg1)
}

func Sky_strings_IndexAny(arg0 any, arg1 any) int {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return strings.IndexAny(_arg0, _arg1)
}

func Sky_strings_IndexByte(arg0 any, arg1 any) int {
	_arg0 := arg0.(string)
	_arg1 := arg1.(byte)
	return strings.IndexByte(_arg0, _arg1)
}

func Sky_strings_IndexFunc(arg0 any, arg1 any) int {
	_arg0 := arg0.(string)
	_skyFn1 := arg1.(func(any) any)
	_arg1 := func(p0 rune) bool {
		return _skyFn1(p0).(bool)
	}
	return strings.IndexFunc(_arg0, _arg1)
}

func Sky_strings_IndexRune(arg0 any, arg1 any) int {
	_arg0 := arg0.(string)
	_arg1 := arg1.(rune)
	return strings.IndexRune(_arg0, _arg1)
}

func Sky_strings_Join(arg0 any, arg1 any) string {
	_arg0 := arg0.([]string)
	_arg1 := arg1.(string)
	return strings.Join(_arg0, _arg1)
}

func Sky_strings_LastIndex(arg0 any, arg1 any) int {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return strings.LastIndex(_arg0, _arg1)
}

func Sky_strings_LastIndexAny(arg0 any, arg1 any) int {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return strings.LastIndexAny(_arg0, _arg1)
}

func Sky_strings_LastIndexByte(arg0 any, arg1 any) int {
	_arg0 := arg0.(string)
	_arg1 := arg1.(byte)
	return strings.LastIndexByte(_arg0, _arg1)
}

func Sky_strings_LastIndexFunc(arg0 any, arg1 any) int {
	_arg0 := arg0.(string)
	_skyFn1 := arg1.(func(any) any)
	_arg1 := func(p0 rune) bool {
		return _skyFn1(p0).(bool)
	}
	return strings.LastIndexFunc(_arg0, _arg1)
}

func Sky_strings_Map(arg0 any, arg1 any) string {
	_skyFn0 := arg0.(func(any) any)
	_arg0 := func(p0 rune) rune {
		return _skyFn0(p0).(rune)
	}
	_arg1 := arg1.(string)
	return strings.Map(_arg0, _arg1)
}

func Sky_strings_NewReader(arg0 any) *strings.Reader {
	_arg0 := arg0.(string)
	return strings.NewReader(_arg0)
}

func Sky_strings_NewReplacer(arg0 any) *strings.Replacer {
	var _arg0 []string
	for _, v := range sky_asList(arg0) {
		_arg0 = append(_arg0, v.(string))
	}
	return strings.NewReplacer(_arg0...)
}

func Sky_strings_Repeat(arg0 any, arg1 any) string {
	_arg0 := arg0.(string)
	_arg1 := arg1.(int)
	return strings.Repeat(_arg0, _arg1)
}

func Sky_strings_Replace(arg0 any, arg1 any, arg2 any, arg3 any) string {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	_arg2 := arg2.(string)
	_arg3 := arg3.(int)
	return strings.Replace(_arg0, _arg1, _arg2, _arg3)
}

func Sky_strings_ReplaceAll(arg0 any, arg1 any, arg2 any) string {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	_arg2 := arg2.(string)
	return strings.ReplaceAll(_arg0, _arg1, _arg2)
}

func Sky_strings_Split(arg0 any, arg1 any) []string {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return strings.Split(_arg0, _arg1)
}

func Sky_strings_SplitAfter(arg0 any, arg1 any) []string {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return strings.SplitAfter(_arg0, _arg1)
}

func Sky_strings_SplitAfterN(arg0 any, arg1 any, arg2 any) []string {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	_arg2 := arg2.(int)
	return strings.SplitAfterN(_arg0, _arg1, _arg2)
}

func Sky_strings_SplitN(arg0 any, arg1 any, arg2 any) []string {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	_arg2 := arg2.(int)
	return strings.SplitN(_arg0, _arg1, _arg2)
}

func Sky_strings_Title(arg0 any) string {
	_arg0 := arg0.(string)
	return strings.Title(_arg0)
}

func Sky_strings_ToLower(arg0 any) string {
	_arg0 := arg0.(string)
	return strings.ToLower(_arg0)
}

func Sky_strings_ToLowerSpecial(arg0 any, arg1 any) string {
	_arg0 := arg0.(unicode.SpecialCase)
	_arg1 := arg1.(string)
	return strings.ToLowerSpecial(_arg0, _arg1)
}

func Sky_strings_ToTitle(arg0 any) string {
	_arg0 := arg0.(string)
	return strings.ToTitle(_arg0)
}

func Sky_strings_ToTitleSpecial(arg0 any, arg1 any) string {
	_arg0 := arg0.(unicode.SpecialCase)
	_arg1 := arg1.(string)
	return strings.ToTitleSpecial(_arg0, _arg1)
}

func Sky_strings_ToUpper(arg0 any) string {
	_arg0 := arg0.(string)
	return strings.ToUpper(_arg0)
}

func Sky_strings_ToUpperSpecial(arg0 any, arg1 any) string {
	_arg0 := arg0.(unicode.SpecialCase)
	_arg1 := arg1.(string)
	return strings.ToUpperSpecial(_arg0, _arg1)
}

func Sky_strings_ToValidUTF8(arg0 any, arg1 any) string {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return strings.ToValidUTF8(_arg0, _arg1)
}

func Sky_strings_Trim(arg0 any, arg1 any) string {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return strings.Trim(_arg0, _arg1)
}

func Sky_strings_TrimFunc(arg0 any, arg1 any) string {
	_arg0 := arg0.(string)
	_skyFn1 := arg1.(func(any) any)
	_arg1 := func(p0 rune) bool {
		return _skyFn1(p0).(bool)
	}
	return strings.TrimFunc(_arg0, _arg1)
}

func Sky_strings_TrimLeft(arg0 any, arg1 any) string {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return strings.TrimLeft(_arg0, _arg1)
}

func Sky_strings_TrimLeftFunc(arg0 any, arg1 any) string {
	_arg0 := arg0.(string)
	_skyFn1 := arg1.(func(any) any)
	_arg1 := func(p0 rune) bool {
		return _skyFn1(p0).(bool)
	}
	return strings.TrimLeftFunc(_arg0, _arg1)
}

func Sky_strings_TrimPrefix(arg0 any, arg1 any) string {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return strings.TrimPrefix(_arg0, _arg1)
}

func Sky_strings_TrimRight(arg0 any, arg1 any) string {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return strings.TrimRight(_arg0, _arg1)
}

func Sky_strings_TrimRightFunc(arg0 any, arg1 any) string {
	_arg0 := arg0.(string)
	_skyFn1 := arg1.(func(any) any)
	_arg1 := func(p0 rune) bool {
		return _skyFn1(p0).(bool)
	}
	return strings.TrimRightFunc(_arg0, _arg1)
}

func Sky_strings_TrimSpace(arg0 any) string {
	_arg0 := arg0.(string)
	return strings.TrimSpace(_arg0)
}

func Sky_strings_TrimSuffix(arg0 any, arg1 any) string {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return strings.TrimSuffix(_arg0, _arg1)
}

func Sky_strings_BuilderCap(this any) int {
	var _this *strings.Builder
	if _p, ok := this.(*strings.Builder); ok { _this = _p } else { _v := this.(strings.Builder); _this = &_v }

	return _this.Cap()
}

func Sky_strings_BuilderGrow(this any, arg0 any) any {
	var _this *strings.Builder
	if _p, ok := this.(*strings.Builder); ok { _this = _p } else { _v := this.(strings.Builder); _this = &_v }
	_arg0 := arg0.(int)
	_this.Grow(_arg0)
	return struct{}{}
}

func Sky_strings_BuilderLen(this any) int {
	var _this *strings.Builder
	if _p, ok := this.(*strings.Builder); ok { _this = _p } else { _v := this.(strings.Builder); _this = &_v }

	return _this.Len()
}

func Sky_strings_BuilderReset(this any) any {
	var _this *strings.Builder
	if _p, ok := this.(*strings.Builder); ok { _this = _p } else { _v := this.(strings.Builder); _this = &_v }

	_this.Reset()
	return struct{}{}
}

func Sky_strings_BuilderString(this any) string {
	var _this *strings.Builder
	if _p, ok := this.(*strings.Builder); ok { _this = _p } else { _v := this.(strings.Builder); _this = &_v }

	return _this.String()
}

func Sky_strings_BuilderWrite(this any, arg0 any) SkyResult {
	var _this *strings.Builder
	if _p, ok := this.(*strings.Builder); ok { _this = _p } else { _v := this.(strings.Builder); _this = &_v }
	_arg0 := arg0.([]byte)
	res, err := _this.Write(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_strings_BuilderWriteByte(this any, arg0 any) SkyResult {
	var _this *strings.Builder
	if _p, ok := this.(*strings.Builder); ok { _this = _p } else { _v := this.(strings.Builder); _this = &_v }
	_arg0 := arg0.(byte)
	err := _this.WriteByte(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_strings_BuilderWriteRune(this any, arg0 any) SkyResult {
	var _this *strings.Builder
	if _p, ok := this.(*strings.Builder); ok { _this = _p } else { _v := this.(strings.Builder); _this = &_v }
	_arg0 := arg0.(rune)
	res, err := _this.WriteRune(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_strings_BuilderWriteString(this any, arg0 any) SkyResult {
	var _this *strings.Builder
	if _p, ok := this.(*strings.Builder); ok { _this = _p } else { _v := this.(strings.Builder); _this = &_v }
	_arg0 := arg0.(string)
	res, err := _this.WriteString(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_strings_ReaderLen(this any) int {
	var _this *strings.Reader
	if _p, ok := this.(*strings.Reader); ok { _this = _p } else { _v := this.(strings.Reader); _this = &_v }

	return _this.Len()
}

func Sky_strings_ReaderRead(this any, arg0 any) SkyResult {
	var _this *strings.Reader
	if _p, ok := this.(*strings.Reader); ok { _this = _p } else { _v := this.(strings.Reader); _this = &_v }
	_arg0 := arg0.([]byte)
	res, err := _this.Read(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_strings_ReaderReadAt(this any, arg0 any, arg1 any) SkyResult {
	var _this *strings.Reader
	if _p, ok := this.(*strings.Reader); ok { _this = _p } else { _v := this.(strings.Reader); _this = &_v }
	_arg0 := arg0.([]byte)
	_arg1 := arg1.(int64)
	res, err := _this.ReadAt(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_strings_ReaderReadByte(this any) SkyResult {
	var _this *strings.Reader
	if _p, ok := this.(*strings.Reader); ok { _this = _p } else { _v := this.(strings.Reader); _this = &_v }

	res, err := _this.ReadByte()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_strings_ReaderReadRune(this any) SkyResult {
	var _this *strings.Reader
	if _p, ok := this.(*strings.Reader); ok { _this = _p } else { _v := this.(strings.Reader); _this = &_v }

	_r0, _r1, err := _this.ReadRune()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(SkyTuple2{V0: _r0, V1: _r1})
}

func Sky_strings_ReaderReset(this any, arg0 any) any {
	var _this *strings.Reader
	if _p, ok := this.(*strings.Reader); ok { _this = _p } else { _v := this.(strings.Reader); _this = &_v }
	_arg0 := arg0.(string)
	_this.Reset(_arg0)
	return struct{}{}
}

func Sky_strings_ReaderSeek(this any, arg0 any, arg1 any) SkyResult {
	var _this *strings.Reader
	if _p, ok := this.(*strings.Reader); ok { _this = _p } else { _v := this.(strings.Reader); _this = &_v }
	_arg0 := arg0.(int64)
	_arg1 := arg1.(int)
	res, err := _this.Seek(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_strings_ReaderSize(this any) int64 {
	var _this *strings.Reader
	if _p, ok := this.(*strings.Reader); ok { _this = _p } else { _v := this.(strings.Reader); _this = &_v }

	return _this.Size()
}

func Sky_strings_ReaderUnreadByte(this any) SkyResult {
	var _this *strings.Reader
	if _p, ok := this.(*strings.Reader); ok { _this = _p } else { _v := this.(strings.Reader); _this = &_v }

	err := _this.UnreadByte()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_strings_ReaderUnreadRune(this any) SkyResult {
	var _this *strings.Reader
	if _p, ok := this.(*strings.Reader); ok { _this = _p } else { _v := this.(strings.Reader); _this = &_v }

	err := _this.UnreadRune()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_strings_ReaderWriteTo(this any, arg0 any) SkyResult {
	var _this *strings.Reader
	if _p, ok := this.(*strings.Reader); ok { _this = _p } else { _v := this.(strings.Reader); _this = &_v }
	_arg0 := arg0.(io.Writer)
	res, err := _this.WriteTo(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_strings_ReplacerReplace(this any, arg0 any) string {
	var _this *strings.Replacer
	if _p, ok := this.(*strings.Replacer); ok { _this = _p } else { _v := this.(strings.Replacer); _this = &_v }
	_arg0 := arg0.(string)
	return _this.Replace(_arg0)
}

func Sky_strings_ReplacerWriteString(this any, arg0 any, arg1 any) SkyResult {
	var _this *strings.Replacer
	if _p, ok := this.(*strings.Replacer); ok { _this = _p } else { _v := this.(strings.Replacer); _this = &_v }
	_arg0 := arg0.(io.Writer)
	_arg1 := arg1.(string)
	res, err := _this.WriteString(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

