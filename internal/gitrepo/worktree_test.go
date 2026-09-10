package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// porcelain 출력의 모양은 git 문서에 고정되어 있다. 표시 줄 뒤에 이유가 붙는 경우와 bare, 분리된 HEAD,
// 줄 끝이 CRLF 인 경우(윈도우)를 표로 잡아 둔다.
func TestParseWorktreeList(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want []WorktreeEntry
	}{
		{
			name: "본 체크아웃 하나",
			out:  "worktree /home/u/mfe\nHEAD 1111111111111111111111111111111111111111\nbranch refs/heads/main\n\n",
			want: []WorktreeEntry{{Path: "/home/u/mfe", Head: "1111111111111111111111111111111111111111", Branch: "main"}},
		},
		{
			name: "연결된 worktree 와 분리된 HEAD",
			out: "worktree /home/u/mfe\nHEAD 1111111111111111111111111111111111111111\nbranch refs/heads/main\n\n" +
				"worktree /home/u/wt/feature\nHEAD 2222222222222222222222222222222222222222\nbranch refs/heads/widget-studio/dev\n\n" +
				"worktree /home/u/wt/old\nHEAD 3333333333333333333333333333333333333333\ndetached\n\n",
			want: []WorktreeEntry{
				{Path: "/home/u/mfe", Head: "1111111111111111111111111111111111111111", Branch: "main"},
				{Path: "/home/u/wt/feature", Head: "2222222222222222222222222222222222222222", Branch: "widget-studio/dev"},
				{Path: "/home/u/wt/old", Head: "3333333333333333333333333333333333333333", Detached: true},
			},
		},
		{
			name: "잠김과 prunable, 이유가 붙은 것과 안 붙은 것",
			out: "worktree /home/u/wt/a\nHEAD 1111111111111111111111111111111111111111\nbranch refs/heads/a\nlocked\n\n" +
				"worktree /home/u/wt/b\nHEAD 2222222222222222222222222222222222222222\nbranch refs/heads/b\nlocked \"reason with spaces\"\n\n" +
				"worktree /home/u/wt/c\nHEAD 3333333333333333333333333333333333333333\nbranch refs/heads/c\nprunable gitdir file points to non-existent location\n\n",
			want: []WorktreeEntry{
				{Path: "/home/u/wt/a", Head: "1111111111111111111111111111111111111111", Branch: "a", Locked: true},
				{Path: "/home/u/wt/b", Head: "2222222222222222222222222222222222222222", Branch: "b", Locked: true},
				{Path: "/home/u/wt/c", Head: "3333333333333333333333333333333333333333", Branch: "c", Prunable: true},
			},
		},
		{
			name: "bare 저장소 자신",
			out:  "worktree /home/u/repo.git\nbare\n\nworktree /home/u/wt/x\nHEAD 1111111111111111111111111111111111111111\nbranch refs/heads/x\n\n",
			want: []WorktreeEntry{
				{Path: "/home/u/repo.git", Bare: true},
				{Path: "/home/u/wt/x", Head: "1111111111111111111111111111111111111111", Branch: "x"},
			},
		},
		{
			name: "CRLF 와 마지막 빈 줄 없음",
			out:  "worktree C:/u/mfe\r\nHEAD 1111111111111111111111111111111111111111\r\nbranch refs/heads/main",
			want: []WorktreeEntry{{Path: "C:/u/mfe", Head: "1111111111111111111111111111111111111111", Branch: "main"}},
		},
		{
			name: "모르는 줄과 항목 밖의 줄은 건너뛴다",
			out:  "something odd\nworktree /home/u/wt/a\nHEAD 1111111111111111111111111111111111111111\nfuture-field value\nbranch refs/heads/a\n\n",
			want: []WorktreeEntry{{Path: "/home/u/wt/a", Head: "1111111111111111111111111111111111111111", Branch: "a"}},
		},
		{name: "빈 출력", out: "", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseWorktreeList([]byte(tc.out))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("해석 결과가 다르다:\n%+v\n기대값\n%+v", got, tc.want)
			}
		})
	}
}

// 실제 저장소에서 잠김과 사라진 디렉터리, 분리된 HEAD 가 각각의 표시로 읽히는지 본다.
func TestWorktreesAgainstRealRepository(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)
	main := filepath.Join(base, "main")
	run(t, base, "git", "clone", "--quiet", remote, main)
	configure(t, main)

	locked := filepath.Join(base, "locked")
	run(t, main, "git", "worktree", "add", "--quiet", "-b", "locked", locked)
	run(t, main, "git", "worktree", "lock", "--reason", "keep me", locked)
	missing := filepath.Join(base, "missing")
	run(t, main, "git", "worktree", "add", "--quiet", "-b", "missing", missing)
	if err := os.RemoveAll(missing); err != nil {
		t.Fatal(err)
	}
	detached := filepath.Join(base, "detached")
	run(t, main, "git", "worktree", "add", "--quiet", "--detach", detached)

	ctx := context.Background()
	runner := Runner{}
	repo, err := runner.Discover(ctx, main)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := runner.Worktrees(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("worktree 는 넷이어야 한다: %+v", entries)
	}
	byName := map[string]WorktreeEntry{}
	for _, entry := range entries {
		byName[filepath.Base(entry.Path)] = entry
	}
	cases := []struct {
		name string
		want WorktreeEntry
	}{
		{"main", WorktreeEntry{Branch: "main"}},
		{"locked", WorktreeEntry{Branch: "locked", Locked: true}},
		{"missing", WorktreeEntry{Branch: "missing", Prunable: true}},
		{"detached", WorktreeEntry{Detached: true}},
	}
	for _, tc := range cases {
		got, ok := byName[tc.name]
		if !ok {
			t.Fatalf("%s 가 목록에 없다: %+v", tc.name, entries)
		}
		if got.Head == "" || !sameDir(filepath.Dir(got.Path), base) {
			t.Fatalf("%s: HEAD 와 경로가 채워져야 한다: %+v", tc.name, got)
		}
		got.Path, got.Head = "", ""
		if got != tc.want {
			t.Fatalf("%s: %+v, 기대값 %+v", tc.name, got, tc.want)
		}
	}
	// 본 체크아웃이 첫 항목이다. 화면이 "본 체크아웃" 을 가려낼 때 herdr 없이도 이 순서에 기댈 수 있다.
	if filepath.Base(entries[0].Path) != "main" {
		t.Fatalf("본 체크아웃이 첫 항목이어야 한다: %+v", entries[0])
	}
}

// 강제 삭제는 없다. 손댄 것이 있거나 잠긴 worktree 는 git 이 거절해야 하고, 손대지 않은 것은 지워지되 브랜치는 남는다.
func TestRemoveWorktreeRefusesTouchedAndLocked(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)
	main := filepath.Join(base, "main")
	run(t, base, "git", "clone", "--quiet", remote, main)
	configure(t, main)

	ctx := context.Background()
	runner := Runner{}
	repo, err := runner.Discover(ctx, main)
	if err != nil {
		t.Fatal(err)
	}

	touched := filepath.Join(base, "touched")
	run(t, main, "git", "worktree", "add", "--quiet", "-b", "touched", touched)
	writeFile(t, filepath.Join(touched, "scratch.txt"), "추적되지 않은 파일")
	if err := runner.RemoveWorktree(ctx, repo, touched); err == nil {
		t.Fatal("추적되지 않은 파일이 있는 worktree 는 거절해야 한다")
	}
	if _, err := os.Stat(touched); err != nil {
		t.Fatalf("거절된 worktree 는 그대로 있어야 한다: %v", err)
	}

	locked := filepath.Join(base, "locked")
	run(t, main, "git", "worktree", "add", "--quiet", "-b", "locked", locked)
	run(t, main, "git", "worktree", "lock", locked)
	if err := runner.RemoveWorktree(ctx, repo, locked); err == nil {
		t.Fatal("잠긴 worktree 는 거절해야 한다")
	}

	// 파일을 치우면 지워진다. 브랜치는 남는다.
	if err := os.Remove(filepath.Join(touched, "scratch.txt")); err != nil {
		t.Fatal(err)
	}
	if err := runner.RemoveWorktree(ctx, repo, touched); err != nil {
		t.Fatalf("손대지 않은 worktree 는 지워져야 한다: %v", err)
	}
	if _, err := os.Stat(touched); !os.IsNotExist(err) {
		t.Fatalf("디렉터리가 사라져야 한다: %v", err)
	}
	entries, err := runner.Worktrees(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Branch == "touched" {
			t.Fatalf("지운 worktree 가 목록에 남아 있다: %+v", entries)
		}
	}
	if _, err := runner.CommitOf(ctx, repo, "refs/heads/touched"); err != nil {
		t.Fatalf("worktree 를 지워도 브랜치는 남아야 한다: %v", err)
	}
	if err := runner.RemoveWorktree(ctx, repo, ""); err == nil {
		t.Fatal("빈 경로는 오류여야 한다")
	}
}

// 전체 fetch 는 브랜치 전부를 가져오되 prune 하지 않고, ls-remote 는 지금 원격에 있는 브랜치만 말한다.
// 둘의 차이가 곧 화면의 gone 판정이다.
func TestFetchAllAndRemoteHeads(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, seed := seedRemote(t, base)
	run(t, seed, "git", "checkout", "--quiet", "-b", "doomed")
	run(t, seed, "git", "commit", "--quiet", "--allow-empty", "-m", "doomed")
	run(t, seed, "git", "push", "--quiet", "origin", "doomed")
	work := filepath.Join(base, "work")
	run(t, base, "git", "clone", "--quiet", remote, work)
	configure(t, work)

	ctx := context.Background()
	runner := Runner{Timeout: 30 * time.Second}
	repo, err := runner.Discover(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	// 전제: clone 은 둘 다 가져왔다.
	if _, err := runner.CommitOf(ctx, repo, "refs/remotes/origin/doomed"); err != nil {
		t.Fatalf("clone 이 doomed 를 가져왔어야 한다: %v", err)
	}

	// 원격에서 브랜치 하나가 새로 생기고 하나가 지워진다.
	run(t, seed, "git", "checkout", "--quiet", "-b", "later", "main")
	run(t, seed, "git", "commit", "--quiet", "--allow-empty", "-m", "later")
	run(t, seed, "git", "push", "--quiet", "origin", "later")
	run(t, seed, "git", "push", "--quiet", "origin", "--delete", "doomed")

	heads, err := runner.RemoteHeads(ctx, repo, "origin")
	if err != nil {
		t.Fatalf("원격의 브랜치 목록을 얻지 못했다: %v", err)
	}
	want := map[string]bool{"refs/heads/main": true, "refs/heads/later": true}
	if !reflect.DeepEqual(heads, want) {
		t.Fatalf("원격에 지금 있는 브랜치만 있어야 한다: %v, 기대값 %v", heads, want)
	}

	if err := runner.FetchAll(ctx, repo, "origin"); err != nil {
		t.Fatalf("전체 fetch 에 실패했다: %v", err)
	}
	if _, err := runner.CommitOf(ctx, repo, "refs/remotes/origin/later"); err != nil {
		t.Fatalf("새 브랜치를 가져왔어야 한다: %v", err)
	}
	// prune 하지 않으므로 지워진 브랜치의 추적 참조는 남는다. 사용자의 참조를 건드리지 않는다는 약속이다.
	if _, err := runner.CommitOf(ctx, repo, "refs/remotes/origin/doomed"); err != nil {
		t.Fatalf("전체 fetch 가 추적 참조를 지워서는 안 된다: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, ".git", "FETCH_HEAD")); err == nil {
		t.Fatal("배경 fetch 는 FETCH_HEAD 를 쓰지 않아야 한다")
	}

	// 닿지 않는 원격은 둘 다 오류다.
	run(t, work, "git", "remote", "add", "nowhere", filepath.Join(base, "no-such-remote.git"))
	if _, err := runner.RemoteHeads(ctx, repo, "nowhere"); err == nil {
		t.Fatal("닿지 않는 원격의 ls-remote 는 실패해야 한다")
	}
	if err := runner.FetchAll(ctx, repo, "nowhere"); err == nil {
		t.Fatal("닿지 않는 원격의 fetch 는 실패해야 한다")
	}
	if err := runner.FetchAll(ctx, repo, ""); err == nil || !strings.Contains(err.Error(), "비어") {
		t.Fatalf("빈 원격 이름은 오류여야 한다: %v", err)
	}
	if _, err := runner.RemoteHeads(ctx, repo, ""); err == nil {
		t.Fatal("빈 원격 이름은 오류여야 한다")
	}
}

// 경로 포함은 문자열 접두어가 아니라 디렉터리 경계로 본다.
func TestWithin(t *testing.T) {
	sep := string(filepath.Separator)
	root := filepath.Join(sep, "wt", "a")
	cases := []struct {
		name string
		dir  string
		want bool
	}{
		{"자기 자신", root, true},
		{"바로 아래", filepath.Join(root, "sub"), true},
		{"깊이 아래", filepath.Join(root, "sub", "deeper"), true},
		{"이름이 접두어로 겹치는 이웃", root + "bc", false},
		{"부모", filepath.Join(sep, "wt"), false},
		{"형제", filepath.Join(sep, "wt", "b"), false},
		{"..로 시작하는 이름의 하위", filepath.Join(root, "..hidden"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := within(tc.dir, root); got != tc.want {
				t.Fatalf("within(%q, %q) = %v, 기대값 %v", tc.dir, root, got, tc.want)
			}
		})
	}
}

// 삭제는 본 체크아웃의 repo 로, 지우려는 worktree 바깥에서 불러야 한다. 윈도우는 어느 프로세스의 cwd 든
// 그 디렉터리를 지우지 못해 반쯤 지워진 worktree 가 남는데, macOS 와 리눅스는 허용하므로 git 이 아니라
// 코드가 막아야 하고, 그 거절이 플랫폼과 무관하게 나는지 여기서 본다.
func TestRemoveWorktreeRefusesCallsFromInsideTarget(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	remote, _ := seedRemote(t, base)
	main := filepath.Join(base, "main")
	run(t, base, "git", "clone", "--quiet", remote, main)
	configure(t, main)
	target := filepath.Join(base, "target")
	run(t, main, "git", "worktree", "add", "--quiet", "-b", "target", target)
	if err := os.MkdirAll(filepath.Join(target, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	runner := Runner{}
	mainRepo, err := runner.Discover(ctx, main)
	if err != nil {
		t.Fatal(err)
	}
	targetRepo, err := runner.Discover(ctx, target)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		repo    Repo
		cwd     string
		wantErr string
	}{
		{"지우려는 worktree 자신을 Discover 한 repo", targetRepo, base, "본 체크아웃"},
		{"지우려는 worktree 가 cwd", mainRepo, target, "안에서는"},
		{"지우려는 worktree 의 하위 디렉터리가 cwd", mainRepo, filepath.Join(target, "sub"), "안에서는"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(tc.cwd)
			err := runner.RemoveWorktree(ctx, tc.repo, target)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("거절해야 한다(%q): %v", tc.wantErr, err)
			}
			if _, err := os.Stat(filepath.Join(target, "f")); err != nil {
				t.Fatalf("거절된 worktree 는 그대로 있어야 한다: %v", err)
			}
		})
	}

	// 본 체크아웃의 repo 로 바깥에서 부르면 지워진다. 위의 거절이 멀쩡한 호출까지 막는 것이 아님을 확인한다.
	t.Chdir(main)
	if err := runner.RemoveWorktree(ctx, mainRepo, target); err != nil {
		t.Fatalf("본 체크아웃에서 부른 삭제는 되어야 한다: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("디렉터리가 사라져야 한다: %v", err)
	}

	// 삭제는 파일 수에 비례해 오래 걸린다. 로컬 명령의 짧은 제한 시간을 쓰면 큰 worktree 가 반만 지워진 채 남는다.
	if removeTimeout <= localTimeout {
		t.Fatalf("삭제 제한 시간(%v)은 로컬 명령의 제한 시간(%v)보다 길어야 한다", removeTimeout, localTimeout)
	}
}
