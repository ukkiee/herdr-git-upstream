//go:build linux

package tui

import "syscall"

// 리눅스는 termios 를 TCGETS/TCSETS 로 읽고 쓴다. TCSETS 는 바로 적용한다(TCSETSW/TCSETSF 는 출력을
// 비우거나 입력을 버린 뒤 적용하는데, 그럴 이유가 없다).
const (
	ioctlReadTermios  = syscall.TCGETS
	ioctlWriteTermios = syscall.TCSETS
)
