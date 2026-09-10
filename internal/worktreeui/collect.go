package worktreeui

// 이 파일은 표의 재료를 모은다. 어느 worktree 가 있는지(목록)와 worktree 마다 judge.Facts 를 채우는 일이다.
// herdr 호출은 목록과 agent_status 뿐이고 나머지는 전부 git 이다. 그래서 herdr 없이도 돌고, 시험도 herdr 없이 한다.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"herdr-git-upstream/internal/gitrepo"
	"herdr-git-upstream/internal/herdrcli"
	"herdr-git-upstream/internal/judge"
	"herdr-git-upstream/internal/state"
)

// maxParallel은 동시에 자료를 모을 worktree 수다. worktree 하나에 git 명령이 예닐곱 개 돌고 저장소에 따라
// 스물일곱 개가 있으므로 겹쳐 돌려야 화면이 곧 뜬다. 데몬의 fetch 상한과 같은 값이다. 그 이상 겹치면
// 디스크와 잠금에 부딪혀 오히려 느려진다.
const maxParallel = 4

// item은 목록의 한 항목이다. herdr 의 WorktreeItem 과 git 의 WorktreeEntry 를 같은 모양으로 맞춘 것이라,
// 두 길로 얻은 목록을 하나의 코드가 판정한다.
type item struct {
	Path     string
	Branch   string
	IsMain   bool
	Detached bool
	Locked   bool
	Prunable bool
	// OpenWorkspaceID는 herdr 로 얻은 목록에만 있다.
	OpenWorkspaceID string
}

// inventory는 목록과 그것이 어느 저장소의 것인지다.
type inventory struct {
	RepoName string
	// Main은 본 체크아웃의 저장소다. fetch 와 `git worktree remove` 는 여기서 돈다. 지우려는 worktree 안에서
	// 지우면 윈도우에서 실패하기 때문이다(gitrepo.RemoveWorktree 의 주석).
	Main  gitrepo.Repo
	Items []item
	// HerdrAvailable이 거짓이면 herdr 에 닿지 못해 git 만으로 만든 목록이다. OpenWorkspaceID 는 전부 비어 있다.
	HerdrAvailable bool
}

// notRepository는 시작한 자리가 git 저장소가 아니라는 오류다. 명령이 stderr 에 그대로 적는다.
// gitrepo.ErrNotRepository 로도 맞는다. 원인이 그것이기 때문이다.
type notRepository struct{ dir string }

func (e notRepository) Error() string { return "not a git repository: " + e.dir }

func (notRepository) Is(target error) bool { return target == gitrepo.ErrNotRepository }

// resolve는 어느 저장소의 어떤 worktree 들인지 정한다.
//
// herdr 가 답하면 그 목록이다. 어느 워크스페이스에 열려 있는지를 herdr 만 알기 때문이다. herdr 에 닿지 않으면
// `git worktree list` 로 대신한다. 서버가 명시적으로 거절한 요청은 다른 저장소로 바꾸지 않고 그 오류를 돌려준다.
// 어느 길이든 잠김은 git 에게 묻는다. herdr 목록에는 없는 정보다.
func resolve(ctx context.Context, herdr Herdr, git gitrepo.Runner, workspaceID, cwd string) (inventory, error) {
	if herdr != nil {
		res, err := herdr.WorktreeList(ctx, workspaceID, cwd)
		if err == nil {
			return fromHerdr(ctx, git, res, cwd)
		}
		var rejected *herdrcli.ResponseError
		if errors.As(err, &rejected) {
			return inventory{}, err
		}
	}
	return fromGit(ctx, git, cwd)
}

// fromHerdr는 herdr 의 목록을 inventory 로 옮긴다. 본 체크아웃은 is_linked_worktree 가 거짓인 항목이다.
func fromHerdr(ctx context.Context, git gitrepo.Runner, res herdrcli.WorktreeListResult, cwd string) (inventory, error) {
	mainPath := res.Source.SourceCheckoutPath
	if mainPath == "" {
		mainPath = res.Source.RepoRoot
	}
	main, err := git.Discover(ctx, mainPath)
	if err != nil {
		// herdr 는 저장소라고 했는데 git 이 그 자리를 찾지 못한다. herdr 가 기억하는 경로가 낡은 경우다.
		// 시작한 자리에서 본 체크아웃을 다시 찾아보고, 그것도 아니면 저장소가 아닌 것으로 답한다.
		if main, err = mainCheckout(ctx, git, cwd); err != nil {
			return inventory{}, err
		}
	}
	locked := lockedPaths(ctx, git, main)
	inv := inventory{RepoName: res.Source.RepoName, Main: main, HerdrAvailable: true}
	if inv.RepoName == "" {
		inv.RepoName = filepath.Base(main.Root)
	}
	for _, wt := range res.Worktrees {
		// bare 항목은 작업 트리가 없어 판정할 것도 지울 것도 없다.
		if wt.IsBare {
			continue
		}
		it := item{
			Path:            wt.Path,
			Branch:          wt.Branch,
			IsMain:          !wt.IsLinkedWorktree,
			Detached:        wt.IsDetached,
			Locked:          locked[canonical(wt.Path)],
			Prunable:        wt.IsPrunable,
			OpenWorkspaceID: wt.OpenWorkspaceID,
		}
		// 본 체크아웃의 워크스페이스는 목록 항목이 아니라 source 에 적혀 있을 수 있다. Enter 가 그리로 가려면 필요하다.
		if it.IsMain && it.OpenWorkspaceID == "" {
			it.OpenWorkspaceID = res.Source.SourceWorkspaceID
		}
		inv.Items = append(inv.Items, it)
	}
	return inv, nil
}

// mainCheckout은 dir 가 속한 저장소의 본 체크아웃을 찾는다. 연결된 worktree 에서 시작해도 삭제와 fetch 는 본
// 체크아웃에서 돌아야 한다. 본 체크아웃은 `git worktree list` 의 첫 항목이다(gitrepo.Worktrees 의 약속). 첫 항목이
// bare 저장소면 본 체크아웃이 없는 저장소라, 그때는 시작한 자리를 그대로 쓴다.
func mainCheckout(ctx context.Context, git gitrepo.Runner, dir string) (gitrepo.Repo, error) {
	repo, err := git.Discover(ctx, dir)
	if err != nil {
		return gitrepo.Repo{}, notRepository{dir: dir}
	}
	entries, err := git.Worktrees(ctx, repo)
	if err != nil {
		return gitrepo.Repo{}, err
	}
	if len(entries) == 0 {
		return gitrepo.Repo{}, notRepository{dir: dir}
	}
	if !entries[0].Bare {
		if found, err := git.Discover(ctx, entries[0].Path); err == nil {
			return found, nil
		}
	}
	return repo, nil
}

// fromGit은 `git worktree list` 로 inventory 를 만든다.
func fromGit(ctx context.Context, git gitrepo.Runner, cwd string) (inventory, error) {
	main, err := mainCheckout(ctx, git, cwd)
	if err != nil {
		return inventory{}, err
	}
	entries, err := git.Worktrees(ctx, main)
	if err != nil {
		return inventory{}, err
	}
	inv := inventory{RepoName: filepath.Base(entries[0].Path), Main: main}
	for i, entry := range entries {
		if entry.Bare {
			continue
		}
		inv.Items = append(inv.Items, item{
			Path:     entry.Path,
			Branch:   entry.Branch,
			IsMain:   i == 0,
			Detached: entry.Detached,
			Locked:   entry.Locked,
			Prunable: entry.Prunable,
		})
	}
	return inv, nil
}

// lockedPaths는 잠긴 worktree 의 경로들이다. 읽지 못하면 아무것도 잠기지 않은 것으로 본다. 잠김은 git 이
// 삭제를 거절하는 마지막 문턱이기도 하므로, 여기서 놓쳐도 지워지지는 않는다.
func lockedPaths(ctx context.Context, git gitrepo.Runner, repo gitrepo.Repo) map[string]bool {
	entries, err := git.Worktrees(ctx, repo)
	if err != nil {
		return nil
	}
	locked := map[string]bool{}
	for _, entry := range entries {
		if entry.Locked {
			locked[canonical(entry.Path)] = true
		}
	}
	return locked
}

// canonical은 같은 자리를 가리키는 경로를 하나의 문자열로 모은다. herdr 와 git 이 같은 worktree 를 심볼릭 링크
// 앞뒤의 다른 경로로 부를 수 있다(macOS 의 /var 와 /private/var).
func canonical(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// agentStatuses는 herdr 가 에이전트 상태를 알려 준 워크스페이스들이다. false 도 확인한 비작업 상태다.
// 조회 실패나 누락으로 키가 없는 열린 워크스페이스는 상태를 모르는 것이므로 지우지 않는다.
func agentStatuses(ctx context.Context, herdr Herdr, inv inventory) map[string]bool {
	if herdr == nil || !inv.HerdrAvailable {
		return nil
	}
	workspaces, err := herdr.WorkspaceList(ctx)
	if err != nil {
		return nil
	}
	working := map[string]bool{}
	for _, ws := range workspaces {
		working[ws.WorkspaceID] = ws.AgentWorking()
	}
	return working
}

// integration은 원격별 통합 브랜치들이다. 같은 저장소의 worktree 들도 서로 다른 원격을 따라갈 수 있다.
// 본 체크아웃에서 로컬 자료를 한 번씩 구하고, 각 행은 자신의 upstream 으로 고른 원격의 결과를 쓴다.
type integration struct {
	ByRemote map[string]judge.Integration
	// LookupRemotes는 원격 기본 브랜치를 물을 원격들이다. 네트워크 호출은 fetch 단계가 맡는다.
	LookupRemotes []string
}

func integrationFor(ctx context.Context, git gitrepo.Runner, store state.Store, main gitrepo.Repo, remote string, now time.Time) integration {
	remotes, _ := git.Remotes(ctx, main)
	// 원격 목록을 읽지 못해도 이미 고른 fetch 원격의 자료는 모을 수 있다.
	if remote != "" {
		remotes = append(remotes, remote)
	}
	result := integration{ByRemote: make(map[string]judge.Integration, len(remotes))}
	for _, name := range remotes {
		if _, found := result.ByRemote[name]; found {
			continue
		}
		// 풀지 못한 mergeTarget 이 있어도 나머지로 판정한다. 판정이 빠질 뿐 틀리지는 않는다.
		found, _ := judge.IntegrationBranches(ctx, git, main, name, store, now)
		result.ByRemote[name] = found
		if found.LookupDue {
			result.LookupRemotes = append(result.LookupRemotes, name)
		}
	}
	return result
}

// deps는 자료를 모을 때 쓰는, 화면이 사는 동안 바뀌지 않는 것들이다. 배경 고루틴이 이것만 읽으므로 잠금이 필요 없다.
type deps struct {
	Git   gitrepo.Runner
	Store state.Store
	// Remote는 fetch 하는 원격이다. ls-remote 결과(heads)는 이 원격의 것이라, 다른 원격을 따라가는 브랜치의
	// gone 은 그것으로 판정하지 않는다.
	Remote string
	// Spawn은 고루틴을 띄우는 방법이다. 터미널 안에서는 패닉에도 터미널을 되돌리는 tui.Terminal.Go 다.
	Spawn func(func())
}

// collect는 worktree 마다 재료를 모아 판정한 행들을 정렬해 돌려준다.
//
// heads 는 `git ls-remote --heads` 의 결과다. 있으면 gone 은 그것으로 판정하고, 없으면(첫 그리기, ls-remote
// 실패) 데몬의 fetch 기록으로 판정한다. 기록도 없으면 모르는 것이고, 모르는 것은 gone 이 아니다.
// targets 는 integrationFor 가 원격별로 구한 통합 브랜치들이다. 각 행의 원격에 맞는 결과를 쓴다.
func collect(ctx context.Context, d deps, inv inventory, agents map[string]bool, heads map[string]bool, targets integration, now time.Time) []Row {
	rows := make([]Row, len(inv.Items))
	limit := make(chan struct{}, maxParallel)
	var wait sync.WaitGroup
	for i, it := range inv.Items {
		wait.Add(1)
		limit <- struct{}{}
		d.Spawn(func() {
			defer wait.Done()
			defer func() { <-limit }()
			rows[i] = rowFor(ctx, d, it, agents, heads, targets, now)
		})
	}
	wait.Wait()
	SortRows(rows)
	return rows
}

// rowFor는 worktree 하나의 재료를 모아 판정한다.
//
// 막힌 것(본 체크아웃, 잠김, 사라짐, 에이전트 작업 중)은 git 에 묻지 않는다. Assess 는 막힌 이유만 말하고 나머지
// 사실은 보지 않으므로 모아 봐야 버려진다. 사라진 디렉터리는 물을 수도 없다.
//
// 모르는 것은 모른다고 둔다. git status 가 실패하면 UntouchedKnown 이 거짓이고, Assess 는 그것을 지우지 않는
// 쪽(review)으로 읽는다.
func rowFor(ctx context.Context, d deps, it item, agents, heads map[string]bool, targets integration, now time.Time) Row {
	row := Row{Path: it.Path, Branch: it.Branch, OpenWorkspaceID: it.OpenWorkspaceID, IsMain: it.IsMain}
	facts := judge.Facts{
		IsMain:       it.IsMain,
		Locked:       it.Locked,
		Prunable:     it.Prunable,
		AgentWorking: it.OpenWorkspaceID != "" && agents[it.OpenWorkspaceID],
	}
	if _, known := agents[it.OpenWorkspaceID]; it.OpenWorkspaceID != "" && !known && !facts.IsMain && !facts.Locked && !facts.Prunable {
		// 조회 실패를 "에이전트가 쉬는 중"으로 해석하면 삭제의 보호 조건이 사라진다.
		row.Verdict = judge.Blocked
		row.Detail = "agent status unknown"
		return row
	}
	if !facts.IsMain && !facts.Locked && !facts.Prunable && !facts.AgentWorking {
		row.Head = fillFacts(ctx, d, it, heads, targets, now, &facts)
	}
	verdict, pieces := judge.Assess(facts)
	row.Verdict = verdict
	row.Detail = strings.Join(pieces, ", ")
	return row
}

// fillFacts는 git 에 물어 판정의 재료를 채우고 그 판정에 사용한 HEAD 를 돌려준다.
// 데몬의 tokensFor 와 같은 순서, 같은 판정 함수다. HEAD 를 다시 읽지 않아 행의 커밋과 판정이 어긋나지 않는다.
func fillFacts(ctx context.Context, d deps, it item, heads map[string]bool, targets integration, now time.Time, facts *judge.Facts) string {
	git := d.Git
	repo, err := git.Discover(ctx, it.Path)
	if err != nil {
		return ""
	}
	if untouched, err := git.IsUntouched(ctx, repo); err == nil {
		facts.Untouched, facts.UntouchedKnown = untouched, true
	}
	// 커밋이 하나도 없는 저장소는 셀 것도 판정할 것도 없다.
	head, err := git.HeadCommit(ctx, repo)
	if err != nil || head == "" {
		return ""
	}

	var upstream *gitrepo.Upstream
	if it.Branch != "" {
		if up, err := git.UpstreamFor(ctx, repo, it.Branch); err == nil {
			upstream = &up
		}
	}
	if upstream != nil {
		if counts, err := git.CountsBetween(ctx, repo, head, upstream.TrackingRef); err == nil {
			facts.Ahead, facts.Behind, facts.CountsKnown = counts.Ahead, counts.Behind, true
		}
		facts.Gone, facts.GoneKnown = gone(d, repo, *upstream, heads)
		if facts.CountsKnown && facts.Behind > 0 {
			facts.Catchup = catchup(ctx, d, repo, head, *upstream, now)
		}
	}
	// merged 는 upstream 이 없어도 판정한다. 방금 갈라져 나온 빈 브랜치도 통합 브랜치의 조상이면 끝난 일이다.
	// 원격 기본 브랜치를 모르면 판정하지 않는다. 통합 브랜치 자체를 체크아웃한 자리를 가려낼 수 없기 때문이다.
	remote, _ := judge.RemoteFor(ctx, git, repo, upstream)
	branches := targets.ByRemote[remote]
	if branches.DefaultKnown {
		refs := make([]string, 0, len(branches.Branches))
		for _, branch := range branches.Branches {
			refs = append(refs, branch.TrackingRef)
		}
		self := judge.SelfRefs(ctx, git, repo, it.Branch, upstream)
		// 실패해도 merged 는 놓쳐도 되는 신호다. 잡은 것만 쓴다.
		merged, _ := judge.JudgeMerged(ctx, git, repo, head, self, refs)
		facts.Merged = merged.Yes
	}
	return head
}

// gone은 upstream 브랜치가 원격에서 사라졌는지 답한다. 두 번째 값은 알 수 있었는지다.
//
// ls-remote 결과가 있고 그 원격의 것이면 그것이 답이다. 지금 원격에 무엇이 있는지를 직접 본 것이라 가장 믿을
// 만하다. 없으면 데몬의 fetch 기록을 본다. 데몬은 열린 워크스페이스만 돌므로 기록이 없는 worktree 가 많고,
// 그때는 모른다.
func gone(d deps, repo gitrepo.Repo, up gitrepo.Upstream, heads map[string]bool) (isGone, known bool) {
	if heads != nil && up.Remote == d.Remote {
		return !heads[up.RemoteRef], true
	}
	record := d.Store.LoadRecord(state.Key(up.FetchKey(repo.CommonDir)))
	if record.LastAttemptUnix == 0 {
		return false, false
	}
	return judge.Gone(record), true
}

// catchup은 따라잡을 때 충돌하는지 판정한다. 데몬과 같은 기록을 나눠 쓰므로 데몬이 이미 판정한 쌍은 다시
// 계산하지 않고, 화면이 새로 계산한 답은 데몬이 다음 회차에 쓴다.
func catchup(ctx context.Context, d deps, repo gitrepo.Repo, head string, up gitrepo.Upstream, now time.Time) judge.Catchup {
	trackingCommit, err := d.Git.CommitOf(ctx, repo, up.TrackingRef)
	if err != nil {
		return judge.CatchupUnknown
	}
	key := state.CatchupKey(up.FetchKey(repo.CommonDir), up.Branch)
	record := d.Store.LoadCatchupRecord(key)
	result, updated, _ := judge.JudgeCatchup(ctx, d.Git, repo, head, trackingCommit, record, now)
	if updated != record {
		// 남기지 못하면 다음에 한 번 더 계산할 뿐이다. 화면에 보일 답은 이미 손에 있다.
		_ = d.Store.SaveCatchupRecord(key, updated)
	}
	return result
}

// remoteFor는 화면이 fetch 할 원격을 본 체크아웃에서 고른다. 그 브랜치의 upstream 이 있으면 그 원격, 없으면
// judge.RemoteFor 의 규칙(하나뿐이면 그것, 아니면 origin)이다. 고를 수 없으면 비어 있고 그때는 fetch 하지 않는다.
func remoteFor(ctx context.Context, git gitrepo.Runner, main gitrepo.Repo) string {
	var upstream *gitrepo.Upstream
	if up, err := git.Upstream(ctx, main); err == nil {
		upstream = &up
	}
	remote, _ := judge.RemoteFor(ctx, git, main, upstream)
	return remote
}
