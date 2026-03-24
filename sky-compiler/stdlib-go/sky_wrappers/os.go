package sky_wrappers

import (
	"os"
	"time"
	"io/fs"
	"io"
	"syscall"
)

func Sky_os_Chdir(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	err := os.Chdir(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_Chmod(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(os.FileMode)
	err := os.Chmod(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_Chown(arg0 any, arg1 any, arg2 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(int)
	_arg2 := arg2.(int)
	err := os.Chown(_arg0, _arg1, _arg2)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_Chtimes(arg0 any, arg1 any, arg2 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(time.Time)
	_arg2 := arg2.(time.Time)
	err := os.Chtimes(_arg0, _arg1, _arg2)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_Clearenv() any {
	os.Clearenv()
	return struct{}{}
}

func Sky_os_CopyFS(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(fs.FS)
	err := os.CopyFS(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_Create(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := os.Create(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_CreateTemp(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	res, err := os.CreateTemp(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_DirFS(arg0 any) fs.FS {
	_arg0 := arg0.(string)
	return os.DirFS(_arg0)
}

func Sky_os_Environ() []string {
	return os.Environ()
}

func Sky_os_Executable() SkyResult {
	res, err := os.Executable()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_Exit(arg0 any) any {
	_arg0 := arg0.(int)
	os.Exit(_arg0)
	return struct{}{}
}

func Sky_os_Expand(arg0 any, arg1 any) string {
	_arg0 := arg0.(string)
	_skyFn1 := sky_asFunc(arg1)
	_arg1 := func(p0 string) string {
		return _skyFn1(p0).(string)
	}
	return os.Expand(_arg0, _arg1)
}

func Sky_os_ExpandEnv(arg0 any) string {
	_arg0 := arg0.(string)
	return os.ExpandEnv(_arg0)
}

func Sky_os_FindProcess(arg0 any) SkyResult {
	_arg0 := arg0.(int)
	res, err := os.FindProcess(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_Getegid() int {
	return os.Getegid()
}

func Sky_os_Getenv(arg0 any) string {
	_arg0 := arg0.(string)
	return os.Getenv(_arg0)
}

func Sky_os_Geteuid() int {
	return os.Geteuid()
}

func Sky_os_Getgid() int {
	return os.Getgid()
}

func Sky_os_Getgroups() SkyResult {
	res, err := os.Getgroups()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_Getpagesize() int {
	return os.Getpagesize()
}

func Sky_os_Getpid() int {
	return os.Getpid()
}

func Sky_os_Getppid() int {
	return os.Getppid()
}

func Sky_os_Getuid() int {
	return os.Getuid()
}

func Sky_os_Getwd() SkyResult {
	res, err := os.Getwd()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_Hostname() SkyResult {
	res, err := os.Hostname()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_IsExist(arg0 any) bool {
	_arg0 := arg0.(error)
	return os.IsExist(_arg0)
}

func Sky_os_IsNotExist(arg0 any) bool {
	_arg0 := arg0.(error)
	return os.IsNotExist(_arg0)
}

func Sky_os_IsPathSeparator(arg0 any) bool {
	_arg0 := arg0.(uint8)
	return os.IsPathSeparator(_arg0)
}

func Sky_os_IsPermission(arg0 any) bool {
	_arg0 := arg0.(error)
	return os.IsPermission(_arg0)
}

func Sky_os_IsTimeout(arg0 any) bool {
	_arg0 := arg0.(error)
	return os.IsTimeout(_arg0)
}

func Sky_os_Lchown(arg0 any, arg1 any, arg2 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(int)
	_arg2 := arg2.(int)
	err := os.Lchown(_arg0, _arg1, _arg2)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_Link(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	err := os.Link(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_LookupEnv(arg0 any) any {
	_arg0 := arg0.(string)
	_val, _ok := os.LookupEnv(_arg0)
	if !_ok {
		return SkyNothing()
	}
	return SkyJust(_val)
}

func Sky_os_Lstat(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := os.Lstat(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_Mkdir(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(os.FileMode)
	err := os.Mkdir(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_MkdirAll(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(os.FileMode)
	err := os.MkdirAll(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_MkdirTemp(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	res, err := os.MkdirTemp(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_NewFile(arg0 any, arg1 any) *os.File {
	_arg0 := arg0.(uintptr)
	_arg1 := arg1.(string)
	return os.NewFile(_arg0, _arg1)
}

func Sky_os_NewSyscallError(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(error)
	err := os.NewSyscallError(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_Open(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := os.Open(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_OpenFile(arg0 any, arg1 any, arg2 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(int)
	_arg2 := arg2.(os.FileMode)
	res, err := os.OpenFile(_arg0, _arg1, _arg2)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_OpenInRoot(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	res, err := os.OpenInRoot(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_OpenRoot(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := os.OpenRoot(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_Pipe() SkyResult {
	_r0, _r1, err := os.Pipe()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(Tuple2{V0: _r0, V1: _r1})
}

func Sky_os_ReadDir(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := os.ReadDir(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_ReadFile(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := os.ReadFile(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_Readlink(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := os.Readlink(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_Remove(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	err := os.Remove(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_RemoveAll(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	err := os.RemoveAll(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_Rename(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	err := os.Rename(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_SameFile(arg0 any, arg1 any) bool {
	_arg0 := arg0.(os.FileInfo)
	_arg1 := arg1.(os.FileInfo)
	return os.SameFile(_arg0, _arg1)
}

func Sky_os_Setenv(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	err := os.Setenv(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_StartProcess(arg0 any, arg1 any, arg2 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.([]string)
	var _arg2 *os.ProcAttr
	if arg2 != nil && arg2 != "nil" { _arg2 = arg2.(*os.ProcAttr) }
	res, err := os.StartProcess(_arg0, _arg1, _arg2)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_Stat(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := os.Stat(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_Symlink(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	err := os.Symlink(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_TempDir() string {
	return os.TempDir()
}

func Sky_os_Truncate(arg0 any, arg1 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.(int64)
	err := os.Truncate(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_Unsetenv(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	err := os.Unsetenv(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_UserCacheDir() SkyResult {
	res, err := os.UserCacheDir()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_UserConfigDir() SkyResult {
	res, err := os.UserConfigDir()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_UserHomeDir() SkyResult {
	res, err := os.UserHomeDir()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_WriteFile(arg0 any, arg1 any, arg2 any) SkyResult {
	_arg0 := arg0.(string)
	_arg1 := arg1.([]byte)
	_arg2 := arg2.(os.FileMode)
	err := os.WriteFile(_arg0, _arg1, _arg2)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_Args() any {
	_val := os.Args
	_result := make([]any, len(_val))
	for _i, _v := range _val { _result[_i] = _v }
	return _result
}

func Sky_os_ErrClosed() any {
	return os.ErrClosed
}

func Sky_os_ErrDeadlineExceeded() any {
	return os.ErrDeadlineExceeded
}

func Sky_os_ErrExist() any {
	return os.ErrExist
}

func Sky_os_ErrInvalid() any {
	return os.ErrInvalid
}

func Sky_os_ErrNoDeadline() any {
	return os.ErrNoDeadline
}

func Sky_os_ErrNoHandle() any {
	return os.ErrNoHandle
}

func Sky_os_ErrNotExist() any {
	return os.ErrNotExist
}

func Sky_os_ErrPermission() any {
	return os.ErrPermission
}

func Sky_os_ErrProcessDone() any {
	return os.ErrProcessDone
}

func Sky_os_Interrupt() any {
	return os.Interrupt
}

func Sky_os_Kill() any {
	return os.Kill
}

func Sky_os_Stderr() any {
	return os.Stderr
}

func Sky_os_Stdin() any {
	return os.Stdin
}

func Sky_os_Stdout() any {
	return os.Stdout
}

func Sky_os_DevNull() any {
	return os.DevNull
}

func Sky_os_ModeAppend() any {
	return os.ModeAppend
}

func Sky_os_ModeCharDevice() any {
	return os.ModeCharDevice
}

func Sky_os_ModeDevice() any {
	return os.ModeDevice
}

func Sky_os_ModeDir() any {
	return os.ModeDir
}

func Sky_os_ModeExclusive() any {
	return os.ModeExclusive
}

func Sky_os_ModeIrregular() any {
	return os.ModeIrregular
}

func Sky_os_ModeNamedPipe() any {
	return os.ModeNamedPipe
}

func Sky_os_ModePerm() any {
	return os.ModePerm
}

func Sky_os_ModeSetgid() any {
	return os.ModeSetgid
}

func Sky_os_ModeSetuid() any {
	return os.ModeSetuid
}

func Sky_os_ModeSocket() any {
	return os.ModeSocket
}

func Sky_os_ModeSticky() any {
	return os.ModeSticky
}

func Sky_os_ModeSymlink() any {
	return os.ModeSymlink
}

func Sky_os_ModeTemporary() any {
	return os.ModeTemporary
}

func Sky_os_ModeType() any {
	return os.ModeType
}

func Sky_os_O_APPEND() any {
	return os.O_APPEND
}

func Sky_os_O_CREATE() any {
	return os.O_CREATE
}

func Sky_os_O_EXCL() any {
	return os.O_EXCL
}

func Sky_os_O_RDONLY() any {
	return os.O_RDONLY
}

func Sky_os_O_RDWR() any {
	return os.O_RDWR
}

func Sky_os_O_SYNC() any {
	return os.O_SYNC
}

func Sky_os_O_TRUNC() any {
	return os.O_TRUNC
}

func Sky_os_O_WRONLY() any {
	return os.O_WRONLY
}

func Sky_os_PathListSeparator() any {
	return os.PathListSeparator
}

func Sky_os_PathSeparator() any {
	return os.PathSeparator
}

func Sky_os_SEEK_CUR() any {
	return os.SEEK_CUR
}

func Sky_os_SEEK_END() any {
	return os.SEEK_END
}

func Sky_os_SEEK_SET() any {
	return os.SEEK_SET
}

func Sky_os_FileChdir(this any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }

	err := _this.Chdir()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_FileChmod(this any, arg0 any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }
	_arg0 := arg0.(os.FileMode)
	err := _this.Chmod(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_FileChown(this any, arg0 any, arg1 any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }
	_arg0 := arg0.(int)
	_arg1 := arg1.(int)
	err := _this.Chown(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_FileClose(this any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }

	err := _this.Close()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_FileFd(this any) uintptr {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }

	return _this.Fd()
}

func Sky_os_FileName(this any) string {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }

	return _this.Name()
}

func Sky_os_FileRead(this any, arg0 any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }
	_arg0 := arg0.([]byte)
	res, err := _this.Read(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_FileReadAt(this any, arg0 any, arg1 any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }
	_arg0 := arg0.([]byte)
	_arg1 := arg1.(int64)
	res, err := _this.ReadAt(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_FileReadDir(this any, arg0 any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }
	_arg0 := arg0.(int)
	res, err := _this.ReadDir(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_FileReadFrom(this any, arg0 any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }
	_arg0 := arg0.(io.Reader)
	res, err := _this.ReadFrom(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_FileReaddir(this any, arg0 any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }
	_arg0 := arg0.(int)
	res, err := _this.Readdir(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_FileReaddirnames(this any, arg0 any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }
	_arg0 := arg0.(int)
	res, err := _this.Readdirnames(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_FileSeek(this any, arg0 any, arg1 any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }
	_arg0 := arg0.(int64)
	_arg1 := arg1.(int)
	res, err := _this.Seek(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_FileSetDeadline(this any, arg0 any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }
	_arg0 := arg0.(time.Time)
	err := _this.SetDeadline(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_FileSetReadDeadline(this any, arg0 any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }
	_arg0 := arg0.(time.Time)
	err := _this.SetReadDeadline(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_FileSetWriteDeadline(this any, arg0 any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }
	_arg0 := arg0.(time.Time)
	err := _this.SetWriteDeadline(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_FileStat(this any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }

	res, err := _this.Stat()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_FileSync(this any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }

	err := _this.Sync()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_FileSyscallConn(this any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }

	res, err := _this.SyscallConn()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_FileTruncate(this any, arg0 any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }
	_arg0 := arg0.(int64)
	err := _this.Truncate(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_FileWrite(this any, arg0 any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }
	_arg0 := arg0.([]byte)
	res, err := _this.Write(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_FileWriteAt(this any, arg0 any, arg1 any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }
	_arg0 := arg0.([]byte)
	_arg1 := arg1.(int64)
	res, err := _this.WriteAt(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_FileWriteString(this any, arg0 any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }
	_arg0 := arg0.(string)
	res, err := _this.WriteString(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_FileWriteTo(this any, arg0 any) SkyResult {
	var _this *os.File
	if _p, ok := this.(*os.File); ok { _this = _p } else { _v := this.(os.File); _this = &_v }
	_arg0 := arg0.(io.Writer)
	res, err := _this.WriteTo(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_LinkErrorError(this any) string {
	var _this *os.LinkError
	if _p, ok := this.(*os.LinkError); ok { _this = _p } else { _v := this.(os.LinkError); _this = &_v }

	return _this.Error()
}

func Sky_os_LinkErrorUnwrap(this any) SkyResult {
	var _this *os.LinkError
	if _p, ok := this.(*os.LinkError); ok { _this = _p } else { _v := this.(os.LinkError); _this = &_v }

	err := _this.Unwrap()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_LinkErrorOp(this any) string {
	var _this *os.LinkError
	if _p, ok := this.(*os.LinkError); ok { _this = _p } else { _v := this.(os.LinkError); _this = &_v }

	return _this.Op
}

func Sky_os_LinkErrorOld(this any) string {
	var _this *os.LinkError
	if _p, ok := this.(*os.LinkError); ok { _this = _p } else { _v := this.(os.LinkError); _this = &_v }

	return _this.Old
}

func Sky_os_LinkErrorNew(this any) string {
	var _this *os.LinkError
	if _p, ok := this.(*os.LinkError); ok { _this = _p } else { _v := this.(os.LinkError); _this = &_v }

	return _this.New
}

func Sky_os_LinkErrorErr(this any) error {
	var _this *os.LinkError
	if _p, ok := this.(*os.LinkError); ok { _this = _p } else { _v := this.(os.LinkError); _this = &_v }

	return _this.Err
}

func Sky_os_ProcAttrDir(this any) string {
	var _this *os.ProcAttr
	if _p, ok := this.(*os.ProcAttr); ok { _this = _p } else { _v := this.(os.ProcAttr); _this = &_v }

	return _this.Dir
}

func Sky_os_ProcAttrEnv(this any) any {
	var _this *os.ProcAttr
	if _p, ok := this.(*os.ProcAttr); ok { _this = _p } else { _v := this.(os.ProcAttr); _this = &_v }

	_val := _this.Env
	_result := make([]any, len(_val))
	for _i, _v := range _val { _result[_i] = _v }
	return _result
}

func Sky_os_ProcAttrFiles(this any) any {
	var _this *os.ProcAttr
	if _p, ok := this.(*os.ProcAttr); ok { _this = _p } else { _v := this.(os.ProcAttr); _this = &_v }

	_val := _this.Files
	_result := make([]any, len(_val))
	for _i, _v := range _val { _result[_i] = _v }
	return _result
}

func Sky_os_ProcAttrSys(this any) *syscall.SysProcAttr {
	var _this *os.ProcAttr
	if _p, ok := this.(*os.ProcAttr); ok { _this = _p } else { _v := this.(os.ProcAttr); _this = &_v }

	return _this.Sys
}

func Sky_os_ProcessKill(this any) SkyResult {
	var _this *os.Process
	if _p, ok := this.(*os.Process); ok { _this = _p } else { _v := this.(os.Process); _this = &_v }

	err := _this.Kill()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_ProcessRelease(this any) SkyResult {
	var _this *os.Process
	if _p, ok := this.(*os.Process); ok { _this = _p } else { _v := this.(os.Process); _this = &_v }

	err := _this.Release()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_ProcessSignal(this any, arg0 any) SkyResult {
	var _this *os.Process
	if _p, ok := this.(*os.Process); ok { _this = _p } else { _v := this.(os.Process); _this = &_v }
	_arg0 := arg0.(os.Signal)
	err := _this.Signal(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_ProcessWait(this any) SkyResult {
	var _this *os.Process
	if _p, ok := this.(*os.Process); ok { _this = _p } else { _v := this.(os.Process); _this = &_v }

	res, err := _this.Wait()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_ProcessWithHandle(this any, arg0 any) SkyResult {
	var _this *os.Process
	if _p, ok := this.(*os.Process); ok { _this = _p } else { _v := this.(os.Process); _this = &_v }
	_skyFn0 := sky_asFunc(arg0)
	_arg0 := func(p0 uintptr) {
		_skyFn0(p0)
	}
	err := _this.WithHandle(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_ProcessPid(this any) int {
	var _this *os.Process
	if _p, ok := this.(*os.Process); ok { _this = _p } else { _v := this.(os.Process); _this = &_v }

	return _this.Pid
}

func Sky_os_ProcessStateExitCode(this any) int {
	var _this *os.ProcessState
	if _p, ok := this.(*os.ProcessState); ok { _this = _p } else { _v := this.(os.ProcessState); _this = &_v }

	return _this.ExitCode()
}

func Sky_os_ProcessStateExited(this any) bool {
	var _this *os.ProcessState
	if _p, ok := this.(*os.ProcessState); ok { _this = _p } else { _v := this.(os.ProcessState); _this = &_v }

	return _this.Exited()
}

func Sky_os_ProcessStatePid(this any) int {
	var _this *os.ProcessState
	if _p, ok := this.(*os.ProcessState); ok { _this = _p } else { _v := this.(os.ProcessState); _this = &_v }

	return _this.Pid()
}

func Sky_os_ProcessStateString(this any) string {
	var _this *os.ProcessState
	if _p, ok := this.(*os.ProcessState); ok { _this = _p } else { _v := this.(os.ProcessState); _this = &_v }

	return _this.String()
}

func Sky_os_ProcessStateSuccess(this any) bool {
	var _this *os.ProcessState
	if _p, ok := this.(*os.ProcessState); ok { _this = _p } else { _v := this.(os.ProcessState); _this = &_v }

	return _this.Success()
}

func Sky_os_ProcessStateSys(this any) any {
	var _this *os.ProcessState
	if _p, ok := this.(*os.ProcessState); ok { _this = _p } else { _v := this.(os.ProcessState); _this = &_v }

	return _this.Sys()
}

func Sky_os_ProcessStateSysUsage(this any) any {
	var _this *os.ProcessState
	if _p, ok := this.(*os.ProcessState); ok { _this = _p } else { _v := this.(os.ProcessState); _this = &_v }

	return _this.SysUsage()
}

func Sky_os_ProcessStateSystemTime(this any) time.Duration {
	var _this *os.ProcessState
	if _p, ok := this.(*os.ProcessState); ok { _this = _p } else { _v := this.(os.ProcessState); _this = &_v }

	return _this.SystemTime()
}

func Sky_os_ProcessStateUserTime(this any) time.Duration {
	var _this *os.ProcessState
	if _p, ok := this.(*os.ProcessState); ok { _this = _p } else { _v := this.(os.ProcessState); _this = &_v }

	return _this.UserTime()
}

func Sky_os_RootChmod(this any, arg0 any, arg1 any) SkyResult {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }
	_arg0 := arg0.(string)
	_arg1 := arg1.(os.FileMode)
	err := _this.Chmod(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_RootChown(this any, arg0 any, arg1 any, arg2 any) SkyResult {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }
	_arg0 := arg0.(string)
	_arg1 := arg1.(int)
	_arg2 := arg2.(int)
	err := _this.Chown(_arg0, _arg1, _arg2)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_RootChtimes(this any, arg0 any, arg1 any, arg2 any) SkyResult {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }
	_arg0 := arg0.(string)
	_arg1 := arg1.(time.Time)
	_arg2 := arg2.(time.Time)
	err := _this.Chtimes(_arg0, _arg1, _arg2)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_RootClose(this any) SkyResult {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }

	err := _this.Close()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_RootCreate(this any, arg0 any) SkyResult {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }
	_arg0 := arg0.(string)
	res, err := _this.Create(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_RootFS(this any) fs.FS {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }

	return _this.FS()
}

func Sky_os_RootLchown(this any, arg0 any, arg1 any, arg2 any) SkyResult {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }
	_arg0 := arg0.(string)
	_arg1 := arg1.(int)
	_arg2 := arg2.(int)
	err := _this.Lchown(_arg0, _arg1, _arg2)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_RootLink(this any, arg0 any, arg1 any) SkyResult {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	err := _this.Link(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_RootLstat(this any, arg0 any) SkyResult {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }
	_arg0 := arg0.(string)
	res, err := _this.Lstat(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_RootMkdir(this any, arg0 any, arg1 any) SkyResult {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }
	_arg0 := arg0.(string)
	_arg1 := arg1.(os.FileMode)
	err := _this.Mkdir(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_RootMkdirAll(this any, arg0 any, arg1 any) SkyResult {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }
	_arg0 := arg0.(string)
	_arg1 := arg1.(os.FileMode)
	err := _this.MkdirAll(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_RootName(this any) string {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }

	return _this.Name()
}

func Sky_os_RootOpen(this any, arg0 any) SkyResult {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }
	_arg0 := arg0.(string)
	res, err := _this.Open(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_RootOpenFile(this any, arg0 any, arg1 any, arg2 any) SkyResult {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }
	_arg0 := arg0.(string)
	_arg1 := arg1.(int)
	_arg2 := arg2.(os.FileMode)
	res, err := _this.OpenFile(_arg0, _arg1, _arg2)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_RootOpenRoot(this any, arg0 any) SkyResult {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }
	_arg0 := arg0.(string)
	res, err := _this.OpenRoot(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_RootReadFile(this any, arg0 any) SkyResult {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }
	_arg0 := arg0.(string)
	res, err := _this.ReadFile(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_RootReadlink(this any, arg0 any) SkyResult {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }
	_arg0 := arg0.(string)
	res, err := _this.Readlink(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_RootRemove(this any, arg0 any) SkyResult {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }
	_arg0 := arg0.(string)
	err := _this.Remove(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_RootRemoveAll(this any, arg0 any) SkyResult {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }
	_arg0 := arg0.(string)
	err := _this.RemoveAll(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_RootRename(this any, arg0 any, arg1 any) SkyResult {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	err := _this.Rename(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_RootStat(this any, arg0 any) SkyResult {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }
	_arg0 := arg0.(string)
	res, err := _this.Stat(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_RootSymlink(this any, arg0 any, arg1 any) SkyResult {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }
	_arg0 := arg0.(string)
	_arg1 := arg1.(string)
	err := _this.Symlink(_arg0, _arg1)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_RootWriteFile(this any, arg0 any, arg1 any, arg2 any) SkyResult {
	var _this *os.Root
	if _p, ok := this.(*os.Root); ok { _this = _p } else { _v := this.(os.Root); _this = &_v }
	_arg0 := arg0.(string)
	_arg1 := arg1.([]byte)
	_arg2 := arg2.(os.FileMode)
	err := _this.WriteFile(_arg0, _arg1, _arg2)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_SignalSignal(this any) any {
	_this := this.(os.Signal)

	_this.Signal()
	return struct{}{}
}

func Sky_os_SignalString(this any) string {
	_this := this.(os.Signal)

	return _this.String()
}

func Sky_os_SyscallErrorError(this any) string {
	var _this *os.SyscallError
	if _p, ok := this.(*os.SyscallError); ok { _this = _p } else { _v := this.(os.SyscallError); _this = &_v }

	return _this.Error()
}

func Sky_os_SyscallErrorTimeout(this any) bool {
	var _this *os.SyscallError
	if _p, ok := this.(*os.SyscallError); ok { _this = _p } else { _v := this.(os.SyscallError); _this = &_v }

	return _this.Timeout()
}

func Sky_os_SyscallErrorUnwrap(this any) SkyResult {
	var _this *os.SyscallError
	if _p, ok := this.(*os.SyscallError); ok { _this = _p } else { _v := this.(os.SyscallError); _this = &_v }

	err := _this.Unwrap()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_SyscallErrorSyscall(this any) string {
	var _this *os.SyscallError
	if _p, ok := this.(*os.SyscallError); ok { _this = _p } else { _v := this.(os.SyscallError); _this = &_v }

	return _this.Syscall
}

func Sky_os_SyscallErrorErr(this any) error {
	var _this *os.SyscallError
	if _p, ok := this.(*os.SyscallError); ok { _this = _p } else { _v := this.(os.SyscallError); _this = &_v }

	return _this.Err
}

