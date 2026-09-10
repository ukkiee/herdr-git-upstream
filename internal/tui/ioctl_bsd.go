//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package tui

import "syscall"

// BSD 계열은 termios 를 TIOCGETA/TIOCSETA 로 읽고 쓴다. darwin 도 여기 속한다.
const (
	ioctlReadTermios  = syscall.TIOCGETA
	ioctlWriteTermios = syscall.TIOCSETA
)
