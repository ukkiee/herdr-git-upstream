//go:build windows

package gitrepo

import (
	"errors"
	"os"
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
// 쓰되, 여기서 기다리지 않는다.
//
// 기다리지 않는 것이 중요하다. 이 함수는 cmd.Cancel로 불리는데, Go는 Cancel이 반환한 뒤에야
// WaitDelay 시계를 켠다. 그래서 여기서 막히면 WaitDelay가 아무 보호도 해 주지 못하고, 데몬 루프가
// 그 자리에서 통째로 멈춘다. 시작만 시키고 회수는 다른 고루틴에 맡긴다.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// 이미 회수된 프로세스라면 그 번호를 다른 프로세스가 물려받았을 수 있다. taskkill은 번호만 보고
	// 트리째 끊으므로, 남의 프로세스를 끊지 않도록 여기서 물러난다.
	if err := cmd.Process.Signal(syscall.Signal(0)); errors.Is(err, os.ErrProcessDone) {
		return os.ErrProcessDone
	}

	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := kill.Start(); err != nil {
		// taskkill이 없는 환경이다. 자식 하나라도 정리한다.
		return cmd.Process.Kill()
	}
	// 좀비를 남기지 않도록 회수만 따로 한다. 이 고루틴은 taskkill이 끝나면 사라진다.
	go func() { _ = kill.Wait() }()
	return nil
}
