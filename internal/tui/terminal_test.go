package tui

import (
	"os"
	"strings"
	"testing"
)

// raw 모드 자체는 시험하지 않는다. 다만 표준 입출력이 터미널이 아닐 때 Open 이 매달리지 않고 오류를 돌려주는
// 것은 시험할 수 있다. 파이프 뒤에서 화면을 그리려 하면 이 오류가 사용자에게 보여야 한다.
// 둘 다 진짜 터미널이면(go test -c 로 만든 바이너리를 콘솔에서 직접 돌린 경우) 화면이 깜빡이지 않도록
// 건너뛴다. 어느 한쪽만 터미널이면 Open 은 raw 모드에 들어가기 전에 돌아오므로 건너뛸 이유가 없다.
// 건너뛰기의 기준은 Open 이 쓰는 isTerminal 그대로다. termSize 로 보면 윈도우에서는 입력 핸들에
// 늘 실패해 건너뛰지 못하고, os.ModeCharDevice 로 보면 유닉스에서 /dev/null 까지 터미널로 잡혀 늘 건너뛴다.
func TestOpenFailsWhenStdioIsNotATerminal(t *testing.T) {
	if isTerminal(os.Stdin) && isTerminal(os.Stdout) {
		t.Skip("표준 입출력이 모두 터미널이라 건너뛴다")
	}
	term, err := Open()
	if err == nil {
		_ = term.Close()
		t.Fatal("터미널이 아니면 Open 은 오류여야 한다")
	}
	if !strings.Contains(err.Error(), "터미널이 아니다") {
		t.Fatalf("터미널이 아니라는 뜻이 문구에 보여야 한다: %v", err)
	}
}

// Close 는 되돌리기를 쓰기보다 먼저 한다. 표준 출력이 끊긴 파이프면 쓰는 순간 SIGPIPE 로 프로세스가 끝나므로,
// 쓰기가 먼저면 termios 가 raw 모드로 남는다.
//
// 순서를 보는 방법: 되돌리기 안에서 파이프의 읽는 쪽을 닫는다. 쓰기가 그 뒤에 오면 닫힌 파이프에 막혀 오류가
// 나고(표준 출력이 아닌 fd 는 SIGPIPE 대신 EPIPE 를 돌려준다), 쓰기가 먼저였으면 파이프 버퍼에 들어가 오류가 없다.
func TestCloseRestoresBeforeWriting(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	restored := 0
	term := &Terminal{
		out:     w,
		restore: func() error { restored++; return r.Close() },
		done:    make(chan struct{}),
	}
	closeErr := term.Close()
	if restored != 1 {
		t.Fatalf("되돌린 횟수 %d, 기대값 1", restored)
	}
	if closeErr == nil {
		t.Fatal("되돌린 뒤에 쓴 시퀀스는 닫힌 파이프에 막혀 오류여야 한다. 오류가 없으면 쓰기가 되돌리기보다 앞선 것이다")
	}
	// 다시 불러도 되돌리지 않고 처음의 결과를 돌려준다.
	if err := term.Close(); err != closeErr || restored != 1 {
		t.Fatalf("두 번째 Close 는 아무것도 하지 않아야 한다: %v, 되돌린 횟수 %d", err, restored)
	}
}

// Go 로 띄운 고루틴이 패닉하면 패닉이 퍼지기 전에 터미널을 되돌린다. 정상으로 끝나면 되돌리지 않는다.
// 배경 고루틴 하나가 끝났다고 화면이 닫히면 안 된다. 고루틴을 띄우지 않는 본체(guarded)를 직접 불러
// 되돌린 뒤 퍼진 패닉을 바깥에서 받아 본다.
func TestGuardedRestoresTerminalOnPanicOnly(t *testing.T) {
	cases := []struct {
		name         string
		fn           func()
		wantPanic    any
		wantRestored int
	}{
		{"패닉하면 되돌린 뒤 같은 값으로 퍼짐", func() { panic("boom") }, "boom", 1},
		{"정상 종료는 되돌리지 않음", func() {}, nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			restored := 0
			term := &Terminal{out: out, restore: func() error { restored++; return nil }, done: make(chan struct{})}
			got := func() (recovered any) {
				defer func() { recovered = recover() }()
				term.guarded(tc.fn)
				return nil
			}()
			if got != tc.wantPanic {
				t.Fatalf("퍼진 패닉 %v, 기대값 %v", got, tc.wantPanic)
			}
			if restored != tc.wantRestored {
				t.Fatalf("되돌린 횟수 %d, 기대값 %d", restored, tc.wantRestored)
			}
		})
	}
}
