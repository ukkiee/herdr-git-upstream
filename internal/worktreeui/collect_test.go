package worktreeui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"herdr-git-upstream/internal/gitrepo"
	"herdr-git-upstream/internal/herdrcli"
)

func TestResolvePreservesRejectedWorkspace(t *testing.T) {
	f := newFixture(t)
	h := newFakeHerdr(f)
	h.listErr = &herdrcli.ResponseError{Code: "workspace_not_found", Message: "workspace missing not found"}
	_, err := resolve(context.Background(), h, f.git, "missing", f.work)
	var rejected *herdrcli.ResponseError
	if !errors.As(err, &rejected) || rejected.Code != "workspace_not_found" {
		t.Fatalf("서버의 거절을 herdr unavailable 로 숨기면 안 된다: %v", err)
	}
}

// herdr 없이 git 만으로 목록을 만들고, worktree 마다 판정이 표(docs/PLAN.md)대로 나오는지 본다.
// 병합된 것은 safe, 손댄 것은 review, 본 체크아웃과 잠긴 것과 사라진 것은 blocked, 나머지는 keep 이다.
func TestCollectWithoutHerdr(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.safeWorktree(t, "done")
	f.dirtyWorktree(t, "dirty")
	locked := f.addWorktree(t, "locked")
	run(t, f.work, "git", "worktree", "lock", locked)
	missing := f.addWorktree(t, "missing")
	if err := os.RemoveAll(missing); err != nil {
		t.Fatal(err)
	}
	// 진행 중인 작업. 올렸고, 원격에 남이 커밋 하나를 더 얹었다.
	behind := f.addWorktree(t, "behind")
	f.commitIn(t, behind, "behind.txt", "1")
	f.pushed(t, behind, "behind")
	run(t, f.seed, "git", "fetch", "--quiet", "origin")
	run(t, f.seed, "git", "checkout", "--quiet", "-b", "behind", "origin/behind")
	writeFile(t, filepath.Join(f.seed, "other.txt"), "2")
	run(t, f.seed, "git", "add", ".")
	run(t, f.seed, "git", "commit", "--quiet", "-m", "other")
	run(t, f.seed, "git", "push", "--quiet", "origin", "behind")
	// 원격에서 지워진 브랜치. 추적 참조는 prune 하지 않아 남아 있다.
	gone := f.addWorktree(t, "gone")
	f.commitIn(t, gone, "gone.txt", "1")
	f.pushed(t, gone, "gone")
	run(t, f.seed, "git", "push", "--quiet", "origin", "--delete", "gone")
	// 분리된 HEAD. 브랜치가 없어 디렉터리 이름으로 보인다. main 의 커밋 위라 origin/main 이 품으므로 merged 다.
	run(t, f.work, "git", "worktree", "add", "--quiet", "--detach", filepath.Join(f.base, "wt", "detached"), "main")
	f.fetch(t)

	inv, err := resolve(ctx, nil, f.git, "", f.work)
	if err != nil {
		t.Fatal(err)
	}
	if inv.HerdrAvailable || inv.RepoName != "work" || inv.Main.Root != f.main.Root {
		t.Fatalf("git 만으로 만든 목록이어야 한다: %+v", inv)
	}
	if len(inv.Items) != 8 || !inv.Items[0].IsMain || inv.Items[0].Branch != "main" {
		t.Fatalf("본 체크아웃이 첫 항목이어야 한다: %+v", inv.Items)
	}
	for _, it := range inv.Items {
		if it.OpenWorkspaceID != "" {
			t.Fatalf("herdr 없이는 열려 있음 정보가 없어야 한다: %+v", it)
		}
	}

	d := f.deps()
	targets := integrationFor(ctx, f.git, f.store, inv.Main, "origin", time.Now())
	origin := targets.ByRemote["origin"]
	if !origin.DefaultKnown || len(origin.Branches) != 1 || origin.Branches[0].TrackingRef != "refs/remotes/origin/main" {
		t.Fatalf("clone 한 저장소는 원격 기본 브랜치를 알아야 한다: %+v", targets)
	}

	// ls-remote 결과 없이. gone 은 데몬 기록도 없어 모르므로 keep 이다.
	rows := collect(ctx, d, inv, nil, nil, targets, time.Now())
	catchup := "↓1"
	if f.git.SupportsMergeTree(ctx) {
		catchup = "↓1, clean catch-up"
	}
	expectRow(t, rows, "main", "blocked", "main checkout")
	expectRow(t, rows, "locked", "blocked", "locked")
	expectRow(t, rows, "missing", "blocked", "missing")
	expectRow(t, rows, "done", "safe", "merged")
	expectRow(t, rows, "(detached) detached", "safe", "merged")
	expectRow(t, rows, "dirty", "review", "dirty")
	expectRow(t, rows, "behind", "keep", catchup)
	expectRow(t, rows, "gone", "keep", "up to date")

	// 정렬: safe, review, keep, blocked 다음 이름.
	var order []string
	for _, row := range rows {
		order = append(order, row.Label())
	}
	want := "(detached) detached,done,dirty,behind,gone,locked,main,missing"
	if got := strings.Join(order, ","); got != want {
		t.Fatalf("정렬이 다르다:\n%s\n기대값\n%s", got, want)
	}
	for _, row := range rows {
		if row.IsMain != (row.Branch == "main") {
			t.Fatalf("IsMain 은 본 체크아웃에만: %+v", row)
		}
		if row.Path == "" {
			t.Fatalf("경로가 비어 있다: %+v", row)
		}
	}

	// ls-remote 결과가 있으면 gone 은 그것으로 판정한다. 앞선 커밋이 0 이므로 safe 다.
	heads, err := f.git.RemoteHeads(ctx, f.main, "origin")
	if err != nil {
		t.Fatal(err)
	}
	rows = collect(ctx, d, inv, nil, heads, targets, time.Now())
	expectRow(t, rows, "gone", "safe", "gone")
	expectRow(t, rows, "behind", "keep", catchup)
	expectRow(t, rows, "done", "safe", "merged")

	// 다른 원격의 ls-remote 결과는 쓰지 않는다. 그때는 기록으로 돌아가고, 기록이 없으니 모른다.
	other := d
	other.Remote = "elsewhere"
	rows = collect(ctx, other, inv, nil, heads, targets, time.Now())
	expectRow(t, rows, "gone", "keep", "up to date")

	// 따라잡기 판정은 데몬과 같은 기록에 남아, 다음에는 다시 계산하지 않는다.
	if f.git.SupportsMergeTree(ctx) {
		entries, err := os.ReadDir(filepath.Join(f.store.Root, "catchup"))
		if err != nil || len(entries) != 1 {
			t.Fatalf("따라잡기 기록이 하나 있어야 한다: %v %d", err, len(entries))
		}
	}
}

// 에이전트가 일하는 워크스페이스의 worktree 는 끝난 일이어도 blocked 다.
func TestCollectAgentWorkingBlocks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	done := f.safeWorktree(t, "done")
	f.fetch(t)
	herdr := newFakeHerdr(f)
	herdr.open(done, "wDONE")
	herdr.open(f.work, "wMAIN")

	inv, err := resolve(ctx, herdr, f.git, "", f.work)
	if err != nil {
		t.Fatal(err)
	}
	if !inv.HerdrAvailable || inv.RepoName != "fake" {
		t.Fatalf("herdr 의 목록이어야 한다: %+v", inv)
	}
	items := map[string]item{}
	for _, it := range inv.Items {
		items[it.Branch] = it
	}
	if items["done"].OpenWorkspaceID != "wDONE" || items["main"].OpenWorkspaceID != "wMAIN" || !items["main"].IsMain || items["done"].IsMain {
		t.Fatalf("열려 있음과 본 체크아웃이 다르다: %+v", inv.Items)
	}
	targets := integrationFor(ctx, f.git, f.store, inv.Main, "origin", time.Now())

	rows := collect(ctx, f.deps(), inv, agentStatuses(ctx, herdr, inv), nil, targets, time.Now())
	expectRow(t, rows, "done", "safe", "merged")

	herdr.working["wDONE"] = true
	rows = collect(ctx, f.deps(), inv, agentStatuses(ctx, herdr, inv), nil, targets, time.Now())
	expectRow(t, rows, "done", "blocked", "agent working")
	if row := byLabel(rows)["done"]; row.OpenWorkspaceID != "wDONE" {
		t.Fatalf("행은 워크스페이스 id 를 들고 있어야 한다: %+v", row)
	}
}

type unavailableAgentStatuses struct{ Herdr }

func (unavailableAgentStatuses) WorkspaceList(context.Context) ([]herdrcli.Workspace, error) {
	return nil, errors.New("workspace list unavailable")
}

// 열린 워크스페이스의 에이전트 상태를 못 읽으면 손대지 않은 merged worktree 도 지우지 않는다.
func TestCollectBlocksUnknownAgentStatus(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	done := f.safeWorktree(t, "done")
	f.fetch(t)
	herdr := newFakeHerdr(f)
	herdr.open(done, "wDONE")
	inv, err := resolve(ctx, herdr, f.git, "", f.work)
	if err != nil {
		t.Fatal(err)
	}
	targets := integrationFor(ctx, f.git, f.store, inv.Main, "origin", time.Now())
	agents := agentStatuses(ctx, unavailableAgentStatuses{herdr}, inv)
	rows := collect(ctx, f.deps(), inv, agents, nil, targets, time.Now())
	expectRow(t, rows, "done", "blocked", "agent status unknown")

	// 목록은 성공했어도 해당 워크스페이스가 빠져 있으면 같은 미확인 상태다.
	rows = collect(ctx, f.deps(), inv, map[string]bool{"other": false}, nil, targets, time.Now())
	expectRow(t, rows, "done", "blocked", "agent status unknown")
}

// herdr 의 목록에는 잠김이 없으므로 git 에게 따로 묻고, 경로가 심볼릭 링크 앞뒤로 달라도 짝을 맞춘다.
// bare 항목은 뺀다. 본 체크아웃의 워크스페이스는 source 에서 가져온다.
func TestFromHerdrMergesLockedAndSkipsBare(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	locked := f.addWorktree(t, "locked")
	run(t, f.work, "git", "worktree", "lock", locked)
	plain := f.addWorktree(t, "plain")

	res := herdrcli.WorktreeListResult{
		Source: herdrcli.WorktreeSource{RepoName: "mfe", RepoRoot: f.work, SourceCheckoutPath: f.work, SourceWorkspaceID: "wMAIN"},
		Worktrees: []herdrcli.WorktreeItem{
			{Path: f.work, Branch: "main", OpenWorkspaceID: ""},
			{Path: filepath.Join(f.base, "bare.git"), IsBare: true, IsLinkedWorktree: true},
			// herdr 가 심볼릭 링크를 푼 경로로 부르는 경우. git 은 만들 때의 경로를 기억한다.
			{Path: canonical(locked), Branch: "locked", IsLinkedWorktree: true},
			{Path: plain, Branch: "plain", IsLinkedWorktree: true, OpenWorkspaceID: "wP", IsPrunable: true},
		},
	}
	inv, err := fromHerdr(ctx, f.git, res, f.work)
	if err != nil {
		t.Fatal(err)
	}
	if inv.RepoName != "mfe" || !inv.HerdrAvailable || inv.Main.Root != f.main.Root {
		t.Fatalf("목록의 머리가 다르다: %+v", inv)
	}
	want := []item{
		{Path: f.work, Branch: "main", IsMain: true, OpenWorkspaceID: "wMAIN"},
		{Path: canonical(locked), Branch: "locked", Locked: true},
		{Path: plain, Branch: "plain", Prunable: true, OpenWorkspaceID: "wP"},
	}
	if len(inv.Items) != len(want) {
		t.Fatalf("항목 수 %d, 기대값 %d: %+v", len(inv.Items), len(want), inv.Items)
	}
	for i, it := range inv.Items {
		if it != want[i] {
			t.Fatalf("%d 번째 항목이 다르다:\n%+v\n기대값\n%+v", i, it, want[i])
		}
	}

	// herdr 가 기억하는 본 체크아웃 경로가 낡았으면 시작한 자리로 다시 찾는다.
	res.Source.SourceCheckoutPath = filepath.Join(f.base, "no-such")
	res.Source.RepoRoot = ""
	if inv, err := fromHerdr(ctx, f.git, res, plain); err != nil || inv.Main.Root != f.main.Root {
		t.Fatalf("시작한 자리로 다시 찾아야 한다: %v %+v", err, inv)
	}
	if _, err := fromHerdr(ctx, f.git, res, t.TempDir()); err == nil || !errors.Is(err, gitrepo.ErrNotRepository) {
		t.Fatalf("어디에서도 찾지 못하면 저장소가 아니다: %v", err)
	}
}

// herdr 에 닿지 않으면 git 으로 대신하고, 저장소가 아니면 그렇다고 말한다.
func TestResolveFallsBackToGit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.addWorktree(t, "a")
	herdr := newFakeHerdr(f)
	herdr.listErr = errors.New("herdr: connection refused")

	// 연결된 worktree 에서 시작해도 본 체크아웃을 찾는다.
	inv, err := resolve(ctx, herdr, f.git, "w1", filepath.Join(f.base, "wt", "a"))
	if err != nil {
		t.Fatal(err)
	}
	if inv.HerdrAvailable || inv.Main.Root != f.main.Root || len(inv.Items) != 2 || !inv.Items[0].IsMain {
		t.Fatalf("git 으로 대신한 목록이어야 한다: %+v", inv)
	}

	empty := t.TempDir()
	_, err = resolve(ctx, herdr, f.git, "", empty)
	if err == nil || err.Error() != "not a git repository: "+empty || !errors.Is(err, gitrepo.ErrNotRepository) {
		t.Fatalf("저장소가 아니라는 오류여야 한다: %v", err)
	}
}

// fetch 할 원격은 본 체크아웃의 upstream 에서 고른다. 없으면 하나뿐인 원격이고, 그것도 없으면 비어 있다.
func TestRemoteFor(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if got := remoteFor(ctx, f.git, f.main); got != "origin" {
		t.Fatalf("clone 한 main 의 원격은 origin 이어야 한다: %q", got)
	}
	run(t, f.work, "git", "remote", "rename", "origin", "up")
	if got := remoteFor(ctx, f.git, f.main); got != "up" {
		t.Fatalf("이름을 바꿔도 upstream 의 원격을 따라야 한다: %q", got)
	}
	run(t, f.work, "git", "branch", "--unset-upstream")
	if got := remoteFor(ctx, f.git, f.main); got != "up" {
		t.Fatalf("upstream 이 없으면 하나뿐인 원격이어야 한다: %q", got)
	}
	run(t, f.work, "git", "remote", "remove", "up")
	if got := remoteFor(ctx, f.git, f.main); got != "" {
		t.Fatalf("원격이 없으면 비어 있어야 한다: %q", got)
	}
}

// 본 체크아웃과 다른 원격의 기본 브랜치를 보는 worktree 도 통합 브랜치 자체로 보호한다.
// team/feature 가 develop 을 품고 있어도 develop 은 끝난 작업이 아니다(ADR 0001).
func TestCollectUsesEachWorktreesUpstreamRemote(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	team := filepath.Join(f.base, "team.git")
	run(t, f.base, "git", "clone", "--quiet", "--bare", f.remote, team)
	run(t, f.work, "git", "remote", "add", "team", team)
	develop := f.addWorktree(t, "develop")
	f.commitIn(t, develop, "develop.txt", "team work")
	run(t, develop, "git", "push", "--quiet", "--set-upstream", "team", "develop")
	run(t, team, "git", "symbolic-ref", "HEAD", "refs/heads/develop")
	run(t, f.work, "git", "push", "--quiet", "team", "develop:feature")
	run(t, f.work, "git", "fetch", "--quiet", "team")
	run(t, f.work, "git", "remote", "set-head", "team", "--auto")

	inv, err := resolve(ctx, nil, f.git, "", f.work)
	if err != nil {
		t.Fatal(err)
	}
	targets := integrationFor(ctx, f.git, f.store, inv.Main, "origin", time.Now())
	rows := collect(ctx, f.deps(), inv, nil, nil, targets, time.Now())
	expectRow(t, rows, "develop", "keep", "up to date")
}

// 삭제 직전 재검증이 같은 커밋을 확인할 수 있도록 행에 판정 대상 HEAD 를 함께 보존한다.
func TestCollectPreservesAssessmentHead(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	done := f.safeWorktree(t, "done")
	f.fetch(t)
	repo, err := f.git.Discover(ctx, done)
	if err != nil {
		t.Fatal(err)
	}
	want, err := f.git.HeadCommit(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := resolve(ctx, nil, f.git, "", f.work)
	if err != nil {
		t.Fatal(err)
	}
	targets := integrationFor(ctx, f.git, f.store, inv.Main, "origin", time.Now())
	rows := collect(ctx, f.deps(), inv, nil, nil, targets, time.Now())
	row := byLabel(rows)["done"]
	if row.Head != want {
		t.Fatalf("판정 HEAD %q, 기대값 %q", row.Head, want)
	}
}
