package sky_wrappers

import (
	"path/filepath"
	"io/fs"
)

func Sky_path_filepath_Abs(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := filepath.Abs(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_path_filepath_Base(arg0 any) string {
	_arg0 := arg0.(string)
	return filepath.Base(_arg0)
}

func Sky_path_filepath_Clean(arg0 any) string {
	_arg0 := arg0.(string)
	return filepath.Clean(_arg0)
}

func Sky_path_filepath_Dir(arg0 any) string {
	_arg0 := arg0.(string)
	return filepath.Dir(_arg0)
}

func Sky_path_filepath_EvalSymlinks(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := filepath.EvalSymlinks(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_path_filepath_Ext(arg0 any) string {
	_arg0 := arg0.(string)
	return filepath.Ext(_arg0)
}

func Sky_path_filepath_FromSlash(arg0 any) string {
	_arg0 := arg0.(string)
	return filepath.FromSlash(_arg0)
}

func Sky_path_filepath_Glob(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := filepath.Glob(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_path_filepath_HasPrefix(arg0 any, arg1 any) bool {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	return filepath.HasPrefix(_arg0, _arg1)
}

func Sky_path_filepath_IsAbs(arg0 any) bool {
	_arg0 := arg0.(string)
	return filepath.IsAbs(_arg0)
}

func Sky_path_filepath_IsLocal(arg0 any) bool {
	_arg0 := arg0.(string)
	return filepath.IsLocal(_arg0)
}

func Sky_path_filepath_Join(arg0 any) string {
	var _arg0 []string
	for _, v := range arg0.([]any) {
		_arg0 = append(_arg0, v.(string))
	}
	return filepath.Join(_arg0...)
}

func Sky_path_filepath_Localize(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := filepath.Localize(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_path_filepath_Match(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	res, err := filepath.Match(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_path_filepath_Rel(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	res, err := filepath.Rel(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_path_filepath_Split(arg0 any) (string, string) {
	_arg0 := arg0.(string)
	return filepath.Split(_arg0)
}

func Sky_path_filepath_SplitList(arg0 any) []string {
	_arg0 := arg0.(string)
	return filepath.SplitList(_arg0)
}

func Sky_path_filepath_ToSlash(arg0 any) string {
	_arg0 := arg0.(string)
	return filepath.ToSlash(_arg0)
}

func Sky_path_filepath_VolumeName(arg0 any) string {
	_arg0 := arg0.(string)
	return filepath.VolumeName(_arg0)
}

func Sky_path_filepath_Walk(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(filepath.WalkFunc)
	err := filepath.Walk(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_path_filepath_WalkDir(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(fs.WalkDirFunc)
	err := filepath.WalkDir(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_path_filepath_ErrBadPattern() any {
	return filepath.ErrBadPattern
}

func Sky_path_filepath_SkipAll() any {
	return filepath.SkipAll
}

func Sky_path_filepath_SkipDir() any {
	return filepath.SkipDir
}

func Sky_path_filepath_ListSeparator() any {
	return filepath.ListSeparator
}

func Sky_path_filepath_Separator() any {
	return filepath.Separator
}

