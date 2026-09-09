//go:build windows

package gitrepo

import (
	"os/exec"
	"strconv"
	"syscall"
)

// setProcessGroup은 자식을 새 프로세스 그룹으로 띄운다.
// 이렇게 해야 herdr나 셸이 받은 Ctrl+C가 이 배경 작업까지 흔들지 않고,
// 정리할 때도 이 그룹만 겨냥할 수 있다.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// killProcessGroup은 자식과 그 자손을 정리한다.
//
// 윈도우에는 유닉스의 프로세스 그룹 신호에 해당하는 것이 없다. Process.Kill()은 자식 하나만 끝내므로
// git이 낳은 git-remote-https 같은 도우미가 남는다. 그래서 프로세스 트리를 함께 끝내 주는 taskkill을
// 먼저 쓰고, 그것이 없거나 실패하면 자식만이라도 정리한다.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	kill := exec.Command("taskkill", "/T", "/F", "/PID", pid)
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := kill.Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
