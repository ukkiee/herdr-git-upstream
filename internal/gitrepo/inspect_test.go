package gitrepo

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// 배포판마다 `git version` 뒤에 붙이는 것이 다르다. 앞 두 숫자만 필요하므로 나머지는 무시해야 한다.
func TestParseVersion(t *testing.T) {
	cases := []struct {
		name      string
		line      string
		major     int
		minor     int
		text      string
		wantError bool
	}{
		{"보통", "git version 2.55.0", 2, 55, "2.55.0", false},
		{"애플", "git version 2.39.5 (Apple Git-154)", 2, 39, "2.39.5", false},
		{"윈도우", "git version 2.45.1.windows.1", 2, 45, "2.45.1.windows.1", false},
		{"줄 끝 공백", "git version 2.38.0\n", 2, 38, "2.38.0", false},
		{"엉뚱한 출력", "usage: git [--version]", 0, 0, "", true},
		{"숫자가 아님", "git version x.y", 0, 0, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			major, minor, text, err := parseVersion(tc.line)
			if tc.wantError {
				if err == nil {
					t.Fatalf("오류여야 한다: %q", tc.line)
				}
				return
			}
			if err != nil {
				t.Fatalf("해석하지 못했다: %v", err)
			}
			if major != tc.major || minor != tc.minor || text != tc.text {
				t.Fatalf("%q -> %d.%d %q, 기대값 %d.%d %q", tc.line, major, minor, text, tc.major, tc.minor, tc.text)
			}
		})
	}
}

// merge-tree --write-tree 는 2.38 에서 생겼다. 경계를 정확히 잡아야 한다.
func TestSupportsMergeTreeThreshold(t *testing.T) {
	cases := []struct {
		major, minor int
		want         bool
	}{
		{2, 37, false},
		{2, 38, true},
		{2, 55, true},
		{3, 0, true},
		{1, 99, false},
	}
	for _, tc := range cases {
		if got := supportsMergeTree(tc.major, tc.minor); got != tc.want {
			t.Fatalf("%d.%d -> %v, 기대값 %v", tc.major, tc.minor, got, tc.want)
		}
	}
}

func TestVersionReadsInstalledGit(t *testing.T) {
	requireGit(t)
	runner := Runner{}
	major, _, err := runner.Version(context.Background())
	if err != nil {
		t.Fatalf("git 판을 읽지 못했다: %v", err)
	}
	if major < 1 {
		t.Fatalf("말이 안 되는 판 번호: %d", major)
	}
	if runner.VersionText(context.Background()) == "" {
		t.Fatal("판 번호 문자열이 비어 있다")
	}
}

// clone 은 refs/remotes/origin/HEAD 를 만들지만, `git remote add` 로 붙인 원격에는 그것이 없다.
func TestRemoteHeadPresentAndAbsent(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)
	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)
	configure(t, work)

	ctx := context.Background()
	runner := Runner{Timeout: 30 * time.Second}
	repo, err := runner.Discover(ctx, work)
	if err != nil {
		t.Fatal(err)
	}

	got, err := runner.RemoteHead(ctx, repo, "origin")
	if err != nil {
		t.Fatalf("clone 직후에는 원격 기본 브랜치가 있어야 한다: %v", err)
	}
	if got != "refs/remotes/origin/main" {
		t.Fatalf("원격 기본 브랜치가 다르다: %q", got)
	}

	// 원격에 직접 물어도 같은 답이어야 한다. 원격은 로컬 bare 저장소라 네트워크를 타지 않는다.
	remoteRef, err := runner.RemoteHeadFromRemote(ctx, repo, "origin")
	if err != nil {
		t.Fatalf("원격에 물어 기본 브랜치를 알아내지 못했다: %v", err)
	}
	if remoteRef != "refs/heads/main" {
		t.Fatalf("원격 쪽 참조가 다르다: %q", remoteRef)
	}

	run(t, work, "git", "remote", "set-head", "origin", "--delete")
	if _, err := runner.RemoteHead(ctx, repo, "origin"); !errors.Is(err, ErrNoRemoteHead) {
		t.Fatalf("지운 뒤에는 ErrNoRemoteHead 여야 한다: %v", err)
	}
	if _, err := runner.RemoteHead(ctx, repo, "nowhere"); !errors.Is(err, ErrNoRemoteHead) {
		t.Fatalf("없는 원격은 ErrNoRemoteHead 여야 한다: %v", err)
	}

	// 커밋이 하나도 없는 원격은 HEAD 를 알리지 않는다. 원격에 물은 결과이므로 "로컬에 없다"와는 다른
	// 오류여야 로그를 읽는 사람이 로컬 참조를 의심하지 않는다.
	empty := filepath.Join(base, "empty.git")
	run(t, base, "git", "init", "--quiet", "--bare", "--initial-branch=main", empty)
	run(t, work, "git", "remote", "add", "empty", empty)
	if _, err := runner.RemoteHeadFromRemote(ctx, repo, "empty"); !errors.Is(err, ErrRemoteHasNoHead) {
		t.Fatalf("빈 원격은 ErrRemoteHasNoHead 여야 한다: %v", err)
	}
	if errors.Is(ErrRemoteHasNoHead, ErrNoRemoteHead) {
		t.Fatal("두 오류값은 서로 다른 뜻이어야 한다")
	}
}

// 원격 이름에 "/" 가 들어갈 수 있으므로 첫 "/" 에서 자르면 안 된다. 등록된 원격 중 가장 긴 일치를 고른다.
func TestTargetFromShortRef(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)
	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)
	configure(t, work)
	run(t, work, "git", "remote", "add", "team/upstream", remote)
	// 최근 git 은 다른 원격 이름의 앞부분이 되는 이름을 `remote add` 로 받지 않는다. 그래도 설정 파일에
	// 직접 적힌 옛 저장소는 있을 수 있으므로, 설정으로 넣어 가장 긴 일치를 고르는지 본다.
	run(t, work, "git", "config", "remote.team.url", remote)
	run(t, work, "git", "config", "remote.team.fetch", "+refs/heads/*:refs/remotes/team/*")

	ctx := context.Background()
	runner := Runner{}
	repo, err := runner.Discover(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	remotes, err := runner.Remotes(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	nested := false
	for _, name := range remotes {
		if name == "team" {
			nested = true
		}
	}

	cases := []struct {
		name      string
		short     string
		want      Upstream
		wantError bool
		nested    bool
	}{
		{"origin", "origin/main", Upstream{Remote: "origin", RemoteRef: "refs/heads/main", TrackingRef: "refs/remotes/origin/main"}, false, false},
		{"브랜치에 슬래시", "origin/widget-studio/dev", Upstream{Remote: "origin", RemoteRef: "refs/heads/widget-studio/dev", TrackingRef: "refs/remotes/origin/widget-studio/dev"}, false, false},
		{"원격 이름에 슬래시", "team/upstream/dev", Upstream{Remote: "team/upstream", RemoteRef: "refs/heads/dev", TrackingRef: "refs/remotes/team/upstream/dev"}, false, false},
		{"짧은 원격이 먼저 맞아도 긴 쪽", "team/upstream/a/b", Upstream{Remote: "team/upstream", RemoteRef: "refs/heads/a/b", TrackingRef: "refs/remotes/team/upstream/a/b"}, false, true},
		{"짧은 원격 자체", "team/dev", Upstream{Remote: "team", RemoteRef: "refs/heads/dev", TrackingRef: "refs/remotes/team/dev"}, false, true},
		{"전체 이름을 적어도 받는다", "refs/remotes/origin/dev", Upstream{Remote: "origin", RemoteRef: "refs/heads/dev", TrackingRef: "refs/remotes/origin/dev"}, false, false},
		{"등록되지 않은 원격", "nowhere/main", Upstream{}, true, false},
		{"브랜치가 없음", "origin/", Upstream{}, true, false},
		{"원격만", "origin", Upstream{}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.nested && !nested {
				t.Skip("이 git 판은 다른 원격의 앞부분이 되는 원격 이름을 나열하지 않는다")
			}
			got, err := runner.TargetFromShortRef(ctx, repo, tc.short)
			if tc.wantError {
				if err == nil {
					t.Fatalf("오류여야 한다: %q -> %+v", tc.short, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("풀지 못했다: %v", err)
			}
			if got != tc.want {
				t.Fatalf("%q -> %+v, 기대값 %+v", tc.short, got, tc.want)
			}
		})
	}
}

func TestMergeTargetsReadsAllValues(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)
	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)

	ctx := context.Background()
	runner := Runner{}
	repo, err := runner.Discover(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	got, err := runner.MergeTargets(ctx, repo)
	if err != nil {
		t.Fatalf("설정이 없는 것은 오류가 아니다: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("설정이 없으면 빈 목록이어야 한다: %v", got)
	}

	run(t, work, "git", "config", "--add", "git-upstream.mergeTarget", "origin/dev")
	run(t, work, "git", "config", "--add", "git-upstream.mergeTarget", "origin/release")
	got, err = runner.MergeTargets(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"origin/dev", "origin/release"}) {
		t.Fatalf("값을 모두 읽어야 한다: %v", got)
	}
}

// refs/remotes/origin/HEAD 는 다른 참조의 별명이라 같은 답이 두 번 나온다. 빼야 한다.
func TestContainingRemoteRefsExcludesHead(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)
	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)
	configure(t, work)

	ctx := context.Background()
	runner := Runner{}
	repo, err := runner.Discover(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	// 전제: clone 이 origin/HEAD 를 만들어 두었다.
	if _, err := runner.RemoteHead(ctx, repo, "origin"); err != nil {
		t.Skip("이 git 판은 clone 때 origin/HEAD 를 만들지 않는다")
	}
	head, err := runner.HeadCommit(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	got, err := runner.ContainingRemoteRefs(ctx, repo, head)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"refs/remotes/origin/main"}) {
		t.Fatalf("origin/HEAD 를 뺀 목록이어야 한다: %v", got)
	}

	// 아무 원격 참조에도 들어 있지 않은 커밋은 빈 목록이다.
	run(t, work, "git", "commit", "--quiet", "--allow-empty", "-m", "local only")
	head, err = runner.HeadCommit(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	got, err = runner.ContainingRemoteRefs(ctx, repo, head)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("로컬에만 있는 커밋을 품은 원격 참조는 없어야 한다: %v", got)
	}
}

// PR 참조 사양이 refs/remotes/ 아래로 가져온 refs/pull/*/head 는 브랜치가 아니다. 자기 PR 의 head 는
// 언제나 HEAD 를 품으므로 세면 안 된다.
func TestContainingRemoteRefsExcludesPullRequestHeads(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)
	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)
	configure(t, work)
	run(t, remote, "git", "update-ref", "refs/pull/7/head", "refs/heads/main")
	run(t, work, "git", "config", "--add", "remote.origin.fetch", "+refs/pull/*/head:refs/remotes/origin/pr/*")
	run(t, work, "git", "fetch", "--quiet", "origin")

	ctx := context.Background()
	runner := Runner{}
	repo, err := runner.Discover(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	// 전제: PR 참조가 로컬에 비쳐 있다.
	if _, err := runner.CommitOf(ctx, repo, "refs/remotes/origin/pr/7"); err != nil {
		t.Fatalf("PR 참조가 가져와졌어야 한다: %v", err)
	}
	head, err := runner.HeadCommit(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	got, err := runner.ContainingRemoteRefs(ctx, repo, head)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"refs/remotes/origin/main"}) {
		t.Fatalf("PR 참조를 뺀 목록이어야 한다: %v", got)
	}
}

// 브랜치가 아니라고 확실히 말할 수 있는 출발지만 거른다. 더 넓은 패턴은 브랜치를 품을 수 있다.
func TestBranchSource(t *testing.T) {
	cases := []struct {
		source string
		want   bool
	}{
		{"refs/heads/*", true},
		{"refs/heads/main", true},
		{"refs/heads/widget-studio/*", true},
		{"refs/*", true},
		{"refs/pull/*/head", false},
		{"refs/merge-requests/*/head", false},
		{"refs/tags/*", false},
		{"refs/notes/*", false},
		{"HEAD", false},
	}
	for _, tc := range cases {
		if got := branchSource(tc.source); got != tc.want {
			t.Fatalf("%q -> %v, 기대값 %v", tc.source, got, tc.want)
		}
	}
}

// 참조 사양의 규칙은 substituteRefspec 한 곳에 산다. 정방향과 역방향이 같은 규칙을 쓰는지 표로 확인한다.
func TestSubstituteRefspec(t *testing.T) {
	cases := []struct {
		name     string
		pattern  string
		template string
		ref      string
		want     string
		ok       bool
	}{
		{"별표 대입", "refs/heads/*", "refs/remotes/origin/*", "refs/heads/main", "refs/remotes/origin/main", true},
		{"역방향", "refs/remotes/origin/*", "refs/heads/*", "refs/remotes/origin/a/b", "refs/heads/a/b", true},
		{"접두사가 다름", "refs/heads/*", "refs/remotes/origin/*", "refs/tags/v1", "", false},
		{"별표 없는 사양은 정확 일치", "refs/heads/main", "refs/remotes/only/main", "refs/heads/main", "refs/remotes/only/main", true},
		{"별표 없는 사양은 다른 참조를 받지 않음", "refs/heads/main", "refs/remotes/only/main", "refs/heads/other", "", false},
		{"template 에 별표가 없으면 대입할 자리가 없음", "refs/heads/*", "refs/remotes/only/main", "refs/heads/main", "", false},
		{"뒤에 붙는 부분", "refs/pull/*/head", "refs/remotes/origin/pr/*", "refs/pull/7/head", "refs/remotes/origin/pr/7", true},
		{"앞뒤가 겹칠 만큼 짧은 참조", "refs/a*a/x", "refs/*", "refs/a/x", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := substituteRefspec(tc.pattern, tc.template, tc.ref)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("(%q, %q, %q) -> %q %v, 기대값 %q %v", tc.pattern, tc.template, tc.ref, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// 분리된 HEAD 는 "브랜치 없음"이지 실패가 아니다. 저장소가 아닌 것과 구별해야 한다.
func TestCurrentBranchDistinguishesDetachedHead(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)
	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)

	ctx := context.Background()
	runner := Runner{}
	repo, err := runner.Discover(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := runner.CurrentBranch(ctx, repo); err != nil || got != "main" {
		t.Fatalf("체크아웃된 브랜치는 main 이어야 한다: %q %v", got, err)
	}
	run(t, work, "git", "checkout", "--quiet", "--detach", "HEAD")
	if got, err := runner.CurrentBranch(ctx, repo); err != nil || got != "" {
		t.Fatalf("분리된 HEAD 는 빈 문자열이고 오류가 아니어야 한다: %q %v", got, err)
	}
	if _, err := runner.Upstream(ctx, repo); !errors.Is(err, ErrNoUpstream) {
		t.Fatalf("분리된 HEAD 에는 upstream 이 없어야 한다: %v", err)
	}
	if _, err := runner.CurrentBranch(ctx, Repo{Root: t.TempDir()}); err == nil {
		t.Fatal("저장소가 아니면 오류여야 한다")
	}
}

// merge-tree 는 종료 코드로 답한다. 0 은 깨끗, 1 은 충돌이며 둘 다 결과 트리를 준다.
func TestMergeTreeCleanConflictAndIdentity(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	runner := Runner{}
	if !runner.SupportsMergeTree(ctx) {
		t.Skip("이 git 판은 merge-tree --write-tree 를 지원하지 않는다")
	}
	base := t.TempDir()
	remote, seed := seedRemote(t, base)
	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)
	configure(t, work)
	repo, err := runner.Discover(ctx, work)
	if err != nil {
		t.Fatal(err)
	}

	// 서로 다른 파일을 고친 두 갈래는 깨끗하게 합쳐진다.
	run(t, work, "git", "checkout", "--quiet", "-b", "other-file")
	writeFile(t, filepath.Join(work, "g"), "g")
	run(t, work, "git", "add", ".")
	run(t, work, "git", "commit", "--quiet", "-m", "g")
	// 같은 파일을 다르게 고친 갈래는 충돌한다.
	run(t, work, "git", "checkout", "--quiet", "-b", "same-file", "main")
	writeFile(t, filepath.Join(work, "f"), "mine")
	run(t, work, "git", "commit", "--quiet", "-am", "mine")
	// 원격도 f 를 고쳤다.
	writeFile(t, filepath.Join(seed, "f"), "theirs")
	run(t, seed, "git", "commit", "--quiet", "-am", "theirs")
	run(t, seed, "git", "push", "--quiet", "origin", "HEAD:refs/heads/main")
	run(t, work, "git", "fetch", "--quiet", "origin")

	t.Run("깨끗", func(t *testing.T) {
		tree, conflict, err := runner.MergeTree(ctx, repo, "refs/remotes/origin/main", "other-file")
		if err != nil {
			t.Fatal(err)
		}
		if conflict || tree == "" {
			t.Fatalf("다른 파일을 고친 갈래는 깨끗해야 한다: tree=%q conflict=%v", tree, conflict)
		}
	})
	t.Run("충돌", func(t *testing.T) {
		tree, conflict, err := runner.MergeTree(ctx, repo, "refs/remotes/origin/main", "same-file")
		if err != nil {
			t.Fatal(err)
		}
		if !conflict || tree == "" {
			t.Fatalf("같은 파일을 고친 갈래는 충돌해야 한다: tree=%q conflict=%v", tree, conflict)
		}
	})
	t.Run("이미 들어간 갈래는 결과 트리가 같다", func(t *testing.T) {
		// main 이 origin/main 의 조상이므로 병합해도 origin/main 그대로다.
		tree, conflict, err := runner.MergeTree(ctx, repo, "refs/remotes/origin/main", "main")
		if err != nil {
			t.Fatal(err)
		}
		want, err := runner.TreeOf(ctx, repo, "refs/remotes/origin/main")
		if err != nil {
			t.Fatal(err)
		}
		if conflict || tree != want {
			t.Fatalf("조상을 병합하면 상대의 트리 그대로여야 한다: %q != %q (conflict=%v)", tree, want, conflict)
		}
	})
	t.Run("없는 참조는 오류", func(t *testing.T) {
		if _, _, err := runner.MergeTree(ctx, repo, "refs/remotes/origin/nope", "main"); err == nil {
			t.Fatal("없는 참조를 병합하면 오류여야 한다")
		}
	})
}

func TestTreeOfAndCommitOf(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)
	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)

	ctx := context.Background()
	runner := Runner{}
	repo, err := runner.Discover(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := runner.CommitOf(ctx, repo, "refs/remotes/origin/main")
	if err != nil {
		t.Fatal(err)
	}
	head, err := runner.HeadCommit(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if commit != head {
		t.Fatalf("clone 직후 HEAD 와 origin/main 은 같은 커밋이어야 한다: %q != %q", commit, head)
	}
	tree, err := runner.TreeOf(ctx, repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if tree == "" || tree == commit {
		t.Fatalf("트리 해시는 커밋 해시와 달라야 한다: %q", tree)
	}
	if _, err := runner.CommitOf(ctx, repo, "refs/remotes/origin/nope"); err == nil {
		t.Fatal("없는 참조는 오류여야 한다")
	}
}

// 깨끗함과 손대지 않음은 다른 물음이다. 추적되지 않은 파일이 있으면 깨끗하지만 손댄 것이다.
func TestIsUntouchedDiffersFromIsClean(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)
	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)

	ctx := context.Background()
	runner := Runner{}
	repo, err := runner.Discover(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	clean, err := runner.IsClean(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	untouched, err := runner.IsUntouched(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !clean || !untouched {
		t.Fatalf("clone 직후에는 둘 다 참이어야 한다: clean=%v untouched=%v", clean, untouched)
	}

	writeFile(t, filepath.Join(work, "new.txt"), "추적되지 않음")
	clean, err = runner.IsClean(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	untouched, err = runner.IsUntouched(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Fatal("추적되지 않은 파일은 깨끗함을 깨지 않는다")
	}
	if untouched {
		t.Fatal("추적되지 않은 파일이 있으면 손댄 것이다")
	}
}

// 추적 참조에서 원격 쪽 이름을 거꾸로 알아낼 때도 참조 사양을 먼저 본다.
func TestRemoteRefForInvertsRefspec(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)
	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)

	ctx := context.Background()
	runner := Runner{}
	repo, err := runner.Discover(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	if got := runner.RemoteRefFor(ctx, repo, "origin", "refs/remotes/origin/main"); got != "refs/heads/main" {
		t.Fatalf("관례를 거꾸로 적용하지 못했다: %q", got)
	}
	run(t, work, "git", "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/mirror/*")
	if got := runner.RemoteRefFor(ctx, repo, "origin", "refs/remotes/mirror/dev"); got != "refs/heads/dev" {
		t.Fatalf("참조 사양을 거꾸로 대입하지 못했다: %q", got)
	}
	if got := runner.RemoteRefFor(ctx, repo, "origin", "refs/remotes/elsewhere/dev"); got != "" {
		t.Fatalf("짝이 없는 참조에는 답하지 않아야 한다: %q", got)
	}
}

// 종료 코드가 뜻을 갖는 명령을 위해 gitExit 는 0 이 아닌 종료를 오류로 만들지 않는다.
// 반면 gitWithEnv 는 지금까지처럼 오류로 만들어야 IsMissingRemoteRef 같은 판단이 그대로 돈다.
func TestGitExitDistinguishesExitCodeFromFailureToRun(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)
	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)

	ctx := context.Background()
	runner := Runner{}
	res, err := runner.gitExit(ctx, work, localTimeout, nonInteractiveEnv(), "config", "--get", "no.such.key")
	if err != nil {
		t.Fatalf("끝까지 돈 명령은 오류가 아니다: %v", err)
	}
	if res.code != 1 {
		t.Fatalf("없는 키는 1 로 끝나야 한다: %d", res.code)
	}
	if _, err := runner.gitWithEnv(ctx, work, localTimeout, nonInteractiveEnv(), "config", "--get", "no.such.key"); err == nil {
		t.Fatal("gitWithEnv 는 0 이 아닌 종료를 오류로 만들어야 한다")
	} else if !strings.Contains(err.Error(), "git config") {
		t.Fatalf("오류 문구에 하위 명령 이름이 있어야 한다: %v", err)
	}
}

// 컨텍스트가 끝났으면 제한 시간이든 취소든 언제나 실패다. 우리가 죽인 프로세스의 종료 코드(윈도우는 1)를
// "키 없음"이나 "충돌" 같은 답으로 읽으면 안 된다.
func TestGitExitFailsWhenContextEnds(t *testing.T) {
	requireGit(t)
	runner := Runner{}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, expire := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expire()

	cases := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"취소", cancelled, "취소됨"},
		{"제한 시간", expired, "제한 시간 초과"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runner.gitExit(tc.ctx, "", localTimeout, nonInteractiveEnv(), "config", "--get", "no.such.key")
			if err == nil {
				t.Fatal("끝난 컨텍스트에서는 종료 코드가 아니라 오류여야 한다")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("오류 문구가 다르다: %v, 기대값 %q", err, tc.want)
			}
		})
	}
}
