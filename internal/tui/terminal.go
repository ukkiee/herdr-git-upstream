package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
)

// Terminal은 raw 모드로 바꾼 표준 입출력이다. Open 으로 들어가고 Close 로 되돌린다.
type Terminal struct {
	in  *os.File
	out *os.File
	// restore는 raw 모드로 바꾸기 전의 설정으로 되돌린다. 플랫폼 코드가 만든다.
	restore func() error

	closeOnce sync.Once
	closeErr  error
	signals   chan os.Signal
	done      chan struct{}
}

// Open은 raw 모드로 들어가고 대체 화면을 켜고 커서를 숨긴다.
//
// 표준 입력이 터미널이 아니면 오류다. 파이프 뒤에서 화면을 그릴 수는 없고, 그때 raw 모드 흉내를
// 내면 아무 일도 하지 않은 채 매달린다. 표준 출력이 터미널이 아니어도 오류다. 화면을 파이프에 그릴 수
// 없을뿐더러, 그 파이프가 끊겨 있으면(`... | head -1` 처럼 읽는 쪽이 먼저 끝난 경우) 대체 화면 시퀀스를
// 쓰는 순간 SIGPIPE 로 죽어 이미 바꾼 표준 입력의 termios 가 되돌려지지 않은 채 셸로 돌아간다. 윈도우의
// enterRaw 는 출력 핸들의 콘솔 모드도 읽으므로 이 경우를 스스로 거르는데, 유닉스도 같은 문턱을 두어야
// 플랫폼 간 동작이 같다. 대체 화면을 쓰는 이유는 화면을 닫았을 때 사용자의 셸 기록이 그대로 남아야
// 하기 때문이다. herdr 의 팝업 pane 에서는 어차피 pane 이 사라지지만, 터미널에서 직접 불렀을 때는 그
// 차이가 크다.
//
// 종료 신호를 받으면 되돌린 뒤 신호를 다시 던진다. 되돌리지 않고 죽으면 사용자의 셸이 raw 모드에
// 남아 글자가 찍히지 않는다. 다시 던지는 이유는 신호의 뜻(끝내라)을 이 패키지가 삼키지 않기 위해서다.
func Open() (*Terminal, error) {
	t := &Terminal{in: os.Stdin, out: os.Stdout}
	// enterRaw 도 같은 이유로 실패하지만, 그 오류는 ioctl 의 errno 라 사용자에게 뜻이 닿지 않는다. 먼저 가려
	// 또렷한 문구를 낸다. raw 모드에 들어가기 전에 둘 다 확인해야 어느 쪽이 아니어도 되돌릴 것이 없다.
	if !isTerminal(t.in) {
		return nil, errors.New("표준 입력이 터미널이 아니다")
	}
	if !isTerminal(t.out) {
		return nil, errors.New("표준 출력이 터미널이 아니다")
	}
	restore, err := enterRaw(t.in, t.out)
	if err != nil {
		return nil, fmt.Errorf("터미널을 raw 모드로 바꾸지 못했다: %w", err)
	}
	t.restore = restore
	if _, err := t.out.WriteString("\x1b[?1049h\x1b[?25l\x1b[2J\x1b[H"); err != nil {
		_ = restore()
		return nil, fmt.Errorf("대체 화면을 켜지 못했다: %w", err)
	}
	t.watchSignals()
	return t, nil
}

// Close는 raw 모드를 되돌리고 커서를 보이고 대체 화면을 끈다. 여러 번 불러도 한 번만 되돌린다.
//
// 되돌리기가 쓰기보다 먼저다. 표준 출력이 끊긴 파이프면 쓰는 순간 SIGPIPE 로 프로세스가 끝나는데(os/signal
// 문서의 SIGPIPE 절. 이 패키지는 SIGPIPE 를 받지 않으므로 그 규칙 그대로다), 그때 되돌리기가 뒤에 있으면
// 표준 입력의 termios 가 raw 모드로 남는다. 순서를 바꿔도 시퀀스 출력에는 영향이 없다. termios 는 입력
// 처리만 바꾸고 출력 후처리(OPOST)는 처음부터 건드리지 않았기 때문이다.
func (t *Terminal) Close() error {
	t.closeOnce.Do(func() {
		signal.Stop(t.signals)
		close(t.done)
		restoreErr := t.restore()
		// 꾸밈을 전부 끄고 나간다. 반전이나 색이 켜진 채 대체 화면을 끄면 셸 프롬프트가 그 꾸밈을 이어받는다.
		_, writeErr := t.out.WriteString("\x1b[0m\x1b[?25h\x1b[?1049l")
		// 되돌리기 실패가 더 중요한 오류다. 쓰기 실패는 화면이 조금 지저분해질 뿐이다.
		t.closeErr = errors.Join(restoreErr, writeErr)
	})
	return t.closeErr
}

// watchSignals는 종료 신호를 받으면 되돌리고 신호를 다시 던지는 고루틴을 띄운다. Close 가 끝내 준다.
func (t *Terminal) watchSignals() {
	t.signals = make(chan os.Signal, 1)
	t.done = make(chan struct{})
	signal.Notify(t.signals, terminationSignals()...)
	go func() {
		select {
		case sig := <-t.signals:
			_ = t.Close()
			raise(sig)
		case <-t.done:
		}
	}()
}

// Size는 창의 열과 행 수다. 알 수 없으면 80x24 다. 그리기마다 다시 물어도 값이 싸다.
func (t *Terminal) Size() (cols, rows int) {
	cols, rows, err := termSize(t.out)
	if err != nil || cols <= 0 || rows <= 0 {
		return 80, 24
	}
	return cols, rows
}

// Output은 화면을 그릴 곳이다. Frame 에 넘긴다.
//
// 입력 쪽은 내놓지 않는다. 키를 받는 길은 Keys 하나다. 입력을 내놓으면 ReadKeys 에 넘겨 읽는 길이 열리는데,
// 그 길로 띄운 읽기 고루틴이 패닉하면 터미널을 되돌리지 못한다. 시험이 임의의 reader 로 읽을 때는 ReadKeys 를 쓴다.
func (t *Terminal) Output() io.Writer {
	return t.out
}

// Run은 터미널을 열어 fn 을 돌리고 어떤 길로 끝나든 되돌린다.
//
// fn 이 오류를 돌려주면 그 오류를 돌려준다. 패닉은 잡지 않는다. Go 는 지연 함수를 모두 돌린 뒤에 패닉 문구와
// 스택을 찍으므로 defer 만으로도 되돌린 뒤에 스택이 찍히고, 잡았다 다시 던지면 "[recovered]" 줄만 덧붙는다.
// 되돌리기 자체가 실패한 것은 fn 이 오류 없이 끝났을 때만 돌려준다. fn 의 오류가 사용자가 알아야 할 것이다.
//
// 다만 여기서 되돌리는 것은 fn 을 부른 이 고루틴의 패닉뿐이다. 다른 고루틴이 패닉하면 Go 는 그 고루틴의 지연
// 함수만 돌리고 프로세스를 끝내므로 이 defer 는 돌지 않고 raw 모드와 대체 화면이 그대로 남는다. 그래서 tui 가
// 띄우는 고루틴(Keys)과 화면 컨트롤러의 배경 고루틴(fetch 같은 것)은 반드시 Go 로 띄운다.
func Run(fn func(t *Terminal) error) (err error) {
	t, err := Open()
	if err != nil {
		return err
	}
	defer func() {
		closeErr := t.Close()
		if err == nil {
			err = closeErr
		}
	}()
	return fn(t)
}

// Go는 fn 을 새 고루틴에서 돌리되, fn 이 패닉으로 끝나면 터미널을 먼저 되돌린다.
//
// Run 의 되돌리기는 Run 을 부른 고루틴에서만 돈다. 다른 고루틴의 패닉은 그 고루틴의 지연 함수만 돌린 뒤
// 프로세스를 끝내므로, 되돌리기를 그 고루틴 안에 심어 두어야 한다. 되돌린 뒤에는 패닉을 잡지 않고 그대로
// 두어 원래 자리의 스택과 함께 찍히게 한다(Run 과 같은 이유다). 정상으로 끝났을 때는 되돌리지 않는다.
// 배경 고루틴 하나가 끝났다고 화면이 닫히면 안 된다.
func (t *Terminal) Go(fn func()) {
	go t.guarded(fn)
}

// guarded는 Go 의 본체다. 고루틴을 띄우지 않으므로 시험이 되돌리기와 퍼진 패닉을 바깥에서 받아 볼 수 있다.
func (t *Terminal) guarded(fn func()) {
	finished := false
	defer func() {
		if !finished {
			_ = t.Close()
		}
	}()
	fn()
	finished = true
}

// Keys는 표준 입력의 키를 채널로 보낸다. ReadKeys 와 같되, 읽는 고루틴을 Go 로 띄워 그 안의 패닉에도
// 터미널을 되돌린다. 터미널 안에서 키를 받는 길은 이것뿐이다.
func (t *Terminal) Keys(ctx context.Context) <-chan Key {
	return readKeys(ctx, t.in, t.Go)
}
