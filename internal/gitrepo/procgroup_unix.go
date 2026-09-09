//go:build !windows

package gitrepo

import (
	"os/exec"
	"syscall"
	"time"
)

// setProcessGroup은 자식을 자기만의 프로세스 그룹에 넣는다.
// 그래야 제한 시간이 지났을 때 git이 낳은 도우미 프로세스까지 한 번에 정리할 수 있다.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup은 프로세스 그룹 전체를 정리한다.
//
// 먼저 SIGTERM을 보낸다. git이 쓰다 만 잠금 파일을 스스로 치우고 나갈 기회를 주기 위해서다.
// 그래도 남아 있으면 잠시 뒤 SIGKILL로 끝낸다.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	// 음수 pid는 그 절댓값을 프로세스 그룹 번호로 해석하라는 뜻이다.
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		// 그룹을 만들지 못했거나 이미 사라진 경우에는 프로세스 하나만이라도 정리한다.
		return cmd.Process.Kill()
	}
	time.AfterFunc(2*time.Second, func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	})
	return nil
}
