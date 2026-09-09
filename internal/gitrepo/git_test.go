package gitrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSubcommandSkipsGlobalOptions(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"하위 명령만", []string{"fetch", "--quiet"}, "fetch"},
		{"값을 받는 전역 옵션 뒤", []string{"-c", "gc.auto=0", "fetch"}, "fetch"},
		{"전역 옵션이 여럿", []string{"-c", "a=b", "-C", "/tmp", "rev-list"}, "rev-list"},
		{"값이 없는 플래그", []string{"--no-pager", "status"}, "status"},
		{"하위 명령이 없음", []string{"--version"}, "git"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := subcommand(tc.args); got != tc.want {
				t.Fatalf("%v -> %q, 기대값 %q", tc.args, got, tc.want)
			}
		})
	}
}

func TestIsMissingRemoteRef(t *testing.T) {
	if IsMissingRemoteRef(nil) {
		t.Fatal("nil 은 영구 실패가 아니다")
	}
	if !IsMissingRemoteRef(errString("fatal: couldn't find remote ref refs/heads/gone")) {
		t.Fatal("사라진 원격 참조를 알아보지 못했다")
	}
	if IsMissingRemoteRef(errString("fatal: Could not read from remote repository")) {
		t.Fatal("일시적인 연결 실패를 영구 실패로 보면 안 된다")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// 아래 시험들은 실제 git 저장소를 만들어 돌린다. 원격은 로컬 bare 저장소라 네트워크를 타지 않는다.
func TestDiscoverAndFetchAgainstRealRepositories(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 이 없어 건너뛴다")
	}
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	run(t, base, "git", "init", "--quiet", "--bare", "--initial-branch=main", remote)

	seed := filepath.Join(base, "seed")
	run(t, base, "git", "clone", "--quiet", remote, seed)
	configure(t, seed)
	writeFile(t, filepath.Join(seed, "f"), "1")
	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "--quiet", "-m", "one")
	run(t, seed, "git", "push", "--quiet", "origin", "HEAD:refs/heads/main")

	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)
	configure(t, work)

	ctx := context.Background()
	runner := Runner{Timeout: 30 * time.Second}

	repo, err := runner.Discover(ctx, work)
	if err != nil {
		t.Fatalf("저장소를 찾지 못했다: %v", err)
	}
	if !sameDir(repo.Root, work) {
		t.Fatalf("작업 트리 최상위가 다르다: %q != %q", repo.Root, work)
	}
	if !filepath.IsAbs(repo.CommonDir) {
		t.Fatalf("공용 디렉터리는 절대 경로여야 한다: %q", repo.CommonDir)
	}

	up, err := runner.Upstream(ctx, repo)
	if err != nil {
		t.Fatalf("upstream 을 찾지 못했다: %v", err)
	}
	if up.Remote != "origin" || up.RemoteRef != "refs/heads/main" || up.TrackingRef != "refs/remotes/origin/main" {
		t.Fatalf("upstream 이 예상과 다르다: %+v", up)
	}

	// 원격에 커밋을 하나 더 올린다. fetch 하기 전에는 뒤처짐이 보이지 않아야 한다.
	writeFile(t, filepath.Join(seed, "f"), "2")
	run(t, seed, "git", "commit", "--quiet", "-am", "two")
	run(t, seed, "git", "push", "--quiet", "origin", "HEAD:refs/heads/main")

	before, err := runner.CountsFor(ctx, repo, up)
	if err != nil {
		t.Fatal(err)
	}
	if before.Behind != 0 {
		t.Fatalf("fetch 전에는 뒤처짐이 보이지 않아야 한다: %+v", before)
	}

	if err := runner.Fetch(ctx, repo, up); err != nil {
		t.Fatalf("fetch 하지 못했다: %v", err)
	}
	after, err := runner.CountsFor(ctx, repo, up)
	if err != nil {
		t.Fatal(err)
	}
	if after.Behind != 1 || after.Ahead != 0 {
		t.Fatalf("fetch 뒤에는 한 커밋 뒤처져야 한다: %+v", after)
	}
}

// 연결된 worktree 는 본 저장소와 참조를 공유한다. 같은 브랜치를 보면 fetch 열쇠가 같아야
// 중복 fetch 가 생기지 않고, 다른 브랜치를 보면 달라야 각각 갱신된다.
func TestFetchKeyDistinguishesBranchesWithinOneRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 이 없어 건너뛴다")
	}
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	run(t, base, "git", "init", "--quiet", "--bare", "--initial-branch=main", remote)

	seed := filepath.Join(base, "seed")
	run(t, base, "git", "clone", "--quiet", remote, seed)
	configure(t, seed)
	writeFile(t, filepath.Join(seed, "f"), "1")
	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "--quiet", "-m", "one")
	run(t, seed, "git", "push", "--quiet", "origin", "HEAD:refs/heads/main")
	run(t, seed, "git", "checkout", "--quiet", "-b", "feature")
	run(t, seed, "git", "commit", "--quiet", "--allow-empty", "-m", "feat")
	run(t, seed, "git", "push", "--quiet", "origin", "feature")

	main := filepath.Join(base, "main")
	run(t, base, "git", "clone", "--quiet", remote, main)
	configure(t, main)
	linked := filepath.Join(base, "linked")
	run(t, main, "git", "worktree", "add", "--quiet", linked, "feature")

	ctx := context.Background()
	runner := Runner{Timeout: 30 * time.Second}

	mainRepo, err := runner.Discover(ctx, main)
	if err != nil {
		t.Fatal(err)
	}
	linkedRepo, err := runner.Discover(ctx, linked)
	if err != nil {
		t.Fatal(err)
	}
	if mainRepo.CommonDir != linkedRepo.CommonDir {
		t.Fatalf("연결된 worktree 는 공용 디렉터리를 공유해야 한다: %q != %q", mainRepo.CommonDir, linkedRepo.CommonDir)
	}

	mainUp, err := runner.Upstream(ctx, mainRepo)
	if err != nil {
		t.Fatal(err)
	}
	linkedUp, err := runner.Upstream(ctx, linkedRepo)
	if err != nil {
		t.Fatal(err)
	}
	if mainUp.FetchKey(mainRepo.CommonDir) == linkedUp.FetchKey(linkedRepo.CommonDir) {
		t.Fatal("서로 다른 브랜치는 fetch 열쇠가 달라야 한다")
	}
	// 같은 저장소, 같은 브랜치를 두 번 물으면 열쇠가 같아야 한다.
	if mainUp.FetchKey(mainRepo.CommonDir) != mainUp.FetchKey(mainRepo.CommonDir) {
		t.Fatal("같은 조건이면 fetch 열쇠가 같아야 한다")
	}
}

func TestUpstreamFailsWithoutTracking(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 이 없어 건너뛴다")
	}
	base := t.TempDir()
	repoDir := filepath.Join(base, "solo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, repoDir, "git", "init", "--quiet", "--initial-branch=main")
	configure(t, repoDir)
	run(t, repoDir, "git", "commit", "--quiet", "--allow-empty", "-m", "one")

	ctx := context.Background()
	runner := Runner{}
	repo, err := runner.Discover(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Upstream(ctx, repo); err != ErrNoUpstream {
		t.Fatalf("upstream 이 없으면 ErrNoUpstream 이어야 한다: %v", err)
	}
}

func TestDiscoverRejectsNonRepository(t *testing.T) {
	ctx := context.Background()
	runner := Runner{}
	if _, err := runner.Discover(ctx, t.TempDir()); err != ErrNotRepository {
		t.Fatalf("저장소가 아니면 ErrNotRepository 여야 한다: %v", err)
	}
	if _, err := runner.Discover(ctx, ""); err != ErrNotRepository {
		t.Fatal("빈 경로는 저장소가 아니다")
	}
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s 실패: %v\n%s", name, strings.Join(args, " "), err, out)
	}
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

// macOS 의 임시 디렉터리는 심볼릭 링크라(/var -> /private/var) 경로 문자열이 서로 다를 수 있다.
func sameDir(a, b string) bool {
	ra, erra := filepath.EvalSymlinks(a)
	rb, errb := filepath.EvalSymlinks(b)
	if erra != nil || errb != nil {
		return a == b
	}
	return ra == rb
}
