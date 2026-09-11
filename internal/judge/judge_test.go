package judge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"herdr-git-upstream/internal/gitrepo"
	"herdr-git-upstream/internal/state"
)

// 아래 시험들은 실제 저장소로 시나리오를 만든다. 원격은 로컬 bare 저장소라 네트워크를 타지 않는다.
// seed 는 "다른 사람"의 작업 사본이고, work 는 판정 대상인 우리 사본이다.

// (a) 일반 병합. 병합 커밋이 조상 관계를 남기므로 조상 검사로 잡힌다.
func TestMergedByAncestorAfterRegularMerge(t *testing.T) {
	f := newFixture(t)
	f.startFeature(t, "feature", "feature.txt", "일")
	f.pushFeature(t, "feature")
	run(t, f.seed, "git", "fetch", "--quiet", "origin")
	run(t, f.seed, "git", "merge", "--quiet", "--no-ff", "-m", "merge feature", "origin/feature")
	run(t, f.seed, "git", "push", "--quiet", "origin", "main")
	run(t, f.work, "git", "fetch", "--quiet", "origin")

	got := f.judgeMerged(t, f.self(t), []string{"refs/remotes/origin/main"})
	if !got.Yes || got.By != "ancestor:refs/remotes/origin/main" {
		t.Fatalf("일반 병합은 조상 검사로 잡혀야 한다: %+v", got)
	}
}

// (b) squash 병합. 조상 관계가 없으므로 merge-tree 비교로만 잡힌다.
func TestMergedByMergeTreeAfterSquashMerge(t *testing.T) {
	f := newFixture(t)
	f.requireMergeTree(t)
	f.startFeature(t, "feature", "feature.txt", "일")
	f.pushFeature(t, "feature")
	run(t, f.seed, "git", "fetch", "--quiet", "origin")
	run(t, f.seed, "git", "merge", "--quiet", "--squash", "origin/feature")
	run(t, f.seed, "git", "commit", "--quiet", "-m", "squash feature")
	run(t, f.seed, "git", "push", "--quiet", "origin", "main")
	run(t, f.work, "git", "fetch", "--quiet", "origin")

	// 전제: 조상 검사로는 잡히지 않는다.
	if got := f.judgeMerged(t, f.self(t), nil); got.Yes {
		t.Fatalf("squash 병합이 조상 검사로 잡히면 시험 준비가 잘못된 것이다: %+v", got)
	}
	got := f.judgeMerged(t, f.self(t), []string{"refs/remotes/origin/main"})
	if !got.Yes || got.By != "merge-tree:refs/remotes/origin/main" {
		t.Fatalf("squash 병합은 merge-tree 비교로 잡혀야 한다: %+v", got)
	}
}

// (c) 병합되지 않은 브랜치. push 는 했지만 자기 추적 참조는 근거가 아니다.
func TestUnmergedBranchIsNotMerged(t *testing.T) {
	f := newFixture(t)
	f.startFeature(t, "feature", "feature.txt", "일")
	f.pushFeature(t, "feature")
	run(t, f.work, "git", "fetch", "--quiet", "origin")

	got := f.judgeMerged(t, f.self(t), []string{"refs/remotes/origin/main"})
	if got.Yes {
		t.Fatalf("병합되지 않은 브랜치가 merged 로 나왔다: %+v", got)
	}
	// 자기 참조를 빼지 않으면 push 만 해도 merged 가 된다. 빼는 것이 맞는지 확인한다.
	if got := f.judgeMerged(t, Self{}, []string{"refs/remotes/origin/main"}); !got.Yes {
		t.Fatal("자기 참조를 빼지 않았다면 조상 검사에 걸렸어야 한다. 시험 전제가 어긋났다")
	}
}

// 넓은 참조 사양의 PR과 중복 목적지에 비친 자기 사본은 push만 했다는 뜻이다. 실제 통합 브랜치에
// 병합한 뒤에는 같은 사용자 참조 사양에서도 조상 검사로 merged가 되어야 한다.
func TestMergedWithCustomTrackingRefspecs(t *testing.T) {
	cases := []struct {
		name        string
		setup       func(t *testing.T, f *fixture)
		target      string
		localBranch string
	}{
		{
			name: "넓은 사양에 포함된 열린 PR",
			setup: func(t *testing.T, f *fixture) {
				run(t, f.work, "git", "config", "remote.origin.fetch", "+refs/*:refs/remotes/origin/*")
				run(t, f.work, "git", "push", "--quiet", "origin", "HEAD:refs/pull/1/head")
			},
			target: "refs/remotes/origin/heads/main",
		},
		{
			name: "자기 브랜치의 두 번째 추적 사본",
			setup: func(t *testing.T, f *fixture) {
				run(t, f.work, "git", "config", "--add", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/extra/*")
			},
			target: "refs/remotes/origin/main",
		},
		{
			name: "이름이 다른 upstream의 두 번째 추적 사본",
			setup: func(t *testing.T, f *fixture) {
				run(t, f.work, "git", "config", "--add", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/extra/*")
			},
			target:      "refs/remotes/origin/main",
			localBranch: "local-feature",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.startFeature(t, "feature", "feature.txt", "아직 병합하지 않은 일")
			tc.setup(t, f)
			f.pushFeature(t, "feature")
			if tc.localBranch != "" {
				run(t, f.work, "git", "branch", "-m", tc.localBranch)
			}
			run(t, f.work, "git", "fetch", "--quiet", "origin")
			if got := f.judgeMerged(t, f.self(t), []string{tc.target}); got.Yes {
				t.Fatalf("push만 한 브랜치가 merged로 나왔다: %+v", got)
			}

			run(t, f.seed, "git", "fetch", "--quiet", "origin")
			run(t, f.seed, "git", "merge", "--quiet", "--no-ff", "-m", "merge feature", "origin/feature")
			run(t, f.seed, "git", "push", "--quiet", "origin", "main")
			run(t, f.work, "git", "fetch", "--quiet", "origin")
			if got := f.judgeMerged(t, f.self(t), []string{tc.target}); !got.Yes || !strings.HasPrefix(got.By, "ancestor:") {
				t.Fatalf("실제 병합은 조상 검사로 잡혀야 한다: %+v", got)
			}
		})
	}
}

// push 만 한 브랜치는 자기 사본(refs/remotes/<원격>/<브랜치>)이 HEAD 를 품지만 그것은 "올렸다"는 뜻이지
// 끝난 작업이라는 뜻이 아니다. upstream 이 없거나 upstream 이 통합 브랜치(origin/main)인 브랜치가
// 자기 사본 때문에 merged 가 되면 거짓 양성이고, 그 값은 worktree 삭제의 재료가 되므로 틀리면 안 된다.
func TestPushedBranchIsNotMergedByItsOwnCopy(t *testing.T) {
	f := newFixture(t)
	fork := filepath.Join(f.base, "fork.git")
	run(t, f.base, "git", "init", "--quiet", "--bare", "--initial-branch=main", fork)
	run(t, f.work, "git", "remote", "add", "fork", fork)

	cases := []struct {
		name    string
		prepare func(t *testing.T)
		copies  string
	}{
		{
			name: "upstream 없이 push",
			prepare: func(t *testing.T) {
				f.startFeature(t, "nou", "nou.txt", "일")
				run(t, f.work, "git", "push", "--quiet", "origin", "nou")
			},
			copies: "refs/remotes/fork/nou,refs/remotes/origin/nou",
		},
		{
			name: "upstream 이 origin/main 인 채로 push",
			prepare: func(t *testing.T) {
				// 원격 추적 참조를 시작점으로 주면 git 이 upstream 을 그 브랜치로 잡는다.
				// herdr 의 `git worktree add -b <새> <경로> origin/<브랜치>` 흐름에서 흔히 생기는 모양이다.
				run(t, f.work, "git", "checkout", "--quiet", "-b", "feature", "origin/main")
				if got := output(t, f.work, "git", "config", "--get", "branch.feature.merge"); got != "refs/heads/main" {
					t.Skipf("이 git 판은 원격 추적 참조에서 만든 브랜치에 upstream 을 잡지 않는다: %q", got)
				}
				writeFile(t, filepath.Join(f.work, "feature.txt"), "일")
				run(t, f.work, "git", "add", ".")
				run(t, f.work, "git", "commit", "--quiet", "-m", "feature")
				run(t, f.work, "git", "push", "--quiet", "origin", "feature")
			},
			copies: "refs/remotes/fork/feature,refs/remotes/origin/feature",
		},
		{
			name: "다른 원격(fork)에만 push",
			prepare: func(t *testing.T) {
				f.startFeature(t, "forked", "forked.txt", "일")
				run(t, f.work, "git", "push", "--quiet", "fork", "forked")
			},
			copies: "refs/remotes/fork/forked,refs/remotes/origin/forked",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.prepare(t)
			self := f.self(t)
			// 원격의 순서는 git 이 정하므로 정렬해 견준다.
			copies := slices.Clone(self.Copies)
			slices.Sort(copies)
			if got := strings.Join(copies, ","); got != tc.copies {
				t.Fatalf("등록된 원격마다 자기 사본이 있어야 한다: %q, 기대값 %q", got, tc.copies)
			}
			got := f.judgeMerged(t, self, []string{"refs/remotes/origin/main"})
			if got.Yes {
				t.Fatalf("push 만 한 브랜치가 merged 로 나왔다: %+v", got)
			}
			// 전제: 자기 사본을 빼지 않으면 조상 검사에 걸린다.
			if got := f.judgeMerged(t, Self{Tracking: self.Tracking}, nil); !got.Yes {
				t.Fatalf("자기 사본이 HEAD 를 품고 있어야 시험이 뜻을 갖는다: %+v", got)
			}
		})
	}
}

// (d) squash 병합 뒤 통합 브랜치가 같은 파일을 다시 고쳤다. 병합해 보면 통합 브랜치와 다른 트리가
// 나오므로 놓친다. 이것이 ADR 0001 이 말하는 "놓칠 수 있어도 틀리지는 않는다"의 한계다.
func TestMergedIsMissedWhenIntegrationBranchTouchedTheSameFileAgain(t *testing.T) {
	f := newFixture(t)
	f.requireMergeTree(t)
	f.startFeature(t, "feature", "f", "feature version")
	f.pushFeature(t, "feature")
	run(t, f.seed, "git", "fetch", "--quiet", "origin")
	run(t, f.seed, "git", "merge", "--quiet", "--squash", "origin/feature")
	run(t, f.seed, "git", "commit", "--quiet", "-m", "squash feature")
	// 통합 브랜치가 같은 파일을 다시 고친다.
	writeFile(t, filepath.Join(f.seed, "f"), "changed again after merge")
	run(t, f.seed, "git", "commit", "--quiet", "-am", "touch f again")
	run(t, f.seed, "git", "push", "--quiet", "origin", "main")
	run(t, f.work, "git", "fetch", "--quiet", "origin")

	got := f.judgeMerged(t, f.self(t), []string{"refs/remotes/origin/main"})
	if got.Yes {
		t.Fatalf("이 경우는 놓치는 것이 알려진 한계다. 잡혔다면 판정 방식이 바뀐 것이니 문서를 고쳐야 한다: %+v", got)
	}
}

// (e) upstream 이 없는 새 브랜치. origin/dev 에서 방금 만들었으면 HEAD 가 origin/dev 의 조상이라 merged 다.
func TestFreshBranchWithoutUpstreamIsMergedWhenItIsAnAncestor(t *testing.T) {
	f := newFixture(t)
	run(t, f.seed, "git", "checkout", "--quiet", "-b", "dev")
	run(t, f.seed, "git", "commit", "--quiet", "--allow-empty", "-m", "dev")
	run(t, f.seed, "git", "push", "--quiet", "origin", "dev")
	run(t, f.work, "git", "fetch", "--quiet", "origin")
	run(t, f.work, "git", "checkout", "--quiet", "--no-track", "-b", "fresh", "origin/dev")

	got := f.judgeMerged(t, f.self(t), []string{"refs/remotes/origin/main"})
	if !got.Yes || got.By != "ancestor:refs/remotes/origin/dev" {
		t.Fatalf("origin/dev 의 조상인 새 브랜치는 merged 여야 한다: %+v", got)
	}
}

// 통합 브랜치에서 갈라져 나오며 upstream 까지 그 브랜치로 잡힌 새 브랜치(herdr 의 worktree 흐름)도
// merged 다. upstream 추적 참조는 조상 검사의 근거에서 빠지지만, 통합 브랜치이므로 merge-tree 비교가 잡는다.
func TestFreshBranchTrackingTheIntegrationBranchIsMerged(t *testing.T) {
	f := newFixture(t)
	f.requireMergeTree(t)
	run(t, f.work, "git", "checkout", "--quiet", "-b", "fresh", "origin/main")
	if got := output(t, f.work, "git", "config", "--get", "branch.fresh.merge"); got != "refs/heads/main" {
		t.Skipf("이 git 판은 원격 추적 참조에서 만든 브랜치에 upstream 을 잡지 않는다: %q", got)
	}

	self := f.self(t)
	if self.Tracking != "refs/remotes/origin/main" {
		t.Fatalf("upstream 은 origin/main 이어야 한다: %+v", self)
	}
	got := f.judgeMerged(t, self, []string{"refs/remotes/origin/main"})
	if !got.Yes || got.By != "merge-tree:refs/remotes/origin/main" {
		t.Fatalf("통합 브랜치에서 방금 만든 빈 브랜치는 merge-tree 비교로 merged 여야 한다: %+v", got)
	}
}

// 통합 브랜치를 체크아웃한 본 체크아웃(main 위의 main)은 merged 가 아니다.
// main 에서 갈라져 나간 브랜치가 원격에 하나라도 있으면 그 참조가 HEAD 를 품어 조상 검사에 걸리므로,
// 자기 사본이 통합 브랜치이면 아예 판정하지 않아야 그 행에 merged 가 붙지 않는다.
func TestCheckoutOfTheIntegrationBranchItselfIsNotMerged(t *testing.T) {
	f := newFixture(t)
	// main 에서 갈라져 나가 앞선 원격 브랜치를 하나 둔다. 실제 저장소에는 이런 브랜치가 수십 개다.
	run(t, f.seed, "git", "checkout", "--quiet", "-b", "feature")
	run(t, f.seed, "git", "commit", "--quiet", "--allow-empty", "-m", "feature")
	run(t, f.seed, "git", "push", "--quiet", "origin", "feature")
	run(t, f.work, "git", "fetch", "--quiet", "origin")

	self := f.self(t)
	if strings.Join(self.Copies, ",") != "refs/remotes/origin/main" || self.Tracking != "refs/remotes/origin/main" {
		t.Fatalf("main 의 자기 참조는 origin/main 이어야 한다: %+v", self)
	}
	got := f.judgeMerged(t, self, []string{"refs/remotes/origin/main"})
	if got.Yes {
		t.Fatalf("main 위의 main 이 merged 로 나왔다: %+v", got)
	}
	// 전제: 조기 반환이 없으면 갈라져 나간 브랜치가 근거가 된다.
	if got := f.judgeMerged(t, self, []string{"refs/remotes/origin/dev"}); !got.Yes || got.By != "ancestor:refs/remotes/origin/feature" {
		t.Fatalf("통합 브랜치가 아니라면 갈라져 나간 브랜치가 HEAD 를 품어 merged 여야 한다: %+v", got)
	}
}

// 아직 가져온 적 없는 통합 브랜치는 로컬에 없다. 그것 때문에 판정이 실패해서는 안 된다.
func TestMissingIntegrationBranchIsSkipped(t *testing.T) {
	f := newFixture(t)
	f.startFeature(t, "feature", "feature.txt", "일")
	got := f.judgeMerged(t, f.self(t), []string{"refs/remotes/origin/not-fetched-yet", "refs/remotes/origin/main"})
	if got.Yes {
		t.Fatalf("병합되지 않은 브랜치가 merged 로 나왔다: %+v", got)
	}
}

// (f) 따라잡기. 깨끗한 경우, 충돌하는 경우, 그리고 캐시 적중.
func TestCatchupCleanConflictAndCache(t *testing.T) {
	f := newFixture(t)
	f.requireMergeTree(t)
	ctx := context.Background()

	// 원격은 f 를 고쳤다.
	writeFile(t, filepath.Join(f.seed, "f"), "theirs")
	run(t, f.seed, "git", "commit", "--quiet", "-am", "theirs")
	run(t, f.seed, "git", "push", "--quiet", "origin", "main")
	run(t, f.work, "git", "fetch", "--quiet", "origin")
	tracking := f.commitOf(t, "refs/remotes/origin/main")
	now := time.Unix(1_000_000, 0)

	t.Run("다른 파일을 고쳤으면 깨끗", func(t *testing.T) {
		run(t, f.work, "git", "checkout", "--quiet", "-b", "clean", "main")
		writeFile(t, filepath.Join(f.work, "g"), "mine")
		run(t, f.work, "git", "add", ".")
		run(t, f.work, "git", "commit", "--quiet", "-m", "g")
		head := f.commitOf(t, "HEAD")

		result, record, err := JudgeCatchup(ctx, f.git, f.repo, head, tracking, state.CatchupRecord{}, now)
		if err != nil {
			t.Fatal(err)
		}
		if result != CatchupClean {
			t.Fatalf("깨끗해야 한다: %v", result)
		}
		want := state.CatchupRecord{Head: head, Tracking: tracking, Result: "clean", CheckedUnix: now.Unix()}
		if record != want {
			t.Fatalf("기록에 쌍과 답과 시각이 적혀야 한다: %+v, 기대값 %+v", record, want)
		}
	})

	t.Run("같은 파일을 고쳤으면 충돌", func(t *testing.T) {
		run(t, f.work, "git", "checkout", "--quiet", "-b", "conflict", "main")
		writeFile(t, filepath.Join(f.work, "f"), "mine")
		run(t, f.work, "git", "commit", "--quiet", "-am", "f")
		head := f.commitOf(t, "HEAD")

		result, record, err := JudgeCatchup(ctx, f.git, f.repo, head, tracking, state.CatchupRecord{}, now)
		if err != nil {
			t.Fatal(err)
		}
		if result != CatchupConflict {
			t.Fatalf("충돌해야 한다: %v", result)
		}
		if record.Result != "conflict" {
			t.Fatalf("기록이 다르다: %+v", record)
		}

		// 캐시 적중. 같은 쌍이면 git 을 부르지 않고 기록의 답을 그대로 쓴다. 기록에 일부러 반대 답을
		// 심어 두어, 다시 계산했다면 드러나게 한다. 기록 값이 그대로인 것도 함께 확인한다. clean 과
		// conflict 는 시각을 보지 않으므로 아주 오래된 기록이어도 그대로 쓴다.
		planted := state.CatchupRecord{Head: head, Tracking: tracking, Result: "clean", CheckedUnix: 1}
		result, after, err := JudgeCatchup(ctx, f.git, f.repo, head, tracking, planted, now.Add(365*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if result != CatchupClean {
			t.Fatalf("같은 쌍이면 기록의 답을 그대로 써야 한다: %v", result)
		}
		if after != planted {
			t.Fatalf("캐시 적중 때는 기록이 그대로여야 한다: %+v != %+v", after, planted)
		}

		// 쌍이 어긋나면 다시 계산한다.
		stale := state.CatchupRecord{Head: head, Tracking: "0000000000000000000000000000000000000000", Result: "clean"}
		result, _, err = JudgeCatchup(ctx, f.git, f.repo, head, tracking, stale, now)
		if err != nil {
			t.Fatal(err)
		}
		if result != CatchupConflict {
			t.Fatalf("쌍이 다르면 다시 계산해야 한다: %v", result)
		}
	})
}

// 판정하지 못한 쌍(관계없는 역사, 제한 시간 초과)도 기록에 남겨 한동안은 merge-tree 를 다시 돌리지 않는다.
// 판정이 안 되는 쌍일수록 비용이 큰 쪽이라 되풀이가 가장 아프다. 다만 일시적인 이유일 수 있으므로 영원히
// 쉬지는 않고 catchupRetry 가 지나면 다시 시도한다.
func TestCatchupUnknownIsRememberedForAWhile(t *testing.T) {
	f := newFixture(t)
	f.requireMergeTree(t)
	ctx := context.Background()
	// 관계없는 역사. merge-tree 는 "refusing to merge unrelated histories" 로 128 을 낸다.
	run(t, f.work, "git", "checkout", "--quiet", "--orphan", "island")
	run(t, f.work, "git", "commit", "--quiet", "--allow-empty", "-m", "island")
	head := f.commitOf(t, "HEAD")
	tracking := f.commitOf(t, "refs/remotes/origin/main")
	now := time.Unix(1_000_000, 0)

	result, record, err := JudgeCatchup(ctx, f.git, f.repo, head, tracking, state.CatchupRecord{}, now)
	if err == nil {
		t.Fatal("관계없는 역사는 판정할 수 없어야 한다")
	}
	want := state.CatchupRecord{Head: head, Tracking: tracking, Result: "unknown", CheckedUnix: now.Unix()}
	if result != CatchupUnknown || record != want {
		t.Fatalf("판정하지 못한 것도 쌍과 시각과 함께 남겨야 한다: %v %+v, 기대값 %+v", result, record, want)
	}

	// 한 시간 안에는 기록을 그대로 쓴다. 다시 돌렸다면 오류가 났을 것이다.
	result, after, err := JudgeCatchup(ctx, f.git, f.repo, head, tracking, record, now.Add(catchupRetry-time.Minute))
	if err != nil || result != CatchupUnknown || after != record {
		t.Fatalf("한동안은 다시 시도하지 않아야 한다: %v %v %+v", err, result, after)
	}

	// 간격이 지나면 다시 시도하고, 시각을 새로 적는다.
	later := now.Add(catchupRetry)
	result, after, err = JudgeCatchup(ctx, f.git, f.repo, head, tracking, record, later)
	if err == nil {
		t.Fatal("간격이 지나면 다시 시도해야 한다")
	}
	if result != CatchupUnknown || after.CheckedUnix != later.Unix() {
		t.Fatalf("다시 시도한 시각을 적어야 한다: %v %+v", result, after)
	}
}

func TestCatchupString(t *testing.T) {
	cases := []struct {
		value Catchup
		want  string
	}{
		{CatchupUnknown, "unknown"},
		{CatchupClean, "clean"},
		{CatchupConflict, "conflict"},
		{Catchup(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.value.String(); got != tc.want {
			t.Fatalf("%d -> %q, 기대값 %q", tc.value, got, tc.want)
		}
	}
	// 세 이름은 되읽힌다. "unknown" 도 기록이다. 판정하지 못했다는 사실을 남기는 것이기 때문이다.
	for _, tc := range cases[:3] {
		if got, ok := parseCatchup(tc.want); !ok || got != tc.value {
			t.Fatalf("%q -> %v %v, 기대값 %v", tc.want, got, ok, tc.value)
		}
	}
	for _, text := range []string{"", "뭔가"} {
		if _, ok := parseCatchup(text); ok {
			t.Fatalf("%q 는 판정한 적 없는 것으로 읽어야 한다", text)
		}
	}
}

// (g) 통합 브랜치는 원격 기본 브랜치와 mergeTarget 을 합친 것이고, 겹치면 하나만 남는다.
func TestIntegrationBranchesMergesDefaultAndTargets(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	store := state.Store{Dir: t.TempDir(), Root: t.TempDir()}
	run(t, f.work, "git", "config", "--add", "git-upstream.mergeTarget", "origin/widget-studio/dev")
	run(t, f.work, "git", "config", "--add", "git-upstream.mergeTarget", "origin/main")

	got, err := IntegrationBranches(ctx, f.git, f.repo, "origin", store, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"refs/remotes/origin/main", "refs/remotes/origin/widget-studio/dev"}
	if trackingRefs(got.Branches) != strings.Join(want, ",") {
		t.Fatalf("원격 기본 브랜치가 먼저, 그다음 mergeTarget, 중복은 하나: %v", trackingRefs(got.Branches))
	}
	for _, up := range got.Branches {
		if up.Remote != "origin" || up.RemoteRef == "" {
			t.Fatalf("fetch 할 수 있도록 원격과 원격 참조가 채워져야 한다: %+v", up)
		}
	}
	if !got.DefaultKnown || got.LookupDue {
		t.Fatalf("origin/HEAD 가 있으면 기본 브랜치를 알고, 원격에 물을 일이 없다: %+v", got)
	}
}

// 오타 난 mergeTarget 하나가 나머지 판정까지 막아서는 안 되지만, 그 사실은 오류로 돌려줘야 한다.
func TestIntegrationBranchesSkipsBadTargetButReportsIt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	store := state.Store{Dir: t.TempDir(), Root: t.TempDir()}
	run(t, f.work, "git", "config", "--add", "git-upstream.mergeTarget", "nowhere/dev")
	run(t, f.work, "git", "config", "--add", "git-upstream.mergeTarget", "origin/dev")

	got, err := IntegrationBranches(ctx, f.git, f.repo, "origin", store, time.Now())
	if err == nil {
		t.Fatal("등록되지 않은 원격은 오류로 알려야 한다")
	}
	if trackingRefs(got.Branches) != "refs/remotes/origin/main,refs/remotes/origin/dev" {
		t.Fatalf("멀쩡한 것은 남아야 한다: %v", trackingRefs(got.Branches))
	}
}

// origin/HEAD 가 없으면 IntegrationBranches 는 원격에 묻지 않고 기록만 본다. 묻는 일은 LookupDefaultBranch 가
// 따로 하며, 알아낸 답은 저장소 기록에 남아 그다음부터는 기록을 쓴다.
func TestDefaultBranchLookupIsSeparateFromIntegrationBranches(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	store := state.Store{Dir: t.TempDir(), Root: t.TempDir()}
	run(t, f.work, "git", "remote", "set-head", "origin", "--delete")

	got, err := IntegrationBranches(ctx, f.git, f.repo, "origin", store, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if trackingRefs(got.Branches) != "" || got.DefaultKnown {
		t.Fatalf("로컬에도 기록에도 없으면 기본 브랜치 없이 진행해야 한다: %+v", got)
	}
	if !got.LookupDue {
		t.Fatal("한 번도 물어본 적 없으면 물을 때다")
	}

	if err := LookupDefaultBranch(ctx, f.git, f.repo, "origin", store, time.Now()); err != nil {
		t.Fatalf("원격에 물어 기본 브랜치를 알아내지 못했다: %v", err)
	}
	record := store.LoadRepoRecord(repoKey(f.repo, "origin"))
	if record.DefaultRemoteRef != "refs/heads/main" || record.DefaultTrackingRef != "refs/remotes/origin/main" || record.DefaultCheckedUnix == 0 {
		t.Fatalf("알아낸 것을 저장소 기록에 남겨야 한다: %+v", record)
	}

	// 원격이 사라져도 기록이 있으니 같은 답이어야 한다. 매 회차 원격에 묻지 않는다는 뜻이다.
	run(t, f.work, "git", "remote", "set-url", "origin", filepath.Join(f.base, "no-such-remote.git"))
	got, err = IntegrationBranches(ctx, f.git, f.repo, "origin", store, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if trackingRefs(got.Branches) != "refs/remotes/origin/main" || !got.DefaultKnown || got.LookupDue {
		t.Fatalf("기록에 있는 값을 쓰고, 알아낸 뒤에는 다시 물을 일이 없다: %+v", got)
	}
}

// 원격에 닿지 못하면 시도한 시각만 남기고 기본 브랜치 없이 진행한다. 하루 안에는 다시 묻지 않는다.
func TestDefaultBranchLookupRemembersFailure(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	store := state.Store{Dir: t.TempDir(), Root: t.TempDir()}
	run(t, f.work, "git", "remote", "set-head", "origin", "--delete")
	run(t, f.work, "git", "remote", "set-url", "origin", filepath.Join(f.base, "no-such-remote.git"))
	run(t, f.work, "git", "config", "--add", "git-upstream.mergeTarget", "origin/dev")

	now := time.Now()
	if err := LookupDefaultBranch(ctx, f.git, f.repo, "origin", store, now); err == nil {
		t.Fatal("닿지 않는 원격은 오류여야 한다")
	}
	record := store.LoadRepoRecord(repoKey(f.repo, "origin"))
	if record.DefaultCheckedUnix != now.Unix() || record.DefaultTrackingRef != "" {
		t.Fatalf("실패해도 시도한 시각은 남겨야 한다: %+v", record)
	}
	got, err := IntegrationBranches(ctx, f.git, f.repo, "origin", store, now)
	if err != nil {
		t.Fatal(err)
	}
	if trackingRefs(got.Branches) != "refs/remotes/origin/dev" || got.DefaultKnown {
		t.Fatalf("기본 브랜치 없이 mergeTarget 만 남아야 한다: %+v", got)
	}

	// 하루 안에는 원격을 다시 살릴 수 있어도 묻지 않는다.
	run(t, f.work, "git", "remote", "set-url", "origin", f.remote)
	got, err = IntegrationBranches(ctx, f.git, f.repo, "origin", store, now.Add(23*time.Hour))
	if err != nil || got.LookupDue {
		t.Fatalf("하루 안에는 다시 묻지 않아야 한다: %v %+v", err, got)
	}
	// 하루가 지났으면 다시 묻는다.
	got, err = IntegrationBranches(ctx, f.git, f.repo, "origin", store, now.Add(25*time.Hour))
	if err != nil || !got.LookupDue {
		t.Fatalf("하루가 지나면 다시 물어야 한다: %v %+v", err, got)
	}
	if err := LookupDefaultBranch(ctx, f.git, f.repo, "origin", store, now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err = IntegrationBranches(ctx, f.git, f.repo, "origin", store, now.Add(25*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if trackingRefs(got.Branches) != "refs/remotes/origin/main,refs/remotes/origin/dev" || !got.DefaultKnown {
		t.Fatalf("다시 물어 알아낸 기본 브랜치가 앞에 와야 한다: %+v", got)
	}
}

// 원격이 기본 브랜치를 바꾼 뒤(master → main) 낡은 origin/HEAD 가 남으면 그것은 없는 참조를 가리킨다.
// 그것을 믿으면 없는 브랜치를 회차마다 가져오다 영구 실패만 쌓고 진짜 기본 브랜치는 영영 묻지 않으므로,
// 가리키는 참조가 실제로 있을 때만 안다고 답해야 한다.
func TestDanglingRemoteHeadIsNotKnown(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	store := state.Store{Dir: t.TempDir(), Root: t.TempDir()}
	run(t, f.work, "git", "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/master")
	// 전제: git 자체는 매달린 별명도 성공으로 읽는다.
	if got, err := f.git.RemoteHead(ctx, f.repo, "origin"); err != nil || got != "refs/remotes/origin/master" {
		t.Fatalf("매달린 origin/HEAD 는 symbolic-ref 로는 읽혀야 시험이 뜻을 갖는다: %q %v", got, err)
	}

	got, err := IntegrationBranches(ctx, f.git, f.repo, "origin", store, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultKnown || !got.LookupDue || trackingRefs(got.Branches) != "" {
		t.Fatalf("없는 참조를 가리키는 origin/HEAD 는 모르는 것으로 보고 원격에 물어야 한다: %+v", got)
	}

	// 원격에 물어 알아낸 값이 기록에 남으면 그것을 쓴다. 낡은 별명은 그대로 두어도 상관없다.
	if err := LookupDefaultBranch(ctx, f.git, f.repo, "origin", store, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err = IntegrationBranches(ctx, f.git, f.repo, "origin", store, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !got.DefaultKnown || trackingRefs(got.Branches) != "refs/remotes/origin/main" {
		t.Fatalf("기록에 남은 진짜 기본 브랜치를 써야 한다: %+v", got)
	}
}

// (h) gone 은 fetch 기록의 영구 실패 표시 그대로다. 근거를 더 요구하지 않는다.
func TestGone(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	missing := state.Record{}.MarkFailure(now, "couldn't find remote ref refs/heads/x", true)
	cases := []struct {
		name   string
		record state.Record
		want   bool
	}{
		{"기록 없음", state.Record{}, false},
		{"성공", state.Record{}.MarkSuccess(now), false},
		{"일시적 실패", state.Record{}.MarkFailure(now, "connection refused", false), false},
		{"원격 참조 없음", missing, true},
		{"원격 참조 없음, 성공한 적 없음(지워진 뒤 prune 된 브랜치를 처음 봄)", state.Record{}.MarkFailure(now, "couldn't find remote ref", true), true},
		{"영구 실패 뒤 성공", missing.MarkSuccess(now), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Gone(tc.record); got != tc.want {
				t.Fatalf("%+v -> %v, 기대값 %v", tc.record, got, tc.want)
			}
		})
	}
}

func TestRemoteFor(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	up := gitrepo.Upstream{Remote: "team/upstream"}
	if got, ok := RemoteFor(ctx, f.git, f.repo, &up); !ok || got != "team/upstream" {
		t.Fatalf("upstream 이 있으면 그 원격이어야 한다: %q %v", got, ok)
	}
	if got, ok := RemoteFor(ctx, f.git, f.repo, nil); !ok || got != "origin" {
		t.Fatalf("원격이 하나뿐이면 그것이어야 한다: %q %v", got, ok)
	}
	run(t, f.work, "git", "remote", "add", "fork", f.remote)
	if got, ok := RemoteFor(ctx, f.git, f.repo, nil); !ok || got != "origin" {
		t.Fatalf("여럿이면 origin 이어야 한다: %q %v", got, ok)
	}
	run(t, f.work, "git", "remote", "rename", "origin", "upstream")
	if got, ok := RemoteFor(ctx, f.git, f.repo, nil); ok {
		t.Fatalf("origin 도 없고 여럿이면 고를 수 없다: %q", got)
	}
}

// 분리된 HEAD 에는 자기 사본이 없다. upstream 의 원격 쪽 이름이 브랜치 이름과 같으면 그 추적 참조도 사본이다.
func TestSelfRefs(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	main := SelfRefs(ctx, f.git, f.repo, "main", &gitrepo.Upstream{Branch: "main", Remote: "origin", RemoteRef: "refs/heads/main", TrackingRef: "refs/remotes/origin/main"})
	if strings.Join(main.Copies, ",") != "refs/remotes/origin/main" || main.Tracking != "refs/remotes/origin/main" {
		t.Fatalf("main 의 자기 참조가 다르다: %+v", main)
	}
	odd := SelfRefs(ctx, f.git, f.repo, "main", &gitrepo.Upstream{Branch: "main", Remote: "origin", RemoteRef: "refs/heads/main", TrackingRef: "refs/remotes/elsewhere/main"})
	if strings.Join(odd.Copies, ",") != "refs/remotes/origin/main,refs/remotes/elsewhere/main" {
		t.Fatalf("이름이 같은 upstream 의 추적 참조는 사본에 들어가야 한다: %+v", odd)
	}
	feature := SelfRefs(ctx, f.git, f.repo, "feature", &gitrepo.Upstream{Branch: "feature", Remote: "origin", RemoteRef: "refs/heads/main", TrackingRef: "refs/remotes/origin/main"})
	if strings.Join(feature.Copies, ",") != "refs/remotes/origin/feature" || feature.Tracking != "refs/remotes/origin/main" {
		t.Fatalf("다른 브랜치를 따라가는 upstream 은 사본이 아니다: %+v", feature)
	}
	detached := SelfRefs(ctx, f.git, f.repo, "", nil)
	if len(detached.Copies) != 0 || detached.Tracking != "" {
		t.Fatalf("분리된 HEAD 에는 자기 참조가 없어야 한다: %+v", detached)
	}
}

// --- 시험 도구 ---------------------------------------------------------------

type fixture struct {
	base   string
	remote string
	seed   string
	work   string
	git    gitrepo.Runner
	repo   gitrepo.Repo
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 이 없어 건너뛴다")
	}
	base := t.TempDir()
	f := &fixture{
		base:   base,
		remote: filepath.Join(base, "remote.git"),
		seed:   filepath.Join(base, "seed"),
		work:   filepath.Join(base, "work"),
		git:    gitrepo.Runner{Timeout: 30 * time.Second},
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
	repo, err := f.git.Discover(context.Background(), f.work)
	if err != nil {
		t.Fatal(err)
	}
	f.repo = repo
	return f
}

func (f *fixture) requireMergeTree(t *testing.T) {
	t.Helper()
	if !f.git.SupportsMergeTree(context.Background()) {
		t.Skip("이 git 판은 merge-tree --write-tree 를 지원하지 않는다")
	}
}

// startFeature는 work 에 main 에서 갈라진 브랜치를 만들고 커밋 하나를 얹는다.
func (f *fixture) startFeature(t *testing.T, branch, file, body string) {
	t.Helper()
	run(t, f.work, "git", "checkout", "--quiet", "-b", branch, "main")
	writeFile(t, filepath.Join(f.work, file), body)
	run(t, f.work, "git", "add", ".")
	run(t, f.work, "git", "commit", "--quiet", "-m", branch)
}

// pushFeature는 브랜치를 올리고 upstream 을 잡는다. 그러면 자기 추적 참조가 생긴다.
func (f *fixture) pushFeature(t *testing.T, branch string) {
	t.Helper()
	run(t, f.work, "git", "push", "--quiet", "--set-upstream", "origin", branch)
}

// self는 데몬이 하는 것과 같은 방법으로 work 의 현재 브랜치의 자기 참조를 모은다.
func (f *fixture) self(t *testing.T) Self {
	t.Helper()
	ctx := context.Background()
	branch, err := f.git.CurrentBranch(ctx, f.repo)
	if err != nil {
		t.Fatal(err)
	}
	var upstream *gitrepo.Upstream
	if up, err := f.git.Upstream(ctx, f.repo); err == nil {
		upstream = &up
	}
	return SelfRefs(ctx, f.git, f.repo, branch, upstream)
}

func (f *fixture) judgeMerged(t *testing.T, self Self, targets []string) Merged {
	t.Helper()
	ctx := context.Background()
	head, err := f.git.HeadCommit(ctx, f.repo)
	if err != nil {
		t.Fatal(err)
	}
	got, err := JudgeMerged(ctx, f.git, f.repo, head, self, targets)
	if err != nil {
		t.Fatalf("판정하지 못했다: %v", err)
	}
	return got
}

func (f *fixture) commitOf(t *testing.T, ref string) string {
	t.Helper()
	commit, err := f.git.CommitOf(context.Background(), f.repo, ref)
	if err != nil {
		t.Fatal(err)
	}
	return commit
}

func trackingRefs(branches []gitrepo.Upstream) string {
	refs := make([]string, 0, len(branches))
	for _, up := range branches {
		refs = append(refs, up.TrackingRef)
	}
	return strings.Join(refs, ",")
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

// output은 명령을 돌리고 출력을 돌려준다. 실패하면 빈 문자열이다. 전제를 확인하는 자리에 쓴다.
func output(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		return ""
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
