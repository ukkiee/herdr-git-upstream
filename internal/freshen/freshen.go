// Package freshen은 갓 만들어진 worktree를 원격의 최신 상태로 맞춘다.
//
// herdr는 worktree를 만들 때 fetch를 하지 않고, 기준 커밋으로 원본 체크아웃의 HEAD를 그대로 쓴다
// (herdr src/app/api/worktrees/deferred.rs의 `params.base.unwrap_or_else(|| "HEAD".into())`).
// 그래서 손에 쥔 main이 원격보다 다섯 커밋 뒤처져 있으면, 방금 만든 worktree도 다섯 커밋 뒤처진
// 자리에서 시작한다. 새 작업을 낡은 바닥 위에 쌓는 셈이라 나중에 병합할 때 값을 치르게 된다.
//
// 참조를 부지런히 갱신하는 것만으로는 이 문제가 풀리지 않는다. fetch는 refs/remotes/*를 움직일 뿐
// 로컬 브랜치의 HEAD는 그대로 두기 때문이다. 그래서 만들어진 직후에 한 번 앞으로 감아 준다.
package freshen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"herdr-git-upstream/internal/config"
	"herdr-git-upstream/internal/gitrepo"
	"herdr-git-upstream/internal/state"
)

// event는 herdr가 HERDR_PLUGIN_EVENT_JSON으로 넘겨주는 봉투 중 필요한 부분이다.
type event struct {
	Event string `json:"event"`
	Data  struct {
		Worktree struct {
			Path       string `json:"path"`
			Branch     string `json:"branch"`
			IsDetached bool   `json:"is_detached"`
			IsBare     bool   `json:"is_bare"`
		} `json:"worktree"`
	} `json:"data"`
}

// ErrSkipped는 손댈 상황이 아니어서 아무것도 하지 않았다는 뜻이다. 실패가 아니다.
var ErrSkipped = errors.New("맞출 상황이 아니다")

// Result는 무엇을 했는지 알려 준다.
type Result struct {
	Path        string
	Branch      string
	TrackingRef string
	// Moved가 참이면 실제로 앞으로 감았다. 거짓이면 이미 최신이었거나 손댈 자리가 아니었다.
	Moved bool
}

// WorktreeCreated는 worktree.created 이벤트를 처리한다.
//
// 이 훅만은 예외적으로 제자리에서 네트워크를 탄다. 다른 훅과 달리 자주 일어나지 않고, 무엇보다
// 사용자가 방금 worktree를 만들고 그 안에서 일을 시작하려는 참이다. 데몬에 넘겨 몇 초 뒤에 바닥이
// 바뀌면, 이미 파일을 열어 둔 사람에게는 그 편이 더 놀랍다.
func WorktreeCreated(ctx context.Context, cfg config.Resolved, log *slog.Logger) (Result, error) {
	if !cfg.Enabled || !cfg.FreshWorktrees {
		return Result{}, ErrSkipped
	}

	var parsed event
	raw := os.Getenv("HERDR_PLUGIN_EVENT_JSON")
	if raw == "" {
		return Result{}, ErrSkipped
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return Result{}, fmt.Errorf("이벤트를 해석하지 못했다: %w", err)
	}
	info := parsed.Data.Worktree
	// 브랜치가 없는(분리된 HEAD) worktree나 bare 저장소는 앞으로 감을 대상이 아니다.
	if info.Path == "" || info.Branch == "" || info.IsDetached || info.IsBare {
		return Result{}, ErrSkipped
	}

	git := gitrepo.Runner{Timeout: cfg.FetchTimeout}
	repo, err := git.Discover(ctx, info.Path)
	if err != nil {
		return Result{}, err
	}
	protected, err := state.New().FresheningSuppressed(repo.CommonDir, info.Branch)
	if err != nil {
		return Result{}, fmt.Errorf("생성 보호 기록을 확인하지 못했다: %w", err)
	}
	if protected {
		if err := state.New().CompleteFresheningSuppression(repo.CommonDir, info.Branch); err != nil {
			return Result{}, fmt.Errorf("생성 완료 기록을 남기지 못했다: %w", err)
		}
		return Result{}, ErrSkipped
	}

	// 갓 만든 worktree라면 깨끗해야 한다. 그렇지 않다면 우리가 아는 상황이 아니므로 손대지 않는다.
	clean, err := git.IsClean(ctx, repo)
	if err != nil {
		return Result{}, err
	}
	if !clean {
		log.Debug("작업 트리가 깨끗하지 않아 건너뛴다", "path", info.Path)
		return Result{}, ErrSkipped
	}

	// 갈라져 나온 기준 브랜치를 찾는다. 새 브랜치 자신은 후보에서 뺀다.
	base, err := git.BaseUpstream(ctx, repo, info.Branch)
	if err != nil {
		log.Debug("기준 브랜치를 찾지 못해 건너뛴다", "path", info.Path, "branch", info.Branch)
		return Result{}, ErrSkipped
	}

	if err := git.Fetch(ctx, repo, base); err != nil {
		// 가져오지 못했으면 옮길 근거가 없다. worktree는 만들어진 그대로 쓸 수 있으므로
		// 사용자의 작업을 막지 않고 물러난다.
		return Result{}, fmt.Errorf("기준 브랜치를 가져오지 못했다: %w", err)
	}

	before, err := git.HeadCommit(ctx, repo)
	if err != nil {
		return Result{}, err
	}
	// 빨리 감기만 한다. 새 브랜치에 이미 커밋이 있다면 실패하는데, 그때는 아무것도 하지 않는 것이 옳다.
	if err := git.FastForward(ctx, repo, base.TrackingRef); err != nil {
		log.Debug("빨리 감기가 되지 않아 그대로 둔다", "path", info.Path, "error", err)
		return Result{Path: info.Path, Branch: info.Branch, TrackingRef: base.TrackingRef}, ErrSkipped
	}
	after, err := git.HeadCommit(ctx, repo)
	if err != nil {
		return Result{}, err
	}

	return Result{
		Path:        info.Path,
		Branch:      info.Branch,
		TrackingRef: base.TrackingRef,
		Moved:       before != after,
	}, nil
}
