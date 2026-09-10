//go:build windows

package tui

import (
	"os"
	"syscall"
	"unsafe"
)

// 윈도우는 termios 대신 콘솔 모드 비트가 있다. kernel32 의 세 함수를 syscall.NewLazyDLL 로 부른다.
// golang.org/x/sys/windows 를 들이지 않기 위해서다. 필요한 것이 셋뿐이다.
//
// 이 파일은 교차 컴파일과 go vet 으로만 확인했고 실제 콘솔에서 실측하지 못했다. README 에 적어 둔다.
var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode             = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode             = kernel32.NewProc("SetConsoleMode")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
)

// 콘솔 모드 비트. 입력과 출력의 비트 이름이 다르므로 나누어 둔다. syscall 패키지는 이 상수를 내보내지 않는다.
const (
	enableProcessedInput       = 0x0001
	enableLineInput            = 0x0002
	enableEchoInput            = 0x0004
	enableVirtualTerminalInput = 0x0200

	enableProcessedOutput           = 0x0001
	enableVirtualTerminalProcessing = 0x0004
)

func getConsoleMode(handle syscall.Handle) (uint32, error) {
	var mode uint32
	if r, _, err := procGetConsoleMode.Call(uintptr(handle), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return 0, err
	}
	return mode, nil
}

func setConsoleMode(handle syscall.Handle, mode uint32) error {
	if r, _, err := procSetConsoleMode.Call(uintptr(handle), uintptr(mode)); r == 0 {
		return err
	}
	return nil
}

// enterRaw는 콘솔의 입력과 출력 모드를 바꾸고 각각 되돌리는 함수를 돌려준다.
//
// 입력에서는 줄 단위 입력, 되울림, 그리고 Ctrl-C 를 신호로 바꾸는 처리(PROCESSED_INPUT)를 끄고, 화살표를
// ESC 시퀀스로 주는 가상 터미널 입력을 켠다. 그래야 유닉스와 같은 바이트가 와서 ParseKeys 하나로 읽는다.
// 출력에서는 ESC 시퀀스를 해석하는 가상 터미널 처리를 켠다. 이것이 없으면 커서 이동 코드가 글자로 찍힌다.
// 표준 입력이 콘솔이 아니면 GetConsoleMode 가 실패하고, 그것이 곧 "터미널이 아니다"는 답이다.
// 출력 복원은 입력과 따로 돌려주어 Terminal.Close 가 화면 종료 시퀀스를 쓸 때까지 VT 처리를 유지하게 한다.
func enterRaw(in, out *os.File) (restore, restoreOutput func() error, err error) {
	inHandle := syscall.Handle(in.Fd())
	outHandle := syscall.Handle(out.Fd())
	inMode, err := getConsoleMode(inHandle)
	if err != nil {
		return nil, nil, err
	}
	outMode, err := getConsoleMode(outHandle)
	if err != nil {
		return nil, nil, err
	}
	rawIn := (inMode &^ (enableEchoInput | enableLineInput | enableProcessedInput)) | enableVirtualTerminalInput
	if err := setConsoleMode(inHandle, rawIn); err != nil {
		return nil, nil, err
	}
	if err := setConsoleMode(outHandle, outMode|enableProcessedOutput|enableVirtualTerminalProcessing); err != nil {
		_ = setConsoleMode(inHandle, inMode)
		return nil, nil, err
	}
	return func() error {
			return setConsoleMode(inHandle, inMode)
		}, func() error {
			return setConsoleMode(outHandle, outMode)
		}, nil
}

// isTerminal은 f 가 콘솔인지 답한다. enterRaw 가 "터미널이 아니다" 를 판단하는 바로 그 호출(GetConsoleMode)을
// 쓴다. termSize 의 GetConsoleScreenBufferInfo 는 화면 버퍼(출력) 핸들에만 답하므로 입력 핸들을 검사하는 데는
// 쓸 수 없다.
func isTerminal(f *os.File) bool {
	_, err := getConsoleMode(syscall.Handle(f.Fd()))
	return err == nil
}

// consoleScreenBufferInfo는 CONSOLE_SCREEN_BUFFER_INFO 와 같은 배치다. 필드 순서와 크기가 그대로여야 한다.
type consoleScreenBufferInfo struct {
	size              coord
	cursorPosition    coord
	attributes        uint16
	window            smallRect
	maximumWindowSize coord
}

type coord struct{ x, y int16 }

type smallRect struct{ left, top, right, bottom int16 }

// termSize는 GetConsoleScreenBufferInfo 로 창 크기를 읽는다. 버퍼 전체가 아니라 보이는 창(srWindow)의 크기다.
func termSize(out *os.File) (cols, rows int, err error) {
	var info consoleScreenBufferInfo
	r, _, callErr := procGetConsoleScreenBufferInfo.Call(uintptr(syscall.Handle(out.Fd())), uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return 0, 0, callErr
	}
	return int(info.window.right-info.window.left) + 1, int(info.window.bottom-info.window.top) + 1, nil
}

// notifyResize는 윈도우에서는 아무것도 하지 않는다. 창 크기 변경 신호가 없으므로 화면이 그릴 때마다 Size 를 다시 본다.
func notifyResize(_ chan<- os.Signal) (stop func()) {
	return func() {}
}

// terminationSignals는 Go 가 윈도우에서 받아 줄 수 있는 종료 신호다. PROCESSED_INPUT 을 껐으므로 Ctrl-C 는
// 여기로 오지 않고 바이트로 온다. 콘솔 창 닫힘·로그오프·시스템 종료(CTRL_CLOSE_EVENT 같은 것)는 Go 가
// SIGTERM 으로 알려 주므로(os/signal 문서의 윈도우 절) 그때도 콘솔 모드를 되돌린다.
func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

// raise는 되돌리기를 마친 뒤 프로세스를 끝낸다. 윈도우에는 신호를 자기에게 다시 보내는 길이 없다.
func raise(_ os.Signal) {
	os.Exit(1)
}
