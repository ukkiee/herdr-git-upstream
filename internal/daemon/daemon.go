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
	"time"

	"herdr-pull-status/internal/config"
	"herdr-pull-status/internal/state"
)

const (
	// tick은 루프가 눈을 뜨는 간격이다. 대부분의 회차는 파일 하나를 확인하고 다시 잠들 뿐이라 값이 싸다.
	// 이 정도로 촘촘해야 포커스를 옮긴 직후에 사람이 기다린다고 느끼지 않는다.
	tick = time.Second
	// heartbeatInterval은 데몬이 살아 있음을 알리는 간격이다.
	heartbeatInterval = 10 * time.Second
	// lockStaleAfter는 하트비트가 이만큼 끊기면 앞선 데몬이 죽은 것으로 보는 기준이다.
	// 하트비트 간격의 여러 배로 두어, 잠깐 느려진 것을 죽음으로 오해하지 않게 한다.
	lockStaleAfter = 90 * time.Second
)

// Run은 갱신 루프를 돈다. 잠금을 얻지 못하면 ErrDaemonRunning을 돌려준다.
func Run(ctx context.Context, log *slog.Logger) error {
	store := state.New()
	if err := store.AcquireDaemonLock(lockStaleAfter); err != nil {
		return err
	}
	defer store.ReleaseDaemonLock()

	// 지난 중지 요청이 남아 있으면 방금 띄운 데몬이 곧바로 꺼진다. 시작할 때 치운다.
	store.ClearStop()

	cfg, err := config.Load()
	if err != nil {
		log.Warn("설정을 읽지 못해 기본값으로 시작한다", "error", err)
	}
	syncer := NewSyncer(cfg, log)
	log.Info("데몬 시작",
		"interval", cfg.Interval.String(),
		"throttle", cfg.Throttle.String(),
		"pid", os.Getpid(),
	)

	// 첫 회차를 바로 돈다. 데몬이 뜨자마자 사이드바가 채워져야, 사용자가 설정이 먹었는지 알 수 있다.
	sweep(ctx, syncer, log, "", false)

	lastSweep := time.Now()
	lastHeartbeat := time.Now()
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("데몬 종료", "reason", "취소됨")
			return nil
		case now := <-ticker.C:
			if store.StopRequested() {
				log.Info("데몬 종료", "reason", "중지 요청")
				return nil
			}
			if now.Sub(lastHeartbeat) >= heartbeatInterval {
				if !store.Heartbeat() {
					// 잠금이 더는 우리 것이 아니다. 다른 데몬이 넘겨받았다는 뜻이므로 물러난다.
					log.Info("데몬 종료", "reason", "잠금을 잃음")
					return nil
				}
				lastHeartbeat = now
			}

			// 설정 파일이 바뀌었을 수 있으므로 매 회차 다시 읽는다. 파일 하나를 읽는 비용이라
			// 사용자가 herdr를 재시작하지 않고도 주기를 바꿀 수 있게 하는 값어치가 있다.
			if reloaded, err := config.Load(); err == nil {
				cfg = reloaded
				syncer.Config = reloaded
				syncer.Git.Timeout = reloaded.FetchTimeout
			}

			if hint, ok := takeWake(store); ok {
				sweep(ctx, syncer, log, hint.Workspace, hint.Force)
				// 전체를 한 바퀴 돌았으니 다음 정기 회차를 처음부터 센다.
				if hint.Workspace == "" {
					lastSweep = now
				}
				continue
			}
			if now.Sub(lastSweep) >= cfg.Interval {
				sweep(ctx, syncer, log, "", false)
				lastSweep = now
			}
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

// wakePath는 힌트 파일의 경로다.
func wakePath(store state.Store) string {
	return filepath.Join(store.Dir, "wake.json")
}

// Wake는 데몬에게 지금 한 바퀴 돌라고 알린다.
//
// 이벤트 훅이 호출하며, 작은 파일 하나를 쓰는 것이 전부라 훅은 곧바로 끝난다. 훅이 herdr의
// 플러그인 명령 자리를 오래 잡지 않아야 다른 플러그인의 훅이 밀리지 않는다.
//
// 이미 강제 갱신 요청이 놓여 있으면 덮어쓰지 않는다. 사람이 누른 "지금 갱신"이 그 직후에 들어온
// 포커스 이벤트에 지워지면, 기다리던 갱신이 조용히 사라져 버리기 때문이다.
func Wake(store state.Store, hint WakeHint) error {
	if err := os.MkdirAll(store.Dir, 0o700); err != nil {
		return err
	}
	if !hint.Force {
		if existing, err := readWake(store); err == nil && existing.Force {
			return nil
		}
	}
	raw, err := json.Marshal(hint)
	if err != nil {
		return err
	}
	return os.WriteFile(wakePath(store), raw, 0o600)
}

func readWake(store state.Store) (WakeHint, error) {
	raw, err := os.ReadFile(wakePath(store))
	if err != nil {
		return WakeHint{}, err
	}
	var hint WakeHint
	if err := json.Unmarshal(raw, &hint); err != nil {
		return WakeHint{}, err
	}
	return hint, nil
}

// takeWake는 힌트가 있으면 읽고 지운다.
func takeWake(store state.Store) (WakeHint, bool) {
	hint, err := readWake(store)
	if err != nil {
		// 파일이 깨져 있어도 치운다. 그대로 두면 매 회차마다 같은 실패를 반복한다.
		if !errors.Is(err, fs.ErrNotExist) {
			_ = os.Remove(wakePath(store))
		}
		return WakeHint{}, false
	}
	_ = os.Remove(wakePath(store))
	return hint, true
}

// EnsureRunning은 데몬이 살아 있는지 보고, 없으면 띄운다.
//
// startup 훅만으로는 부족하다. herdr는 서버가 세션을 복구할 때만 그 훅을 부르고, 사용자가 플러그인을
// 방금 link하거나 enable했을 때는 부르지 않는다. 그래서 포커스 이벤트가 이 함수를 거치도록 해 두면,
// 설치 직후 herdr를 재시작하지 않아도 곧 동작하기 시작한다.
func EnsureRunning() error {
	store := state.New()
	if store.DaemonAlive(lockStaleAfter) {
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
func Stop() error {
	store := state.New()
	if !store.DaemonAlive(lockStaleAfter) {
		// 이미 멈춰 있다. 남은 잠금 파일만 치워 다음 시작이 넘겨받기 절차를 거치지 않게 한다.
		if err := os.Remove(filepath.Join(store.Dir, "daemon.lock")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return store.RequestStop()
}
