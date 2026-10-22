// 本文件仅解释 Win32 调用完成与超时，不从窗口返回值推断消息送达。
package ipcexperiment

import "time"

func classifyCall(api, lresult uint64, code uint32, elapsed time.Duration) CallResult {
	r := CallResult{Invoked: true, APIReturn: api, Completed: api != 0, LastError: code, LResult: lresult, LResultValid: api != 0, Timeout: "not_reported", ElapsedMS: elapsed.Milliseconds()}
	if r.Completed {
		r.Timeout = "no"
	}
	if api == 0 && code == 1460 {
		r.Timeout = "yes"
	} // ERROR_TIMEOUT；错误为 0 时不能认定未超时。
	return r
}
