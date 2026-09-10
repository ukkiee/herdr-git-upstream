//go:build darwin || dragonfly || freebsd || netbsd || openbsd || linux

package tui

import (
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

// enterRaw는 표준 입력을 raw 모드로 바꾸고 되돌리는 함수를 돌려준다.
//
// termios 를 읽고 쓰는 ioctl 요청 번호는 계열마다 다르다. BSD 계열(darwin 포함)은 TIOCGETA/TIOCSETA,
// 리눅스는 TCGETS/TCSETS 다. 그 상수는 ioctl_*.go 가 플랫폼별로 정한다. 구조체(syscall.Termios)와
// 플래그 상수는 어디서나 같은 이름이라 여기 한 벌로 둔다.
//
// 이 파일의 빌드 태그는 ioctl_*.go 두 파일의 합집합과 같다. `!windows` 로 두면 그 밖의 유닉스 계열(solaris 등)
// 에서 이 파일은 컴파일되는데 ioctl 상수만 없어 이 파일 한가운데서 깨진다. 태그를 맞춰 두면 그 플랫폼에서는
// 이 파일이 아예 빠져 플랫폼 훅(isTerminal, notifyResize 같은 것)이 없다는 오류가 패키지 경계에서 나고, 어느
// 플랫폼을 지원하는지가 이 한 줄로 드러난다. 플랫폼을 더하려면 ioctl_*.go 와 이 태그를 함께 고친다.
//
// cfmakeraw 와 같은 것을 끄되 출력 쪽(OPOST)은 그대로 둔다. Frame 은 줄마다 커서를 직접 옮기므로 출력
// 후처리가 있어도 상관없고, 켜 두면 오류 문구처럼 화면 밖에서 새어 나온 출력의 줄 바꿈이 망가지지 않는다.
// ISIG 를 끄므로 Ctrl-C 는 신호가 아니라 바이트로 온다. 화면이 그것을 KeyCtrlC 로 받아 닫는다.
func enterRaw(in, _ *os.File) (restore, restoreOutput func() error, err error) {
	fd := in.Fd()
	var saved syscall.Termios
	if err := ioctl(fd, ioctlReadTermios, unsafe.Pointer(&saved)); err != nil {
		return nil, nil, err
	}
	raw := saved
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP |
		syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	// 바이트 하나가 오면 바로 돌려주고, 기다리는 시간 제한은 없다.
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if err := ioctl(fd, ioctlWriteTermios, unsafe.Pointer(&raw)); err != nil {
		return nil, nil, err
	}
	return func() error {
		return ioctl(fd, ioctlWriteTermios, unsafe.Pointer(&saved))
	}, nil, nil
}

// ioctl은 포인터 인자를 받는 ioctl 하나를 부른다.
// uintptr 로의 변환이 Syscall 호출식 안에 있어야 가리키는 것이 호출이 끝날 때까지 살아 있다.
func ioctl(fd, request uintptr, arg unsafe.Pointer) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(arg)); errno != 0 {
		return errno
	}
	return nil
}

// isTerminal은 f 가 터미널인지 답한다. enterRaw 가 termios 를 읽는 바로 그 ioctl 이 되는지로 판단한다.
// 문자 장치인지(os.ModeCharDevice)로 보면 안 된다. /dev/null 도 문자 장치라 go test 아래(표준 입력이
// /dev/null)에서 터미널로 잘못 잡힌다.
func isTerminal(f *os.File) bool {
	var termios syscall.Termios
	return ioctl(f.Fd(), ioctlReadTermios, unsafe.Pointer(&termios)) == nil
}

// termSize는 TIOCGWINSZ 로 창 크기를 읽는다.
func termSize(out *os.File) (cols, rows int, err error) {
	var size struct {
		Row, Col, Xpixel, Ypixel uint16
	}
	if err := ioctl(out.Fd(), syscall.TIOCGWINSZ, unsafe.Pointer(&size)); err != nil {
		return 0, 0, err
	}
	return int(size.Col), int(size.Row), nil
}

// notifyResize는 창 크기 변경 신호를 ch 로 보내고, 그만두는 함수를 돌려준다.
func notifyResize(ch chan<- os.Signal) (stop func()) {
	signal.Notify(ch, syscall.SIGWINCH)
	return func() { signal.Stop(ch) }
}

// terminationSignals는 터미널을 되돌린 뒤 프로세스를 끝내야 하는 신호들이다.
// SIGHUP 은 터미널이 닫혔다는 뜻이라 되돌릴 화면도 없지만, 그래도 termios 는 되돌려 두는 것이 맞다.
func terminationSignals() []os.Signal {
	return []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}
}

// raise는 되돌리기를 마친 뒤 신호를 자기 자신에게 다시 보낸다.
//
// Terminal 의 수신은 이미 끊겼으므로, 다른 곳(예: main 의 signal.NotifyContext)이 같은 신호를 받고
// 있으면 그쪽이 처리하고, 아무도 받지 않으면 기본 동작대로 프로세스가 끝난다. 어느 쪽이든 신호의 뜻은
// 살아남고 종료 코드도 신호를 따른다.
func raise(sig os.Signal) {
	if s, ok := sig.(syscall.Signal); ok {
		if err := syscall.Kill(syscall.Getpid(), s); err == nil {
			return
		}
	}
	os.Exit(1)
}
