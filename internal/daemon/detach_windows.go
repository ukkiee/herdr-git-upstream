//go:build windows

package daemon

import (
	"os/exec"
	"syscall"
)

// DETACHED_PROCESS는 자식이 부모의 콘솔을 물려받지 않게 한다.
// syscall 패키지가 이 상수를 내보내지 않아 직접 적는다.
const detachedProcess = 0x00000008

// detachProcess는 데몬을 부모의 콘솔과 프로세스 그룹에서 떼어 낸다.
//
// 콘솔을 물려받지 않아야 herdr가 훅을 실행한 창이 닫혀도 데몬이 살아남고,
// 새 프로세스 그룹에 들어가야 그 창으로 가는 Ctrl+C에 함께 죽지 않는다.
func detachProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP
	cmd.SysProcAttr.HideWindow = true
}
