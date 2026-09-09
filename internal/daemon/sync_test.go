package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"herdr-git-upstream/internal/config"
	"herdr-git-upstream/internal/gitrepo"
	"herdr-git-upstream/internal/herdrcli"
	"herdr-git-upstream/internal/state"
)

// 아래 시험들은 herdr 없이 돈다. 페인 목록을 손으로 만들어 resolveTargets 에 넣고, fetch 와 토큰
// 계산까지만 본다. 원격은 로컬 bare 저장소라 네트워크를 타지 않는다.

// 저장소가 아닌 워크스페이스에도 여섯 토큰을 모두 빈 값으로 보내야, 예전에 올린 값이 남지 않는다.
func TestTokensForNonRepositoryClearsEveryToken(t *testing.T) {
	s := newTestSyncer(t)
	items := s.resolveTargets(context.Background(), []herdrcli.Pane{{WorkspaceID: "w1", CWD: t.TempDir()}}, nil, "")
	if len(items) != 1 || items[0].HasRepo {
		t.Fatalf("저장소가 아니어야 한다: %+v", items)
	}
	tokens := s.tokensFor(context.Background(), items[0], time.Now())
	expectTokens(t, tokens, map[string]string{
		"behind": "", "ahead": "", "sync_stale": "", "gone": "", "merged": "", "catchup": "",
	})
}

// 통합 브랜치를 체크아웃한 본 체크아웃은 아무 판정에도 걸리지 않아야 한다.
// main 에서 갈라져 나간 원격 브랜치가 있어도(실제 저장소에는 수십 개다) 그것은 근거가 아니다.
func TestTokensForUpToDateMainIsQuiet(t *testing.T) {
	s := newTestSyncer(t)
	f := newRepoFixture(t)
	run(t, f.seed, "git", "checkout", "--quiet", "-b", "feature")
	run(t, f.seed, "git", "commit", "--quiet", "--allow-empty", "-m", "feature")
	run(t, f.seed, "git", "push", "--quiet", "origin", "feature")
	run(t, f.work, "git", "fetch", "--quiet", "origin")

	item := s.resolveOne(t, f.work)
	if !item.Resolved || !item.HasRepo || item.Remote != "origin" || item.Branch != "main" {
		t.Fatalf("main 은 upstream 이 있어야 한다: %+v", item)
	}
	if len(item.Integration) != 1 || item.Integration[0].TrackingRef != "refs/remotes/origin/main" {
		t.Fatalf("통합 브랜치는 origin/main 하나여야 한다: %+v", item.Integration)
	}
	s.fetchAll(context.Background(), []target{item}, true)
	// 현재 브랜치가 곧 통합 브랜치면 FetchKey 가 같아 한 번만 가져간다. fetch 기록 파일이 하나여야 한다.
	if item.Upstream.FetchKey(item.Repo.CommonDir) != item.Integration[0].FetchKey(item.Repo.CommonDir) {
		t.Fatalf("upstream 과 통합 브랜치의 FetchKey 가 같아야 한다: %q != %q", item.Upstream.FetchKey(item.Repo.CommonDir), item.Integration[0].FetchKey(item.Repo.CommonDir))
	}
	if got := fetchRecordCount(t, s); got != 1 {
		t.Fatalf("같은 참조를 두 번 가져오면 안 된다. fetch 기록이 %d 개다", got)
	}
	tokens := s.tokensFor(context.Background(), item, time.Now())
	expectTokens(t, tokens, map[string]string{
		"behind": "", "ahead": "", "sync_stale": "", "gone": "", "merged": "", "catchup": "",
	})
}

// 일반 병합된 브랜치는 merged 라벨을 받는다.
func TestTokensForMergedBranch(t *testing.T) {
	s := newTestSyncer(t)
	f := newRepoFixture(t)
	f.startFeature(t, "feature")
	run(t, f.work, "git", "push", "--quiet", "--set-upstream", "origin", "feature")
	run(t, f.seed, "git", "fetch", "--quiet", "origin")
	run(t, f.seed, "git", "merge", "--quiet", "--no-ff", "-m", "merge", "origin/feature")
	run(t, f.seed, "git", "push", "--quiet", "origin", "main")

	item := s.resolveOne(t, f.work)
	// fetchAll 이 통합 브랜치(origin/main)도 가져와야 판정이 최신이 된다.
	s.fetchAll(context.Background(), []target{item}, true)
	tokens := s.tokensFor(context.Background(), item, time.Now())
	expectTokens(t, tokens, map[string]string{
		"behind": "", "ahead": "", "sync_stale": "", "gone": "", "merged": "merged", "catchup": "",
	})
}

// 원격에서 지워진 브랜치는 gone 라벨을 받는다. fetch 가 영구 실패로 기록되는 것이 근거다.
func TestTokensForGoneBranch(t *testing.T) {
	s := newTestSyncer(t)
	f := newRepoFixture(t)
	f.startFeature(t, "feature")
	run(t, f.work, "git", "push", "--quiet", "--set-upstream", "origin", "feature")
	run(t, f.seed, "git", "push", "--quiet", "origin", "--delete", "feature")

	item := s.resolveOne(t, f.work)
	s.fetchAll(context.Background(), []target{item}, true)
	tokens := s.tokensFor(context.Background(), item, time.Now())
	if tokens["gone"] != "gone" {
		t.Fatalf("지워진 브랜치는 gone 이어야 한다: %v", tokens)
	}
	if tokens["merged"] != "" {
		t.Fatalf("병합되지 않았으니 merged 는 비어야 한다: %v", tokens)
	}
	if tokens["sync_stale"] != "" {
		t.Fatalf("영구 실패는 stale 이 아니다: %v", tokens)
	}
}

// 뒤처졌고 같은 파일을 고쳤으면 catchup 이 충돌 라벨을 받는다. 다른 파일이면 비어 있다.
func TestTokensForCatchup(t *testing.T) {
	s := newTestSyncer(t)
	if !s.Git.SupportsMergeTree(context.Background()) {
		t.Skip("이 git 판은 merge-tree --write-tree 를 지원하지 않는다")
	}
	f := newRepoFixture(t)
	writeFile(t, filepath.Join(f.seed, "f"), "theirs")
	run(t, f.seed, "git", "commit", "--quiet", "-am", "theirs")
	run(t, f.seed, "git", "push", "--quiet", "origin", "main")

	t.Run("충돌", func(t *testing.T) {
		writeFile(t, filepath.Join(f.work, "f"), "mine")
		run(t, f.work, "git", "commit", "--quiet", "-am", "mine")
		item := s.resolveOne(t, f.work)
		s.fetchAll(context.Background(), []target{item}, true)
		fetchKey := state.Key(item.Upstream.FetchKey(item.Repo.CommonDir))
		fetched := s.Store.LoadRecord(fetchKey)
		tokens := s.tokensFor(context.Background(), item, time.Now())
		expectTokens(t, tokens, map[string]string{
			"behind": "↓1", "ahead": "↑1", "sync_stale": "", "gone": "", "merged": "", "catchup": "conflict",
		})
		// 판정이 기록에 남아 다음 회차가 merge-tree 를 다시 돌리지 않는다.
		record := s.Store.LoadCatchupRecord(catchupKey(item))
		if record.Result != "conflict" || record.Head != item.Head {
			t.Fatalf("catchup 판정이 기록에 남아야 한다: %+v", record)
		}
		// 판정 경로는 fetch 기록을 되쓰지 않는다. 되쓰면 같은 저장소를 도는 다른 세션의 데몬이 그 사이에
		// 남긴 fetch 결과를 낡은 값으로 덮는다.
		if got := s.Store.LoadRecord(fetchKey); got != fetched {
			t.Fatalf("토큰 계산이 fetch 기록을 바꿨다: %+v != %+v", got, fetched)
		}
	})
	t.Run("깨끗", func(t *testing.T) {
		run(t, f.work, "git", "reset", "--quiet", "--hard", "HEAD~1")
		writeFile(t, filepath.Join(f.work, "g"), "mine")
		run(t, f.work, "git", "add", ".")
		run(t, f.work, "git", "commit", "--quiet", "-m", "g")
		item := s.resolveOne(t, f.work)
		tokens := s.tokensFor(context.Background(), item, time.Now())
		expectTokens(t, tokens, map[string]string{
			"behind": "↓1", "ahead": "↑1", "sync_stale": "", "gone": "", "merged": "", "catchup": "",
		})
	})
}

// upstream 이 없는 새 브랜치도 merged 는 판정한다. origin/main 에서 방금 만든 빈 브랜치가 그 예다.
func TestTokensForFreshBranchWithoutUpstream(t *testing.T) {
	s := newTestSyncer(t)
	f := newRepoFixture(t)
	run(t, f.work, "git", "checkout", "--quiet", "--no-track", "-b", "fresh", "origin/main")

	item := s.resolveOne(t, f.work)
	if item.Resolved || !item.HasRepo || item.Remote != "origin" {
		t.Fatalf("upstream 은 없고 저장소와 원격은 있어야 한다: %+v", item)
	}
	tokens := s.tokensFor(context.Background(), item, time.Now())
	expectTokens(t, tokens, map[string]string{
		"behind": "", "ahead": "", "sync_stale": "", "gone": "", "merged": "merged", "catchup": "",
	})
}

// 통합 브랜치의 추적 참조가 낡으면 판정도 낡는다. 현재 브랜치가 아니어도 통합 브랜치는 가져와야 한다.
func TestFetchAllAlsoFetchesIntegrationBranches(t *testing.T) {
	s := newTestSyncer(t)
	f := newRepoFixture(t)
	f.startFeature(t, "feature")
	run(t, f.work, "git", "push", "--quiet", "--set-upstream", "origin", "feature")
	run(t, f.seed, "git", "commit", "--quiet", "--allow-empty", "-m", "main moves on")
	run(t, f.seed, "git", "push", "--quiet", "origin", "main")

	item := s.resolveOne(t, f.work)
	before := output(t, f.work, "git", "rev-parse", "refs/remotes/origin/main")
	s.fetchAll(context.Background(), []target{item}, true)
	after := output(t, f.work, "git", "rev-parse", "refs/remotes/origin/main")
	if before == after {
		t.Fatal("feature 브랜치에 있어도 origin/main 을 가져와야 한다")
	}
	if after != output(t, f.seed, "git", "rev-parse", "main") {
		t.Fatalf("origin/main 이 원격의 최신이어야 한다: %s", after)
	}
}

// push 만 한 브랜치는 자기 사본(refs/remotes/origin/<브랜치>)이 HEAD 를 품지만 그것은 끝난 작업이 아니다.
// upstream 이 없어도, upstream 이 통합 브랜치(origin/main)여도 마찬가지다.
func TestTokensForPushedOnlyBranchIsNotMerged(t *testing.T) {
	s := newTestSyncer(t)
	f := newRepoFixture(t)

	t.Run("upstream 없음", func(t *testing.T) {
		f.startFeature(t, "nou")
		run(t, f.work, "git", "push", "--quiet", "origin", "nou")
		item := s.resolveOne(t, f.work)
		if item.Resolved || item.Branch != "nou" {
			t.Fatalf("upstream 은 없고 브랜치 이름은 있어야 한다: %+v", item)
		}
		s.fetchAll(context.Background(), []target{item}, true)
		tokens := s.tokensFor(context.Background(), item, time.Now())
		expectTokens(t, tokens, map[string]string{
			"behind": "", "ahead": "", "sync_stale": "", "gone": "", "merged": "", "catchup": "",
		})
	})
	t.Run("upstream 이 origin/main", func(t *testing.T) {
		run(t, f.work, "git", "checkout", "--quiet", "-b", "feature", "origin/main")
		if got := output(t, f.work, "git", "config", "--get", "branch.feature.merge"); got != "refs/heads/main" {
			t.Skipf("이 git 판은 원격 추적 참조에서 만든 브랜치에 upstream 을 잡지 않는다: %q", got)
		}
		writeFile(t, filepath.Join(f.work, "feature.txt"), "feature")
		run(t, f.work, "git", "add", ".")
		run(t, f.work, "git", "commit", "--quiet", "-m", "feature")
		run(t, f.work, "git", "push", "--quiet", "origin", "feature")
		item := s.resolveOne(t, f.work)
		if !item.Resolved || item.Upstream.TrackingRef != "refs/remotes/origin/main" {
			t.Fatalf("upstream 은 origin/main 이어야 한다: %+v", item)
		}
		s.fetchAll(context.Background(), []target{item}, true)
		tokens := s.tokensFor(context.Background(), item, time.Now())
		expectTokens(t, tokens, map[string]string{
			"behind": "", "ahead": "↑1", "sync_stale": "", "gone": "", "merged": "", "catchup": "",
		})
	})
}

// PR 참조 사양(+refs/pull/*/head:refs/remotes/origin/pr/*)이 있으면 열린 자기 PR 의 head 가 HEAD 를 품지만,
// 그것은 브랜치가 아니므로 merged 의 근거가 아니다.
func TestTokensForPullRequestRefIsNotMerged(t *testing.T) {
	s := newTestSyncer(t)
	f := newRepoFixture(t)
	f.startFeature(t, "my-feature")
	run(t, f.work, "git", "push", "--quiet", "--set-upstream", "origin", "my-feature")
	run(t, f.remote, "git", "update-ref", "refs/pull/7/head", "refs/heads/my-feature")
	run(t, f.work, "git", "config", "--add", "remote.origin.fetch", "+refs/pull/*/head:refs/remotes/origin/pr/*")
	run(t, f.work, "git", "fetch", "--quiet", "origin")
	// 전제: PR 참조가 로컬에 비쳐 있고 HEAD 를 품는다.
	if output(t, f.work, "git", "rev-parse", "refs/remotes/origin/pr/7") != output(t, f.work, "git", "rev-parse", "HEAD") {
		t.Fatal("PR 참조가 HEAD 를 가리켜야 시험이 뜻을 갖는다")
	}

	item := s.resolveOne(t, f.work)
	s.fetchAll(context.Background(), []target{item}, true)
	tokens := s.tokensFor(context.Background(), item, time.Now())
	expectTokens(t, tokens, map[string]string{
		"behind": "", "ahead": "", "sync_stale": "", "gone": "", "merged": "", "catchup": "",
	})
}

// 빈 원격을 clone 한 직후에는 git 이 branch.main 을 잡아 두어 좁은 fetch 가 "원격 참조 없음"으로 실패하지만,
// 태어나지 않은 브랜치는 사라질 수 없다. gone 이 붙으면 안 된다.
func TestTokensForEmptyRemoteCloneIsNotGone(t *testing.T) {
	s := newTestSyncer(t)
	base := t.TempDir()
	remote := filepath.Join(base, "empty.git")
	work := filepath.Join(base, "work")
	run(t, base, "git", "init", "--quiet", "--bare", "--initial-branch=main", remote)
	run(t, base, "git", "clone", "--quiet", remote, work)

	item := s.resolveOne(t, work)
	if !item.HasRepo || item.Head != "" {
		t.Fatalf("저장소이지만 커밋은 없어야 한다: %+v", item)
	}
	if !item.Resolved {
		t.Skip("이 git 판은 빈 저장소를 clone 할 때 upstream 을 잡지 않는다")
	}
	s.fetchAll(context.Background(), []target{item}, true)
	record := s.Store.LoadRecord(state.Key(item.Upstream.FetchKey(item.Repo.CommonDir)))
	if !record.LastErrorPermanent {
		t.Skipf("이 git 판은 없는 참조의 fetch 를 영구 실패로 내지 않는다: %+v", record)
	}
	tokens := s.tokensFor(context.Background(), item, time.Now())
	expectTokens(t, tokens, map[string]string{
		"behind": "", "ahead": "", "sync_stale": "", "gone": "", "merged": "", "catchup": "",
	})
}

// HEAD 가 분리되어 있으면 upstream 이 없으므로 merged 만 판정한다. README 가 그렇게 약속한다.
func TestTokensForDetachedHeadJudgesMergedOnly(t *testing.T) {
	s := newTestSyncer(t)
	f := newRepoFixture(t)
	run(t, f.work, "git", "checkout", "--quiet", "--detach", "origin/main")

	item := s.resolveOne(t, f.work)
	if item.Resolved || !item.HasRepo || item.Branch != "" || item.Head == "" {
		t.Fatalf("분리된 HEAD 는 브랜치도 upstream 도 없어야 한다: %+v", item)
	}
	tokens := s.tokensFor(context.Background(), item, time.Now())
	expectTokens(t, tokens, map[string]string{
		"behind": "", "ahead": "", "sync_stale": "", "gone": "", "merged": "merged", "catchup": "",
	})
}

// origin/HEAD 가 없는 저장소는 원격 기본 브랜치를 원격에 물어야 하는데, 그 왕복은 fetch 와 같은 자리에서
// 한다. 알아낸 답은 fetch 뒤 refreshIntegration 이 다시 읽어 같은 회차의 판정에 쓴다.
func TestFetchAllLooksUpDefaultBranchInTheFetchSlot(t *testing.T) {
	s := newTestSyncer(t)
	f := newRepoFixture(t)
	run(t, f.work, "git", "remote", "set-head", "origin", "--delete")

	targets := []target{s.resolveOne(t, f.work)}
	if first := targets[0]; len(first.Integration) != 0 || first.DefaultKnown || !first.LookupDefaultBranch {
		t.Fatalf("resolveTargets 는 기본 브랜치를 모른 채 물어볼 것으로 표시해야 한다: %+v", first)
	}
	s.fetchAll(context.Background(), targets, false)
	s.refreshIntegration(context.Background(), targets)
	if refreshed := targets[0]; len(refreshed.Integration) != 1 || refreshed.Integration[0].TrackingRef != "refs/remotes/origin/main" || !refreshed.DefaultKnown {
		t.Fatalf("같은 회차에 기록에서 기본 브랜치를 다시 읽어야 한다: %+v", refreshed)
	}

	second := s.resolveOne(t, f.work)
	if len(second.Integration) != 1 || second.LookupDefaultBranch || !second.DefaultKnown {
		t.Fatalf("다음 회차는 기록에서 기본 브랜치를 읽고 다시 묻지 않는다: %+v", second)
	}
}

// 원격 기본 브랜치를 모르는 저장소(origin/HEAD 없음)에서 통합 브랜치 자체를 체크아웃한 자리(main 위의 main)는
// 첫 회차에도 merged 가 아니어야 한다. 물어서 알아냈으면 그 답을 같은 회차에 쓰고, 원격이 닿지 않아 끝내
// 모르면 판정하지 않는다. 모르는 채 판정하면 main 에서 갈라져 나간 원격 브랜치가 HEAD 를 품어 merged 가 붙는다.
func TestTokensForMainWithoutRemoteHeadIsNotMergedOnFirstSweep(t *testing.T) {
	s := newTestSyncer(t)
	f := newRepoFixture(t)
	run(t, f.seed, "git", "checkout", "--quiet", "-b", "feature")
	run(t, f.seed, "git", "commit", "--quiet", "--allow-empty", "-m", "feature")
	run(t, f.seed, "git", "push", "--quiet", "origin", "feature")
	run(t, f.work, "git", "fetch", "--quiet", "origin")
	run(t, f.work, "git", "remote", "set-head", "origin", "--delete")
	// 전제: 통합 브랜치를 모르면 조상 검사가 갈라져 나간 브랜치에 걸린다.
	if output(t, f.work, "git", "for-each-ref", "--format=%(refname)", "--contains", "HEAD", "refs/remotes/origin/feature") == "" {
		t.Fatal("origin/feature 가 HEAD 를 품어야 시험이 뜻을 갖는다")
	}

	t.Run("물어서 알아냄", func(t *testing.T) {
		targets := []target{s.resolveOne(t, f.work)}
		if !targets[0].LookupDefaultBranch || targets[0].DefaultKnown {
			t.Fatalf("첫 회차는 원격에 물어야 한다: %+v", targets[0])
		}
		s.fetchAll(context.Background(), targets, true)
		s.refreshIntegration(context.Background(), targets)
		tokens := s.tokensFor(context.Background(), targets[0], time.Now())
		expectTokens(t, tokens, map[string]string{
			"behind": "", "ahead": "", "sync_stale": "", "gone": "", "merged": "", "catchup": "",
		})
	})

	t.Run("원격이 닿지 않아 끝내 모름", func(t *testing.T) {
		s := newTestSyncer(t)
		run(t, f.work, "git", "remote", "set-url", "origin", filepath.Join(f.base, "no-such-remote.git"))
		targets := []target{s.resolveOne(t, f.work)}
		s.fetchAll(context.Background(), targets, true)
		s.refreshIntegration(context.Background(), targets)
		if targets[0].DefaultKnown {
			t.Fatalf("닿지 않는 원격의 기본 브랜치를 알 수는 없다: %+v", targets[0])
		}
		tokens := s.tokensFor(context.Background(), targets[0], time.Now())
		if tokens["merged"] != "" {
			t.Fatalf("통합 브랜치를 모르면 merged 를 판정하지 않아야 한다: %v", tokens)
		}
	})
}

// 같은 추적 참조를 따라가는 브랜치가 둘이면(feature1, feature2 → origin/main) 따라잡기 캐시는 브랜치마다
// 따로여야 한다. 하나로 두면 회차마다 서로의 캐시를 지워 merge-tree 가 매번 다시 돈다.
func TestCatchupCacheIsPerBranch(t *testing.T) {
	s := newTestSyncer(t)
	if !s.Git.SupportsMergeTree(context.Background()) {
		t.Skip("이 git 판은 merge-tree --write-tree 를 지원하지 않는다")
	}
	f := newRepoFixture(t)
	writeFile(t, filepath.Join(f.seed, "f"), "theirs")
	run(t, f.seed, "git", "commit", "--quiet", "-am", "theirs")
	run(t, f.seed, "git", "push", "--quiet", "origin", "main")

	var items []target
	for _, branch := range []string{"feature1", "feature2"} {
		run(t, f.work, "git", "checkout", "--quiet", "-b", branch, "main")
		run(t, f.work, "git", "branch", "--quiet", "--set-upstream-to=origin/main", branch)
		writeFile(t, filepath.Join(f.work, branch+".txt"), branch)
		run(t, f.work, "git", "add", ".")
		run(t, f.work, "git", "commit", "--quiet", "-m", branch)
		items = append(items, s.resolveOne(t, f.work))
	}
	if items[0].Upstream.FetchKey(items[0].Repo.CommonDir) != items[1].Upstream.FetchKey(items[1].Repo.CommonDir) {
		t.Fatal("두 브랜치의 fetch 열쇠는 같아야 시험이 뜻을 갖는다")
	}
	s.fetchAll(context.Background(), items, true)

	for _, item := range items {
		if got := s.tokensFor(context.Background(), item, time.Now())["catchup"]; got != "" {
			t.Fatalf("%s 는 다른 파일만 고쳤으니 깨끗해야 한다: %q", item.Branch, got)
		}
	}
	for _, item := range items {
		record := s.Store.LoadCatchupRecord(catchupKey(item))
		if record.Head != item.Head || record.Result != "clean" {
			t.Fatalf("%s 의 캐시가 다른 브랜치에 덮이면 안 된다: %+v", item.Branch, record)
		}
	}
}

// 뒤처짐과 앞섬은 회차 첫머리에 읽어 둔 HEAD 로 센다. 회차 도중 사용자가 브랜치를 바꿔도 같은 회차의
// merged, catchup 과 같은 커밋을 말해야 한다.
func TestTokensCountFromTheSnapshotHead(t *testing.T) {
	s := newTestSyncer(t)
	f := newRepoFixture(t)
	f.startFeature(t, "feature")
	run(t, f.work, "git", "push", "--quiet", "--set-upstream", "origin", "feature")
	run(t, f.seed, "git", "commit", "--quiet", "--allow-empty", "-m", "main moves on")
	run(t, f.seed, "git", "push", "--quiet", "origin", "main")
	run(t, f.work, "git", "checkout", "--quiet", "main")

	item := s.resolveOne(t, f.work)
	s.fetchAll(context.Background(), []target{item}, true)
	// 회차 도중 feature 로 옮겼다. 앞선 커밋이 하나인 브랜치다.
	run(t, f.work, "git", "checkout", "--quiet", "feature")
	tokens := s.tokensFor(context.Background(), item, time.Now())
	expectTokens(t, tokens, map[string]string{
		"behind": "↓1", "ahead": "", "sync_stale": "", "gone": "", "merged": "", "catchup": "",
	})
}

// 이름이 빈 토큰은 보고하지 않는다.
func TestTokensForSkipsEmptyNames(t *testing.T) {
	s := newTestSyncer(t)
	s.Config.GoneToken = ""
	s.Config.CatchupToken = ""
	items := s.resolveTargets(context.Background(), []herdrcli.Pane{{WorkspaceID: "w1", CWD: t.TempDir()}}, nil, "")
	tokens := s.tokensFor(context.Background(), items[0], time.Now())
	expectTokens(t, tokens, map[string]string{"behind": "", "ahead": "", "sync_stale": "", "merged": ""})
}

// --- 시험 도구 ---------------------------------------------------------------

func newTestSyncer(t *testing.T) *Syncer {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 이 없어 건너뛴다")
	}
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.FetchTimeout = 30 * time.Second
	dir := t.TempDir()
	return &Syncer{
		Config: cfg,
		Git:    gitrepo.Runner{Timeout: cfg.FetchTimeout},
		Store:  state.Store{Dir: dir, Root: dir},
		Log:    slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})),
	}
}

func (s *Syncer) resolveOne(t *testing.T, dir string) target {
	t.Helper()
	items := s.resolveTargets(context.Background(), []herdrcli.Pane{{WorkspaceID: "w1", CWD: dir}}, nil, "")
	if len(items) != 1 {
		t.Fatalf("워크스페이스 하나여야 한다: %d", len(items))
	}
	return items[0]
}

// fetchRecordCount는 fetch 기록 파일의 수를 센다. fetch 작업 하나가 기록 하나를 남기므로, 몇 번 가져갔는지의 증거다.
func fetchRecordCount(t *testing.T, s *Syncer) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(s.Store.Root, "fetch"))
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func expectTokens(t *testing.T, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("토큰 수가 다르다: %v, 기대값 %v", got, want)
	}
	for name, value := range want {
		actual, ok := got[name]
		if !ok {
			t.Fatalf("토큰 %q 가 빠졌다: %v", name, got)
		}
		if actual != value {
			t.Fatalf("토큰 %q = %q, 기대값 %q (전체 %v)", name, actual, value, got)
		}
	}
}

type repoFixture struct {
	base   string
	remote string
	seed   string
	work   string
}

func newRepoFixture(t *testing.T) *repoFixture {
	t.Helper()
	base := t.TempDir()
	f := &repoFixture{
		base:   base,
		remote: filepath.Join(base, "remote.git"),
		seed:   filepath.Join(base, "seed"),
		work:   filepath.Join(base, "work"),
	}
	run(t, base, "git", "init", "--quiet", "--bare", "--initial-branch=main", f.remote)
	run(t, base, "git", "clone", "--quiet", f.remote, f.seed)
	configure(t, f.seed)
	writeFile(t, filepath.Join(f.seed, "f"), "A")
	run(t, f.seed, "git", "add", ".")
	run(t, f.seed, "git", "commit", "--quiet", "-m", "A")
	run(t, f.seed, "git", "push", "--quiet", "origin", "main")
	run(t, base, "git", "clone", "--quiet", f.remote, f.work)
	configure(t, f.work)
	return f
}

func (f *repoFixture) startFeature(t *testing.T, branch string) {
	t.Helper()
	run(t, f.work, "git", "checkout", "--quiet", "-b", branch, "main")
	writeFile(t, filepath.Join(f.work, branch+".txt"), branch)
	run(t, f.work, "git", "add", ".")
	run(t, f.work, "git", "commit", "--quiet", "-m", branch)
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s 실패: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func output(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %s 실패: %v", name, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

func gitEnv() []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
	)
}

func configure(t *testing.T, dir string) {
	t.Helper()
	run(t, dir, "git", "config", "user.email", "t@example.invalid")
	run(t, dir, "git", "config", "user.name", "t")
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
