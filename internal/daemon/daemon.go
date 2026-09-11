// Package daemon은 배경에서 도는 갱신 루프와, 그 루프를 띄우고 깨우는 방법을 담는다.
//
// 왜 이벤트 훅이 직접 fetch하지 않고 데몬을 두는가. herdr는 동시에 도는 플러그인 명령을 32개로
// 제한한다. 포커스를 옮길 때마다 네트워크를 타는 명령이 그 자리를 최대 수십 초씩 차지하면, 탭 이름을
// 바꾸는 것 같은 다른 플러그인의 훅까지 밀린다. 그래서 이벤트 훅은 데몬을 깨우기만 하고 즉시 끝나며,
// 오래 걸리는 일은 herdr 바깥의 데몬이 맡는다.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"

	"herdr-git-upstream/internal/config"
	"herdr-git-upstream/internal/state"
)

const (
	// tick은 루프가 눈을 뜨는 간격이다. 대부분의 회차는 파일 하나를 확인하고 다시 잠들 뿐이라 값이 싸다.
	// 이 정도로 촘촘해야 포커스를 옮긴 직후에 사람이 기다린다고 느끼지 않는다.
	tick = time.Second
	// LockStaleAfter는 하트비트가 이만큼 끊기면 앞선 데몬이 죽은 것으로 보는 기준이다.
	// 하트비트 간격의 여러 배로 두어, 잠깐 느려진 것을 죽음으로 오해하지 않게 한다.
	LockStaleAfter = 90 * time.Second
)

// heartbeatInterval은 데몬이 살아 있음을 알리는 간격이다.
// 상수가 아닌 이유는 시험에서 짧게 줄여, 90초를 기다리지 않고도 동작을 확인하기 위해서다.
var heartbeatInterval = 10 * time.Second

// Run은 갱신 루프를 돈다. 잠금을 얻지 못하면 ErrDaemonRunning을 돌려준다.
func Run(ctx context.Context, log *slog.Logger) error {
	store := state.New()
	if err := store.AcquireDaemonLock(LockStaleAfter); err != nil {
		return err
	}
	defer store.ReleaseDaemonLock()

	// 지난 중지 요청이 남아 있으면 방금 띄운 데몬이 곧바로 꺼진다. 시작할 때 치운다.
	store.ClearStop()

	cfg, err := config.Load()
	if err != nil {
		log.Warn("설정을 읽지 못해 기본값으로 시작한다", "error", err)
	}
	for _, name := range cfg.InvalidTokenNames() {
		log.Warn("토큰 이름이 herdr 규칙에 맞지 않아 그 토큰은 보고하지 않는다", "name", name)
	}
	syncer := NewSyncer(cfg, log)
	log.Info("데몬 시작",
		"interval", cfg.Interval.String(),
		"throttle", cfg.Throttle.String(),
		"pid", os.Getpid(),
		"state_dir", store.Dir,
	)

	// 잠금 갱신과 중지 확인은 전용 고루틴이 맡는다.
	//
	// sweep은 길이에 상한이 없다. 응답 없는 원격 하나가 제한 시간만큼 붙잡고, 워크스페이스가 많으면
	// 그것이 여러 번 쌓인다. 하트비트를 틱 처리 안에서 찍으면 그동안 한 번도 갱신되지 않아
	// 멀쩡히 일하는 데몬이 죽은 것으로 몰리고, 다른 데몬이 잠금을 빼앗아 둘이 함께 돌게 된다.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var lockLost, stopped atomic.Bool
	go keepLockFresh(ctx, store, cancel, &lockLost, &stopped)
	localDone := make(chan struct{})
	go func() {
		defer close(localDone)
		syncer.watchLocal(ctx)
	}()
	defer func() { cancel(); <-localDone }()

	// 첫 회차를 바로 돈다. 데몬이 뜨자마자 사이드바가 채워져야, 사용자가 설정이 먹었는지 알 수 있다.
	sweep(ctx, syncer, log, "", false)

	lastSweep := time.Now()
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			switch {
			case stopped.Load():
				log.Info("데몬 종료", "reason", "중지 요청")
			case lockLost.Load():
				log.Info("데몬 종료", "reason", "잠금을 잃음")
			default:
				log.Info("데몬 종료", "reason", "취소됨")
			}
			return nil
		case <-ticker.C:
			// 티커가 실어 보낸 시각이 아니라 지금을 쓴다. sweep이 길어져 틱이 밀렸을 때
			// 티커의 시각은 과거를 가리켜, 다음 회차가 곧바로 또 도는 것으로 계산된다.
			now := time.Now()

			// 설정 파일이 바뀌었을 수 있으므로 매 회차 다시 읽는다. 파일 하나를 읽는 비용이라
			// 사용자가 herdr를 재시작하지 않고도 주기를 바꿀 수 있게 하는 값어치가 있다.
			if reloaded, err := config.Load(); err == nil {
				cfg = reloaded
				syncer.localMu.Lock()
				syncer.Config = reloaded
				syncer.Git.Timeout = reloaded.FetchTimeout
				syncer.localMu.Unlock()
			}

			if hint, ok := takeWake(store); ok {
				sweep(ctx, syncer, log, hint.Workspace, hint.Force)
				// 전체를 한 바퀴 돌았으니 다음 정기 회차를 처음부터 센다.
				if hint.Workspace == "" {
					lastSweep = time.Now()
				}
				continue
			}
			if now.Sub(lastSweep) >= cfg.Interval {
				sweep(ctx, syncer, log, "", false)
				lastSweep = time.Now()
			}
		}
	}
}

// keepLockFresh는 데몬이 사는 동안 잠금을 갱신하고 중지 요청을 지켜본다.
//
// 갱신을 도는 쪽과 떼어 놓는 것이 핵심이다. sweep은 길이에 상한이 없어서 같은 흐름에 두면
// 그동안 잠금이 낡아 버리고, 멀쩡히 일하는 데몬이 쫓겨난다.
//
// 중지 확인을 여기서만 하는 이유도 있다. StopRequested는 표시를 지우면서 답하므로, 두 곳에서
// 부르면 한쪽이 먼저 먹어 버려 다른 쪽은 요청을 영영 보지 못한다.
func keepLockFresh(ctx context.Context, store state.Store, cancel context.CancelFunc, lockLost, stopped *atomic.Bool) {
	beat := time.NewTicker(tick)
	defer beat.Stop()
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-beat.C:
			if store.StopRequested() {
				stopped.Store(true)
				cancel()
				return
			}
			// 단조 시계가 아니라 벽시계로 견준다. 기기가 절전에 들어간 동안에는 벽시계만 흐르므로,
			// 깨어난 직후 곧바로 하트비트를 찍어야 잠금이 낡은 것으로 보이지 않는다.
			now := time.Now()
			if now.Round(0).Sub(last.Round(0)) < heartbeatInterval {
				continue
			}
			if !store.Heartbeat() {
				lockLost.Store(true)
				cancel()
				return
			}
			last = now
		}
	}
}

func sweep(ctx context.Context, syncer *Syncer, log *slog.Logger, workspace string, force bool) {
	if err := syncer.Sweep(ctx, workspace, force); err != nil {
		// herdr가 꺼져 있거나 재시작 중이면 여기로 온다. 다음 회차에 저절로 회복되므로 조용히 넘어간다.
		log.Debug("갱신 실패", "workspace", workspace, "error", err)
	}
}

// WakeHint는 데몬에게 남기는 쪽지다.
type WakeHint struct {
	// Workspace가 비어 있으면 전체를, 아니면 그 워크스페이스만 갱신한다.
	Workspace string `json:"workspace,omitempty"`
	// Force가 참이면 스로틀을 무시한다. 사람이 직접 갱신을 눌렀을 때만 쓴다.
	Force bool `json:"force,omitempty"`
}

func wakePath(store state.Store) string {
	return filepath.Join(store.Dir, "wake.json")
}

// wakeForcePath는 "지금 모두 갱신" 요청을 나타내는 표시 파일이다.
//
// 강제 요청을 쪽지 안의 필드로 두면 안 된다. herdr는 포커스 한 번에 workspace.focused와
// pane.focused를 함께 내보내고 각각을 별도 프로세스로 띄우므로, 쪽지를 쓰는 일이 늘 겹친다.
// 그 사이에 놓인 강제 요청은 조용히 덮여 사라진다. 파일을 따로 두면 덮일 것이 없다.
func wakeForcePath(store state.Store) string {
	return filepath.Join(store.Dir, "wake.force")
}

// Wake는 데몬에게 지금 한 바퀴 돌라고 알린다.
//
// 이벤트 훅이 호출하며, 작은 파일 하나를 쓰는 것이 전부라 훅은 곧바로 끝난다. 훅이 herdr의
// 플러그인 명령 자리를 오래 잡지 않아야 다른 플러그인의 훅이 밀리지 않는다.
func Wake(store state.Store, hint WakeHint) error {
	if err := os.MkdirAll(store.Dir, 0o700); err != nil {
		return err
	}
	if hint.Force {
		// 이미 대기 중인 강제 요청이 있으면 그것으로 충분하다. O_EXCL이 실패하는 것은
		// 요청이 놓여 있다는 뜻이므로 성공으로 다룬다.
		file, err := os.OpenFile(wakeForcePath(store), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			if errors.Is(err, fs.ErrExist) {
				return nil
			}
			return err
		}
		return file.Close()
	}
	raw, err := json.Marshal(hint)
	if err != nil {
		return err
	}
	return state.WriteFileAtomic(wakePath(store), raw)
}

// takeWake는 쪽지가 있으면 집어 온다.
//
// 읽기 전에 이름을 바꿔 가져온다. 읽고 나서 지우면 그 사이에 쓰인 쪽지를 한 번도 읽지 않은 채
// 지우게 된다. 강제 표시는 지우기의 성공 여부 자체로 판단해, 확인과 소비를 한 번의 시스템 호출로 합친다.
func takeWake(store state.Store) (WakeHint, bool) {
	forced := os.Remove(wakeForcePath(store)) == nil

	taken := wakePath(store) + ".taken"
	if err := os.Rename(wakePath(store), taken); err != nil {
		// 쪽지는 없고 강제 요청만 들어온 경우다. 전체를 강제로 갱신한다.
		if forced {
			return WakeHint{Force: true}, true
		}
		return WakeHint{}, false
	}
	defer func() { _ = os.Remove(taken) }()

	raw, err := os.ReadFile(taken)
	if err != nil {
		return WakeHint{Force: forced}, true
	}
	var hint WakeHint
	if err := json.Unmarshal(raw, &hint); err != nil {
		return WakeHint{Force: forced}, true
	}
	if forced {
		// 강제 요청은 "지금 모두 갱신"이므로 범위를 좁히지 않는다. 마침 함께 들어온 포커스 쪽지의
		// 워크스페이스만 갱신하면 액션 이름과 다른 일을 하게 된다.
		hint.Force = true
		hint.Workspace = ""
	}
	return hint, true
}

// EnsureRunning은 데몬이 살아 있는지 보고, 없으면 띄운다.
//
// startup 훅만으로는 부족하다. herdr는 서버가 세션을 복구할 때만 그 훅을 부르고, 사용자가 플러그인을
// 방금 link하거나 enable했을 때는 부르지 않는다. 그래서 포커스 이벤트가 이 함수를 거치도록 해 두면,
// 설치 직후 herdr를 재시작하지 않아도 곧 동작하기 시작한다.
func EnsureRunning() error {
	store := state.New()
	if store.DaemonAlive(LockStaleAfter) {
		return nil
	}
	return SpawnDetached()
}

// SpawnDetached는 자기 자신을 데몬으로 다시 띄우고 곧바로 반환한다.
//
// herdr의 startup 훅은 일회성이라, 훅 안에서 루프를 돌면 그 명령이 영원히 끝나지 않은 것으로 남는다.
// 그래서 훅은 이 함수로 데몬을 떼어 내고 즉시 끝난다.
func SpawnDetached() error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("실행 파일 경로를 찾지 못했다: %w", err)
	}
	store := state.New()
	if err := os.MkdirAll(store.Dir, 0o700); err != nil {
		return err
	}
	// 떨어져 나간 프로세스의 출력은 갈 곳이 있어야 한다. 로그 파일로 보내면 문제가 생겼을 때
	// 사용자가 볼 곳이 생기고, 파이프가 막혀 데몬이 멈추는 일도 없다.
	logFile, err := os.OpenFile(store.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()

	cmd := exec.Command(executable, "daemon", "--foreground")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// 작업 디렉터리를 플러그인 폴더 밖으로 옮긴다. 그러지 않으면 herdr가 훅을 실행한 자리를
	// 그대로 물려받는데, 윈도우에서는 프로세스의 작업 디렉터리가 붙잡혀 있는 동안 그 폴더를
	// 이름 바꾸거나 지울 수 없다. 데몬은 오래 살기 때문에 플러그인 갱신과 삭제가 막혀 버린다.
	// 데몬 자신은 작업 디렉터리에 기대지 않는다. git 호출은 저마다 위치를 지정하고,
	// herdr는 절대 경로로 부른다.
	cmd.Dir = store.Dir
	detachProcess(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("데몬을 띄우지 못했다: %w", err)
	}
	// 자식을 기다리지 않고 놓아준다. 부모가 먼저 끝나면 자식은 init에 입양된다.
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("데몬 프로세스를 놓아주지 못했다: %w", err)
	}
	return nil
}

// Stop은 데몬에게 멈추라고 요청한다.
//
// 살아 있는지 먼저 보지 않는다. 하트비트만으로는 긴 회차를 도는 멀쩡한 데몬도 죽은 것처럼 보이는데,
// 그때 잠금 파일만 지우고 물러나면 데몬은 계속 돌면서 잠금은 비어, 이어지는 포커스 이벤트가
// 두 번째 데몬을 띄운다. 요청 파일은 데몬이 정말 죽어 있어도 해롭지 않다. 다음 데몬이 시작할 때
// 스스로 치운다.
func Stop() error {
	return state.New().RequestStop()
}
