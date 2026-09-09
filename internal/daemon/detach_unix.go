//go:build !windows

package daemon

import (
	"os/exec"
	"syscall"
)

// detachProcess는 데몬을 새 세션으로 떼어 낸다.
//
// 새 세션에 들어가면 herdr가 훅을 실행한 터미널과의 연결이 끊긴다. 그래야 그 터미널이 닫힐 때
// 데몬이 함께 죽지 않고, 터미널로 가는 신호에도 흔들리지 않는다.
func detachProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
