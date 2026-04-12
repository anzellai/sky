package sky_wrappers

import (
	"os/exec"
	"context"
	"io"
	"os"
	"syscall"
	"time"
)

func Sky_os_exec_Command(arg0 any, arg1 any) *exec.Cmd {
	_arg0 := arg0.(string)
	var _arg1 []string
	for _, v := range arg1.([]any) {
		_arg1 = append(_arg1, v.(string))
	}
	return exec.Command(_arg0, _arg1...)
}

func Sky_os_exec_CommandContext(arg0 any, arg1 any, arg2 any) *exec.Cmd {
	_arg0 := arg0.(context.Context)
	_arg1 := arg1.(string)
	var _arg2 []string
	for _, v := range arg2.([]any) {
		_arg2 = append(_arg2, v.(string))
	}
	return exec.CommandContext(_arg0, _arg1, _arg2...)
}

func Sky_os_exec_LookPath(arg0 any) SkyResult {
	_arg0 := arg0.(string)
	res, err := exec.LookPath(_arg0)
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_exec_ErrDot() any {
	return exec.ErrDot
}

func Sky_os_exec_ErrNotFound() any {
	return exec.ErrNotFound
}

func Sky_os_exec_ErrWaitDelay() any {
	return exec.ErrWaitDelay
}

func Sky_os_exec_CmdCombinedOutput(this any) SkyResult {
	_this := this.(*exec.Cmd)

	res, err := _this.CombinedOutput()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_exec_CmdEnviron(this any) []string {
	_this := this.(*exec.Cmd)

	return _this.Environ()
}

func Sky_os_exec_CmdOutput(this any) SkyResult {
	_this := this.(*exec.Cmd)

	res, err := _this.Output()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_exec_CmdRun(this any) SkyResult {
	_this := this.(*exec.Cmd)

	err := _this.Run()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_exec_CmdStart(this any) SkyResult {
	_this := this.(*exec.Cmd)

	err := _this.Start()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_exec_CmdStderrPipe(this any) SkyResult {
	_this := this.(*exec.Cmd)

	res, err := _this.StderrPipe()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_exec_CmdStdinPipe(this any) SkyResult {
	_this := this.(*exec.Cmd)

	res, err := _this.StdinPipe()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_exec_CmdStdoutPipe(this any) SkyResult {
	_this := this.(*exec.Cmd)

	res, err := _this.StdoutPipe()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(res)
}

func Sky_os_exec_CmdString(this any) string {
	_this := this.(*exec.Cmd)

	return _this.String()
}

func Sky_os_exec_CmdWait(this any) SkyResult {
	_this := this.(*exec.Cmd)

	err := _this.Wait()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_exec_CmdPath(this any) string {
	_this := this.(*exec.Cmd)

	return _this.Path
}

func Sky_os_exec_CmdArgs(this any) any {
	_this := this.(*exec.Cmd)

	_val := _this.Args
	_result := make([]any, len(_val))
	for _i, _v := range _val { _result[_i] = _v }
	return _result
}

func Sky_os_exec_CmdEnv(this any) any {
	_this := this.(*exec.Cmd)

	_val := _this.Env
	_result := make([]any, len(_val))
	for _i, _v := range _val { _result[_i] = _v }
	return _result
}

func Sky_os_exec_CmdDir(this any) string {
	_this := this.(*exec.Cmd)

	return _this.Dir
}

func Sky_os_exec_CmdStdin(this any) io.Reader {
	_this := this.(*exec.Cmd)

	return _this.Stdin
}

func Sky_os_exec_CmdStdout(this any) io.Writer {
	_this := this.(*exec.Cmd)

	return _this.Stdout
}

func Sky_os_exec_CmdStderr(this any) io.Writer {
	_this := this.(*exec.Cmd)

	return _this.Stderr
}

func Sky_os_exec_CmdExtraFiles(this any) any {
	_this := this.(*exec.Cmd)

	_val := _this.ExtraFiles
	_result := make([]any, len(_val))
	for _i, _v := range _val { _result[_i] = _v }
	return _result
}

func Sky_os_exec_CmdSysProcAttr(this any) *syscall.SysProcAttr {
	_this := this.(*exec.Cmd)

	return _this.SysProcAttr
}

func Sky_os_exec_CmdProcess(this any) *os.Process {
	_this := this.(*exec.Cmd)

	return _this.Process
}

func Sky_os_exec_CmdProcessState(this any) *os.ProcessState {
	_this := this.(*exec.Cmd)

	return _this.ProcessState
}

func Sky_os_exec_CmdErr(this any) error {
	_this := this.(*exec.Cmd)

	return _this.Err
}

func Sky_os_exec_CmdCancel(this any) func() error {
	_this := this.(*exec.Cmd)

	return _this.Cancel
}

func Sky_os_exec_CmdWaitDelay(this any) time.Duration {
	_this := this.(*exec.Cmd)

	return _this.WaitDelay
}

func Sky_os_exec_ErrorError(this any) string {
	_this := this.(*exec.Error)

	return _this.Error()
}

func Sky_os_exec_ErrorUnwrap(this any) SkyResult {
	_this := this.(*exec.Error)

	err := _this.Unwrap()
	if err != nil {
		return SkyErr(err)
	}
	return SkyOk(struct{}{})
}

func Sky_os_exec_ErrorName(this any) string {
	_this := this.(*exec.Error)

	return _this.Name
}

func Sky_os_exec_ErrorErr(this any) error {
	_this := this.(*exec.Error)

	return _this.Err
}

func Sky_os_exec_ExitErrorError(this any) string {
	_this := this.(*exec.ExitError)

	return _this.Error()
}

func Sky_os_exec_ExitErrorExitCode(this any) int {
	_this := this.(*exec.ExitError)

	return _this.ExitCode()
}

func Sky_os_exec_ExitErrorExited(this any) bool {
	_this := this.(*exec.ExitError)

	return _this.Exited()
}

func Sky_os_exec_ExitErrorPid(this any) int {
	_this := this.(*exec.ExitError)

	return _this.Pid()
}

func Sky_os_exec_ExitErrorString(this any) string {
	_this := this.(*exec.ExitError)

	return _this.String()
}

func Sky_os_exec_ExitErrorSuccess(this any) bool {
	_this := this.(*exec.ExitError)

	return _this.Success()
}

func Sky_os_exec_ExitErrorSys(this any) any {
	_this := this.(*exec.ExitError)

	return _this.Sys()
}

func Sky_os_exec_ExitErrorSysUsage(this any) any {
	_this := this.(*exec.ExitError)

	return _this.SysUsage()
}

func Sky_os_exec_ExitErrorSystemTime(this any) time.Duration {
	_this := this.(*exec.ExitError)

	return _this.SystemTime()
}

func Sky_os_exec_ExitErrorUserTime(this any) time.Duration {
	_this := this.(*exec.ExitError)

	return _this.UserTime()
}

func Sky_os_exec_ExitErrorProcessState(this any) *os.ProcessState {
	_this := this.(*exec.ExitError)

	return _this.ProcessState
}

func Sky_os_exec_ExitErrorStderr(this any) any {
	_this := this.(*exec.ExitError)

	_val := _this.Stderr
	_result := make([]any, len(_val))
	for _i, _v := range _val { _result[_i] = _v }
	return _result
}

