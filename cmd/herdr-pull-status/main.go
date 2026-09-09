// herdr-pull-status는 herdr 사이드바에 "이 저장소는 원격보다 몇 커밋 뒤처져 있다"를 띄운다.
//
// herdr는 사이드바에 앞뒤 커밋 수를 그리지만, 그 비교 대상인 원격 추적 참조를 스스로 갱신하지는
// 않는다. 그래서 원격에 새 커밋이 올라와도 누군가 fetch 하기 전까지는 아무 변화가 없다.
// 이 프로그램이 그 fetch를 맡고, 결과를 워크스페이스 토큰으로도 보고한다.
//
// 토큰까지 보고하는 이유가 있다. herdr는 같은 저장소를 공유하는 워크스페이스들을 묶어 들여쓰는데,
// 들여쓴 행에서는 내장 git 표시를 지운다. worktree를 쓰는 사람에게는 그 행이 오히려 더 중요하다.
// 커스텀 토큰은 그 행에서도 그려지므로, 토큰을 쓰면 어느 행에서나 같은 정보가 보인다.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"herdr-pull-status/internal/config"
	"herdr-pull-status/internal/daemon"
	"herdr-pull-status/internal/state"
)

const version = "0.1.0"

const usage = `herdr-pull-status ` + version + `

사용법:
  herdr-pull-status daemon [--detach|--foreground]   배경 갱신 루프를 띄운다
  herdr-pull-status fetch                            데몬을 깨운다 (herdr 이벤트 훅용)
  herdr-pull-status refresh                          스로틀을 무시하고 전체를 갱신한다
  herdr-pull-status stop                             데몬을 멈춘다
  herdr-pull-status status                           데몬과 설정 상태를 출력한다
  herdr-pull-status version                          판 번호를 출력한다
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}

	switch args[0] {
	case "daemon":
		return runDaemon(args[1:])
	case "fetch":
		return runFetch()
	case "refresh":
		return runRefresh()
	case "stop":
		return runStop()
	case "status":
		return runStatus()
	case "version", "--version", "-v":
		fmt.Println(version)
		return 0
	case "help", "--help", "-h":
		fmt.Print(usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "알 수 없는 명령: %s\n\n%s", args[0], usage)
		return 2
	}
}

func runDaemon(args []string) int {
	foreground := false
	for _, arg := range args {
		switch arg {
		case "--foreground":
			foreground = true
		case "--detach":
			foreground = false
		default:
			fmt.Fprintf(os.Stderr, "daemon: 알 수 없는 옵션: %s\n", arg)
			return 2
		}
	}

	if !foreground {
		// startup 훅과 액션이 오는 길이다. 데몬을 떼어 내고 즉시 끝나야 훅이 매달려 있지 않는다.
		if alreadyRunning() {
			return 0
		}
		if err := daemon.SpawnDetached(); err != nil {
			fmt.Fprintf(os.Stderr, "데몬을 띄우지 못했다: %v\n", err)
			return 1
		}
		return 0
	}

	log := newLogger(os.Stderr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := daemon.Run(ctx, log); err != nil {
		if err == state.ErrDaemonRunning {
			// 다른 데몬이 이미 일하고 있다. 실패가 아니다.
			return 0
		}
		log.Error("데몬이 멈췄다", "error", err)
		return 1
	}
	return 0
}

// runFetch는 herdr의 포커스 이벤트가 부르는 길이다.
//
// 여기서 절대 네트워크를 타면 안 된다. herdr는 동시에 도는 플러그인 명령을 32개로 제한하는데,
// 포커스는 자주 바뀌고 fetch는 느리기 때문이다. 그래서 쪽지를 남기고 데몬이 살아 있는지만 확인한다.
func runFetch() int {
	store := state.New()
	hint := daemon.WakeHint{Workspace: os.Getenv("HERDR_WORKSPACE_ID")}
	if err := daemon.Wake(store, hint); err != nil {
		// 쪽지를 남기지 못해도 정기 회차가 곧 돌아온다. 포커스 이벤트를 실패로 만들 일은 아니다.
		fmt.Fprintf(os.Stderr, "데몬을 깨우지 못했다: %v\n", err)
	}
	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "데몬을 띄우지 못했다: %v\n", err)
		return 1
	}
	return 0
}

// runRefresh는 사람이 "지금 갱신"을 눌렀을 때다. 스로틀을 무시하도록 표시해 데몬에 넘긴다.
func runRefresh() int {
	store := state.New()
	if err := daemon.Wake(store, daemon.WakeHint{Force: true}); err != nil {
		fmt.Fprintf(os.Stderr, "갱신을 요청하지 못했다: %v\n", err)
		return 1
	}
	if err := daemon.EnsureRunning(); err != nil {
		fmt.Fprintf(os.Stderr, "데몬을 띄우지 못했다: %v\n", err)
		return 1
	}
	fmt.Println("갱신을 요청했다.")
	return 0
}

func runStop() int {
	if err := daemon.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "데몬을 멈추지 못했다: %v\n", err)
		return 1
	}
	fmt.Println("데몬에 중지를 요청했다.")
	return 0
}

func runStatus() int {
	store := state.New()
	cfg, cfgErr := config.Load()

	type report struct {
		Version      string `json:"version"`
		DaemonAlive  bool   `json:"daemon_alive"`
		StateDir     string `json:"state_dir"`
		LogPath      string `json:"log_path"`
		ConfigDir    string `json:"config_dir"`
		ConfigError  string `json:"config_error,omitempty"`
		Enabled      bool   `json:"enabled"`
		Interval     string `json:"interval"`
		Throttle     string `json:"throttle"`
		FetchTimeout string `json:"fetch_timeout"`
		StaleAfter   string `json:"stale_after"`
		BehindToken  string `json:"behind_token"`
		AheadToken   string `json:"ahead_token"`
		StaleToken   string `json:"stale_token"`
	}
	out := report{
		Version:      version,
		DaemonAlive:  store.DaemonAlive(90 * time.Second),
		StateDir:     store.Dir,
		LogPath:      store.LogPath(),
		ConfigDir:    config.Dir(),
		Enabled:      cfg.Enabled,
		Interval:     cfg.Interval.String(),
		Throttle:     cfg.Throttle.String(),
		FetchTimeout: cfg.FetchTimeout.String(),
		StaleAfter:   cfg.StaleAfter.String(),
		BehindToken:  cfg.BehindToken,
		AheadToken:   cfg.AheadToken,
		StaleToken:   cfg.StaleToken,
	}
	if cfgErr != nil {
		out.ConfigError = cfgErr.Error()
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "상태를 출력하지 못했다: %v\n", err)
		return 1
	}
	return 0
}

func alreadyRunning() bool {
	return state.New().DaemonAlive(90 * time.Second)
}

func newLogger(w io.Writer) *slog.Logger {
	level := slog.LevelInfo
	// 문제를 쫓을 때 환경변수 하나로 상세 로그를 켤 수 있게 한다.
	if os.Getenv("HERDR_PULL_STATUS_DEBUG") != "" {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}
