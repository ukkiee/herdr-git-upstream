// herdr-git-upstream는 herdr 사이드바에 "이 저장소는 원격보다 몇 커밋 뒤처져 있다"를 띄운다.
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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"herdr-git-upstream/internal/config"
	"herdr-git-upstream/internal/daemon"
	"herdr-git-upstream/internal/freshen"
	"herdr-git-upstream/internal/gitrepo"
	"herdr-git-upstream/internal/herdrcli"
	"herdr-git-upstream/internal/herdrconfig"
	"herdr-git-upstream/internal/herdrpaths"
	"herdr-git-upstream/internal/setup"
	"herdr-git-upstream/internal/state"
	"herdr-git-upstream/internal/worktreeui"
)

const version = "0.1.0"

// pluginID는 매니페스트(herdr-plugin.toml)의 id 다. 자기 pane 을 열 때 herdr 에게 이 이름으로 말한다.
const pluginID = "git-upstream"

const usage = `herdr-git-upstream ` + version + `

사용법:
  herdr-git-upstream daemon [--detach|--foreground]   배경 갱신 루프를 띄운다
  herdr-git-upstream fetch                            데몬을 깨운다 (herdr 이벤트 훅용)
  herdr-git-upstream refresh                          스로틀을 무시하고 전체를 갱신한다
  herdr-git-upstream worktree-created                 새 worktree를 최신 상태로 맞춘다
  herdr-git-upstream stop                             데몬을 멈춘다
  herdr-git-upstream status                           데몬과 설정 상태를 출력한다
  herdr-git-upstream setup                            붙여 넣을 herdr 설정을 출력한다
  herdr-git-upstream worktrees [--cwd <path>]         worktree 화면을 지금 페인에 그린다
  herdr-git-upstream open-worktrees                   worktree 화면을 herdr 팝업 pane 으로 연다
  herdr-git-upstream new-worktree [--cwd <path>]      기준 브랜치를 고르는 생성 팝업을 그린다
  herdr-git-upstream open-new-worktree                생성 팝업을 herdr pane 으로 연다
  herdr-git-upstream version                          판 번호를 출력한다
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
	case "worktree-created":
		return runWorktreeCreated()
	case "stop":
		return runStop()
	case "status":
		return runStatus()
	case "setup":
		return runSetup()
	case "worktrees":
		return runWorktrees(args[1:])
	case "open-worktrees":
		return runOpenScreen("worktrees")
	case "new-worktree":
		return runScreen(args[1:], "new-worktree", worktreeui.RunCreate)
	case "open-new-worktree":
		return runOpenScreen("new-worktree")
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

// runWorktreeCreated는 herdr가 worktree를 막 만들었을 때 부른다.
//
// herdr는 원본 체크아웃의 HEAD를 기준으로 worktree를 만들고 fetch는 하지 않는다. 그래서 손에 쥔
// 브랜치가 뒤처져 있으면 새 worktree도 뒤처진 채로 시작한다. 여기서 한 번 앞으로 감아 준다.
func runWorktreeCreated() int {
	log := newLogger(os.Stderr)
	cfg, err := config.Load()
	if err != nil {
		log.Warn("설정을 읽지 못해 기본값으로 진행한다", "error", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	result, err := freshen.WorktreeCreated(ctx, cfg, log)
	switch {
	case errors.Is(err, freshen.ErrSkipped):
		return 0
	case err != nil:
		// worktree 자체는 멀쩡히 만들어졌다. 최신으로 맞추지 못한 것이 사용자의 작업을 막을 이유는 없다.
		log.Warn("새 worktree를 최신 상태로 맞추지 못했다", "error", err)
		return 0
	case result.Moved:
		log.Info("새 worktree를 최신 상태로 맞췄다",
			"path", result.Path, "branch", result.Branch, "base", result.TrackingRef)
	}
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

// runSetup은 사용자가 herdr 설정에 붙여 넣을 조각을 출력한다.
//
// 파일은 절대 고치지 않는다. 사이드바 행과 키는 herdr의 config.toml에 들어가야 하는데, 그 파일은
// 사용자의 것이다. 이미 들어 있는 것은 빼고 남은 것만 보여 주어 붙여 넣기가 한 번으로 끝나게 한다.
func runSetup() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "설정을 읽지 못해 기본 토큰 이름으로 진행한다: %v\n", err)
	}
	path := herdrpaths.ConfigFile()
	scan, err := herdrconfig.Load()
	if err != nil {
		// 파일이 없는 것은 오류가 아니다. 여기 오는 것은 권한 같은 문제인데, 그래도 붙여 넣을 본문은
		// 만들 수 있다. 아무것도 없는 것으로 보고 전체를 보여 준다.
		fmt.Fprintf(os.Stderr, "%s 를 읽지 못해 아무것도 없는 것으로 보고 진행한다: %v\n", path, err)
	}
	fmt.Print(setup.Render(cfg, scan, abbreviateHome(path)))
	return 0
}

// abbreviateHome은 홈 디렉터리 아래의 경로를 ~ 로 줄인다. 안내 첫 줄에 보여 줄 때만 쓴다.
//
// 홈은 정리한 뒤 견준다. os.UserHomeDir는 HOME을 그대로 돌려주므로 값이 구분자로 끝나면
// `home + 구분자` 접두사가 구분자 둘로 끝나, herdrpaths가 filepath.Join으로 정리해 둔 경로와 맞지 않는다.
func abbreviateHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	home = filepath.Clean(home)
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}
	return path
}

// runWorktrees는 worktree 화면을 지금 있는 페인(터미널)에 그린다.
//
// 어느 저장소인지는 두 길로 정한다. 액션이나 herdr 셸에서 부르면 HERDR_WORKSPACE_ID 가 있어 그 워크스페이스의
// 저장소이고, 없으면 현재 디렉터리다. --cwd 를 주면 그것이 우선한다. 사람이 자리를 명시했는데 환경변수가 이기면
// 다른 저장소를 보고 있는 줄도 모르게 된다.
//
// 종료 코드를 가른다. 터미널이 아니면 2(사용법 오류와 같은 급이다. 파이프 뒤에서 화면을 그릴 수는 없다),
// 저장소가 아니거나 그 밖의 실패는 1 이다.
func runWorktrees(args []string) int {
	return runScreen(args, "worktrees", worktreeui.Run)
}

// runScreen은 두 화면의 경로 선택과 오류 종료 코드를 같은 규칙으로 처리한다.
func runScreen(args []string, command string, screen func(context.Context, worktreeui.Options) error) int {
	cwd := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--cwd":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "%s: --cwd 뒤에 경로가 있어야 한다\n", command)
				return 2
			}
			cwd = args[i+1]
			i++
		default:
			fmt.Fprintf(os.Stderr, "%s: 알 수 없는 옵션: %s\n", command, args[i])
			return 2
		}
	}
	workspaceID := os.Getenv("HERDR_WORKSPACE_ID")
	if cwd != "" {
		workspaceID = ""
	} else {
		dir, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: 현재 디렉터리를 알 수 없다: %v\n", command, err)
			return 1
		}
		cwd = dir
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "설정을 읽지 못해 기본값으로 진행한다: %v\n", err)
	}
	// 신호는 화면(internal/tui)이 받아 터미널을 되돌린 뒤 다시 던진다. 여기서 ctx 로 받으면 되돌린 뒤의 신호가
	// "취소됨" 오류로 바뀌어 셸에 찍힌다. 프로세스가 신호로 끝나는 것이 그 뜻에 맞다.
	err = screen(context.Background(), worktreeui.Options{
		CWD:         cwd,
		WorkspaceID: workspaceID,
		Git:         gitrepo.Runner{Timeout: cfg.FetchTimeout},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", command, err)
		if errors.Is(err, worktreeui.ErrNoTerminal) {
			return 2
		}
		return 1
	}
	return 0
}

// runOpenScreen은 매니페스트의 화면을 herdr 의 pane 으로 연다. 키 설정과 `plugin action invoke` 가 오는 길이다.
//
// placement 는 비워서 부른다. 매니페스트의 popup 을 따르게 하려는 것인데 CLI 의 --placement 목록에는 popup 이
// 없기 때문이다. 비운 것을 herdr 가 받아 주지 않으면(오류 문구에 placement 가 들어 있으면) overlay 로 한 번 더
// 부른다. 실측 뒤 매니페스트의 popup 을 따르지 않는 것으로 확인되면 그때 기본을 overlay 로 바꾼다.
func runOpenScreen(entrypoint string) int {
	client := herdrcli.New()
	ctx := context.Background()
	err := client.PluginPaneOpen(ctx, pluginID, entrypoint, "")
	if err == nil {
		return 0
	}
	fmt.Fprintf(os.Stderr, "%s 화면을 열지 못했다: %v\n", entrypoint, err)
	if !strings.Contains(err.Error(), "placement") {
		return 1
	}
	fmt.Fprintln(os.Stderr, "placement 를 overlay 로 다시 시도한다")
	if err := client.PluginPaneOpen(ctx, pluginID, entrypoint, "overlay"); err != nil {
		fmt.Fprintf(os.Stderr, "%s 화면을 열지 못했다: %v\n", entrypoint, err)
		return 1
	}
	return 0
}

func runStatus() int {
	store := state.New()
	cfg, cfgErr := config.Load()
	scan, scanErr := herdrconfig.Load()
	git := gitrepo.Runner{}
	ctx := context.Background()

	type report struct {
		Version     string `json:"version"`
		DaemonAlive bool   `json:"daemon_alive"`
		// GitVersion과 MergeTreeSupported는 catchup 토큰이 왜 비어 있는지 답한다. merge-tree 비교는
		// git 2.38 이상에서만 하므로, 그 아래에서는 catchup이 조용히 쉬고 merged는 조상 검사만 한다.
		GitVersion         string `json:"git_version"`
		MergeTreeSupported bool   `json:"merge_tree_supported"`
		StateDir           string `json:"state_dir"`
		LogPath            string `json:"log_path"`
		ConfigDir          string `json:"config_dir"`
		ConfigError        string `json:"config_error,omitempty"`
		Enabled            bool   `json:"enabled"`
		Interval           string `json:"interval"`
		Throttle           string `json:"throttle"`
		FetchTimeout       string `json:"fetch_timeout"`
		StaleAfter         string `json:"stale_after"`
		BehindToken        string `json:"behind_token"`
		AheadToken         string `json:"ahead_token"`
		GoneToken          string `json:"gone_token"`
		MergedToken        string `json:"merged_token"`
		CatchupToken       string `json:"catchup_token"`
		StaleToken         string `json:"stale_token"`
		FreshWorktrees     bool   `json:"fresh_worktrees"`
		// InvalidTokens는 설정에 적혔지만 herdr 규칙에 맞지 않아 버린 이름들이다.
		// 사이드바에 아무것도 뜨지 않을 때 여기부터 보면 된다.
		InvalidTokens []string `json:"invalid_token_names,omitempty"`
		// ConfigTOML은 herdr 자신의 설정 파일이다. 아래 두 값은 그 파일을 훑어 얻는다.
		// 파일이 없으면 둘 다 false 이며, 그때는 setup 이 내놓는 것을 붙여 넣으면 된다.
		ConfigTOML        string `json:"config_toml"`
		ConfigTOMLError   string `json:"config_toml_error,omitempty"`
		SidebarConfigured bool   `json:"sidebar_configured"`
		KeyBound          bool   `json:"key_bound"`
	}
	out := report{
		Version:            version,
		DaemonAlive:        store.DaemonAlive(daemon.LockStaleAfter),
		GitVersion:         git.VersionText(ctx),
		MergeTreeSupported: git.SupportsMergeTree(ctx),
		StateDir:           store.Dir,
		LogPath:            store.LogPath(),
		ConfigDir:          config.Dir(),
		Enabled:            cfg.Enabled,
		Interval:           cfg.Interval.String(),
		Throttle:           cfg.Throttle.String(),
		FetchTimeout:       cfg.FetchTimeout.String(),
		StaleAfter:         cfg.StaleAfter.String(),
		BehindToken:        cfg.BehindToken,
		AheadToken:         cfg.AheadToken,
		GoneToken:          cfg.GoneToken,
		MergedToken:        cfg.MergedToken,
		CatchupToken:       cfg.CatchupToken,
		StaleToken:         cfg.StaleToken,
		FreshWorktrees:     cfg.FreshWorktrees,

		InvalidTokens: cfg.InvalidTokenNames(),

		ConfigTOML:        herdrpaths.ConfigFile(),
		SidebarConfigured: setup.SidebarConfigured(cfg, scan),
		KeyBound:          setup.KeyBound(scan),
	}
	if cfgErr != nil {
		out.ConfigError = cfgErr.Error()
	}
	if scanErr != nil {
		out.ConfigTOMLError = scanErr.Error()
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
	return state.New().DaemonAlive(daemon.LockStaleAfter)
}

func newLogger(w io.Writer) *slog.Logger {
	level := slog.LevelInfo
	// 문제를 쫓을 때 환경변수 하나로 상세 로그를 켤 수 있게 한다.
	if os.Getenv("HERDR_GIT_UPSTREAM_DEBUG") != "" {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}
