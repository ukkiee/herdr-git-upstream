package worktreeui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"herdr-git-upstream/internal/gitrepo"
	"herdr-git-upstream/internal/herdrcli"
	"herdr-git-upstream/internal/state"
)

// 아래 시험들은 실제 저장소로 시나리오를 만든다. 원격은 로컬 bare 저장소라 네트워크를 타지 않는다.
// seed 는 "다른 사람"의 작업 사본이고, work 는 본 체크아웃이며, worktree 들은 work 에서 갈라진다.

type fixture struct {
	base   string
	remote string
	seed   string
	work   string
	git    gitrepo.Runner
	main   gitrepo.Repo
	store  state.Store
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
		store:  state.Store{Dir: filepath.Join(base, "state"), Root: filepath.Join(base, "state")},
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
	main, err := f.git.Discover(context.Background(), f.work)
	if err != nil {
		t.Fatal(err)
	}
	f.main = main
	return f
}

// addWorktree는 main 에서 갈라진 새 브랜치의 worktree 를 만들고 그 경로를 돌려준다.
func (f *fixture) addWorktree(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(f.base, "wt", name)
	run(t, f.work, "git", "worktree", "add", "--quiet", "-b", name, path, "main")
	return path
}

// commitIn은 worktree 에 파일 하나를 커밋한다. 그래야 그 브랜치가 main 의 조상이 아니게 된다.
func (f *fixture) commitIn(t *testing.T, path, file, body string) {
	t.Helper()
	writeFile(t, filepath.Join(path, file), body)
	run(t, path, "git", "add", ".")
	run(t, path, "git", "commit", "--quiet", "-m", file)
}

// pushed는 브랜치를 올리고 upstream 을 잡는다.
func (f *fixture) pushed(t *testing.T, path, branch string) {
	t.Helper()
	run(t, path, "git", "push", "--quiet", "--set-upstream", "origin", branch)
}

// mergedOnRemote는 seed 에서 브랜치를 main 에 병합해 올린다. 그러면 origin/main 이 그 브랜치를 품는다.
func (f *fixture) mergedOnRemote(t *testing.T, branch string) {
	t.Helper()
	run(t, f.seed, "git", "fetch", "--quiet", "origin")
	run(t, f.seed, "git", "merge", "--quiet", "--no-ff", "-m", "merge "+branch, "origin/"+branch)
	run(t, f.seed, "git", "push", "--quiet", "origin", "main")
}

// safeWorktree는 병합되어 끝난 worktree 다. work 가 fetch 한 뒤에는 safe 로 판정되어야 한다.
func (f *fixture) safeWorktree(t *testing.T, name string) string {
	t.Helper()
	path := f.addWorktree(t, name)
	f.commitIn(t, path, name+".txt", "1")
	f.pushed(t, path, name)
	f.mergedOnRemote(t, name)
	return path
}

// dirtyWorktree는 손댄 것(추적되지 않은 파일)이 있는 worktree 다. review 여야 한다.
func (f *fixture) dirtyWorktree(t *testing.T, name string) string {
	t.Helper()
	path := f.addWorktree(t, name)
	f.commitIn(t, path, name+".txt", "1")
	writeFile(t, filepath.Join(path, "scratch.txt"), "untracked")
	return path
}

func (f *fixture) fetch(t *testing.T) {
	t.Helper()
	run(t, f.work, "git", "fetch", "--quiet", "origin")
}

func (f *fixture) deps() deps {
	return deps{Git: f.git, Store: f.store, Remote: "origin", Spawn: func(fn func()) { go fn() }}
}

// fakeHerdr는 herdr 노릇을 한다. 목록은 git 에게 물어 만들고, 어느 워크스페이스에 열려 있는지는 ids 가 말한다.
// 삭제는 herdr 가 하듯 git 으로 지운다. 실행 중인 herdr 에는 아무것도 보내지 않는다.
type fakeHerdr struct {
	mu   sync.Mutex
	git  gitrepo.Runner
	main gitrepo.Repo
	// listErr가 있으면 herdr 에 닿지 않는 것처럼 군다.
	listErr error
	// ids는 경로(정리된 것) → 워크스페이스 id 다. 없으면 열려 있지 않은 worktree 다.
	ids map[string]string
	// working은 에이전트가 일하는 중인 워크스페이스 id 들이다.
	working map[string]bool
	calls   []string
}

func newFakeHerdr(f *fixture) *fakeHerdr {
	return &fakeHerdr{git: f.git, main: f.main, ids: map[string]string{}, working: map[string]bool{}}
}

func (h *fakeHerdr) open(path, id string) { h.ids[canonical(path)] = id }

func (h *fakeHerdr) record(call string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, call)
}

func (h *fakeHerdr) recorded() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}

func (h *fakeHerdr) WorktreeList(ctx context.Context, workspaceID, cwd string) (herdrcli.WorktreeListResult, error) {
	if h.listErr != nil {
		return herdrcli.WorktreeListResult{}, h.listErr
	}
	entries, err := h.git.Worktrees(ctx, h.main)
	if err != nil {
		return herdrcli.WorktreeListResult{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	res := herdrcli.WorktreeListResult{Source: herdrcli.WorktreeSource{
		RepoName:           "fake",
		RepoRoot:           h.main.Root,
		SourceCheckoutPath: h.main.Root,
		SourceWorkspaceID:  h.ids[canonical(h.main.Root)],
	}}
	for i, entry := range entries {
		res.Worktrees = append(res.Worktrees, herdrcli.WorktreeItem{
			Path:             entry.Path,
			Branch:           entry.Branch,
			IsBare:           entry.Bare,
			IsDetached:       entry.Detached,
			IsLinkedWorktree: i > 0,
			IsPrunable:       entry.Prunable,
			OpenWorkspaceID:  h.ids[canonical(entry.Path)],
		})
	}
	return res, nil
}

func (h *fakeHerdr) WorkspaceList(ctx context.Context) ([]herdrcli.Workspace, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var list []herdrcli.Workspace
	for _, id := range h.ids {
		status := "idle"
		if h.working[id] {
			status = "working"
		}
		list = append(list, herdrcli.Workspace{WorkspaceID: id, AgentStatus: status})
	}
	return list, nil
}

func (h *fakeHerdr) WorktreeRemove(ctx context.Context, workspaceID string) error {
	h.record("remove " + workspaceID)
	h.mu.Lock()
	path := ""
	for p, id := range h.ids {
		if id == workspaceID {
			path = p
		}
	}
	h.mu.Unlock()
	if path == "" {
		return os.ErrNotExist
	}
	if err := h.git.RemoveWorktree(ctx, h.main, path); err != nil {
		return err
	}
	h.mu.Lock()
	delete(h.ids, path)
	h.mu.Unlock()
	return nil
}

func (h *fakeHerdr) WorktreeOpen(ctx context.Context, path string) error {
	h.record("open " + filepath.Base(path))
	return nil
}

func (h *fakeHerdr) WorkspaceFocus(ctx context.Context, id string) error {
	h.record("focus " + id)
	return nil
}

func run(t *testing.T, dir, name string, args ...string) {
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

// byLabel은 행들을 이름으로 찾기 쉽게 만든다.
func byLabel(rows []Row) map[string]Row {
	m := map[string]Row{}
	for _, row := range rows {
		m[row.Label()] = row
	}
	return m
}

// expectRow는 이름의 행이 기대한 판정과 설명인지 본다.
func expectRow(t *testing.T, rows []Row, label, verdict, detail string) {
	t.Helper()
	row, ok := byLabel(rows)[label]
	if !ok {
		t.Fatalf("%s 행이 없다: %+v", label, rows)
	}
	if row.Verdict.String() != verdict || row.Detail != detail {
		t.Fatalf("%s: %s %q, 기대값 %s %q", label, row.Verdict, row.Detail, verdict, detail)
	}
}
