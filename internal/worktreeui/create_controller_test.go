package worktreeui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"herdr-git-upstream/internal/gitrepo"
	"herdr-git-upstream/internal/herdrcli"
	"herdr-git-upstream/internal/tui"
)

type fakeCreateHerdr struct {
	*fakeHerdr
	f                 *fixture
	t                 *testing.T
	entered           chan context.Context
	release           chan struct{}
	createErr         error
	beforeCreateError func()
	muCreate          sync.Mutex
	created           []string
}

func (h *fakeCreateHerdr) WorktreeCreate(ctx context.Context, workspaceID, cwd, branch, base string) (string, error) {
	// herdr normalizes the supplied branch before creating or opening it.
	branch = strings.TrimSpace(branch)
	if h.entered != nil {
		h.entered <- ctx
		select {
		case <-h.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if h.createErr != nil {
		if h.beforeCreateError != nil {
			h.beforeCreateError()
		}
		return "", h.createErr
	}
	h.muCreate.Lock()
	h.created = append(h.created, workspaceID+"|"+cwd+"|"+branch+"|"+base)
	h.muCreate.Unlock()
	path := filepath.Join(h.f.base, "created", PathSlug(branch))
	exists, err := h.f.git.LocalBranchExists(ctx, h.f.main, branch)
	if err != nil {
		return "", err
	}
	if exists {
		run(h.t, h.f.work, "git", "worktree", "add", "--quiet", path, branch)
	} else {
		run(h.t, h.f.work, "git", "worktree", "add", "--quiet", "-b", branch, path, base)
	}
	return path, nil
}

func newCreateFake(t *testing.T, f *fixture) *fakeCreateHerdr {
	return &fakeCreateHerdr{fakeHerdr: newFakeHerdr(f), f: f, t: t}
}

func TestCreateCandidatesUseInvokingCheckoutAndCanonicalRefs(t *testing.T) {
	f := newFixture(t)
	linked := f.addWorktree(t, "widget-studio/dev")
	run(t, linked, "git", "branch", "--set-upstream-to=origin/main")
	run(t, f.work, "git", "branch", "origin/main")
	run(t, f.work, "git", "config", "--add", "git-upstream.mergeTarget", "origin/main")
	run(t, f.work, "git", "config", "--add", "git-upstream.mergeTarget", "origin/missing")
	h := newCreateFake(t, f)
	h.open(linked, "linked")
	h.open(f.work, "main-workspace")
	c, err := newCreateController(context.Background(), Options{CWD: f.work, WorkspaceID: "linked", Herdr: h, Git: f.git, Store: f.store})
	if err != nil {
		t.Fatal(err)
	}
	if canonical(c.repo.Root) != canonical(linked) || c.workspaceID != "main-workspace" || canonical(c.main.Root) != canonical(f.work) {
		t.Fatalf("current/source: %+v", c)
	}
	var refs []string
	for _, candidate := range c.m.Candidates {
		refs = append(refs, candidate.Ref)
	}
	if !slices.Equal(refs, []string{"refs/heads/widget-studio/dev", "refs/remotes/origin/main", "refs/remotes/origin/missing"}) {
		t.Fatalf("candidates order/dedup: %v", refs)
	}
	if c.m.Candidates[1].Kind != CandidateUpstream || c.m.Candidates[2].Kind != CandidateMergeTarget || c.m.Candidates[2].Status != "not fetched" {
		t.Fatalf("candidate kinds: %+v", c.m.Candidates)
	}
	if c.m.Name != "widget-studio/dev-2" || !c.m.Taken["origin/main"] || !c.m.Taken["main"] {
		t.Fatalf("auto/taken: %+v", c.m)
	}

	// 로컬 origin/main 과 원격 origin/main 은 서로 다른 후보다.
	run(t, linked, "git", "switch", "--quiet", "origin/main")
	c, err = newCreateController(context.Background(), Options{CWD: linked, Herdr: h, Git: f.git, Store: f.store})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.m.Candidates) < 2 || c.m.Candidates[0].Ref != "refs/heads/origin/main" || c.m.Candidates[1].Ref != "refs/remotes/origin/main" {
		t.Fatalf("same short name: %+v", c.m.Candidates)
	}
}

func TestCreateRequiresHerdr(t *testing.T) {
	f := newFixture(t)
	h := newCreateFake(t, f)
	h.listErr = &herdrcli.ResponseError{Code: "workspace_not_found", Message: "missing"}
	_, err := newCreateController(context.Background(), Options{CWD: f.work, Herdr: h, Git: f.git, Store: f.store})
	var rejected *herdrcli.ResponseError
	if !errors.As(err, &rejected) {
		t.Fatalf("explicit rejection lost: %v", err)
	}
	h.listErr = errors.New("server unavailable")
	if _, err := newCreateController(context.Background(), Options{CWD: f.work, Herdr: h, Git: f.git, Store: f.store}); err == nil {
		t.Fatal("creation cannot fall back to git")
	}
}

func TestCreateRemoteBaseUnsetsOnlyNewBranchUpstream(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[existing], func(t *testing.T) {
			f := newFixture(t)
			h := newCreateFake(t, f)
			if existing {
				run(t, f.work, "git", "branch", "--track", "chosen", "origin/main")
			}
			c, err := newCreateController(context.Background(), Options{CWD: f.work, Herdr: h, Git: f.git, Store: f.store})
			if err != nil {
				t.Fatal(err)
			}
			base := c.m.Candidates[1]
			outcome := c.create(context.Background(), "chosen", base)
			if outcome.err != nil || len(outcome.warnings) > 0 {
				t.Fatalf("create result: %+v", outcome)
			}
			path := filepath.Join(f.base, "created", "chosen")
			repo, err := f.git.Discover(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			up, err := f.git.Upstream(context.Background(), repo)
			if existing && (err != nil || up.TrackingRef != "refs/remotes/origin/main") {
				t.Fatalf("existing upstream altered: %+v %v", up, err)
			}
			if !existing && !errors.Is(err, gitrepo.ErrNoUpstream) {
				t.Fatalf("new upstream remains: %+v %v", up, err)
			}
			if err := f.store.AllowFreshening(f.main.CommonDir, "chosen"); err != nil {
				t.Fatalf("successful creation left pending protection: %v", err)
			}
		})
	}
}

func TestCreateCurrentChecksPreviousCreation(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "completed"}[completed], func(t *testing.T) {
			f := newFixture(t)
			h := newCreateFake(t, f)
			if err := f.store.SuppressFreshening(f.main.CommonDir, "chosen"); err != nil {
				t.Fatal(err)
			}
			if completed {
				if err := f.store.CompleteFresheningSuppression(f.main.CommonDir, "chosen"); err != nil {
					t.Fatal(err)
				}
			}
			c, err := newCreateController(context.Background(), Options{CWD: f.work, Herdr: h, Git: f.git, Store: f.store})
			if err != nil {
				t.Fatal(err)
			}
			outcome := c.create(context.Background(), "chosen", c.m.Candidates[0])
			if !completed {
				if outcome.err == nil || len(h.created) != 0 {
					t.Fatalf("pending creation was ignored: %+v %v", outcome, h.created)
				}
				return
			}
			if outcome.err != nil || len(h.created) != 1 {
				t.Fatalf("completed name could not be reused: %+v %v", outcome, h.created)
			}
			protected, err := f.store.FresheningSuppressed(f.main.CommonDir, "chosen")
			if err != nil || protected {
				t.Fatalf("new Current still suppressed: %t %v", protected, err)
			}
		})
	}
}

func TestCreateRejectionControlsSameNameRetry(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		retry bool
	}{
		{"missing workspace", &herdrcli.ResponseError{Code: "workspace_not_found", Message: "missing"}, true},
		{"not checkout", &herdrcli.ResponseError{Code: "not_git_worktree", Message: "not checkout"}, true},
		{"linked source", &herdrcli.ResponseError{Code: "linked_worktree_source", Message: "linked"}, true},
		{"timeout", context.DeadlineExceeded, false},
		{"transport", errors.New("connection reset"), false},
		{"partial success", &herdrcli.ResponseError{Code: "worktree_open_failed", Message: "open failed"}, false},
		{"other rejection", &herdrcli.ResponseError{Code: "worktree_creation_failed", Message: "failed"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			h := newCreateFake(t, f)
			h.createErr = tc.err
			c, err := newCreateController(context.Background(), Options{CWD: f.work, Herdr: h, Git: f.git, Store: f.store})
			if err != nil {
				t.Fatal(err)
			}
			first := c.create(context.Background(), "chosen", c.m.Candidates[1])
			if !errors.Is(first.err, tc.err) {
				t.Fatalf("original rejection lost: %v", first.err)
			}
			h.createErr = nil
			second := c.create(context.Background(), "chosen", c.m.Candidates[0])
			if tc.retry {
				if second.err != nil || len(h.created) != 1 {
					t.Fatalf("completed rejection blocks retry: %+v %v", second, h.created)
				}
			} else if second.err == nil || len(h.created) != 0 {
				t.Fatalf("uncertain creation allows same-name retry: %+v %v", second, h.created)
			}
		})
	}
}

func TestCreateRejectionPreservesCompletionFailure(t *testing.T) {
	f := newFixture(t)
	h := newCreateFake(t, f)
	rejection := &herdrcli.ResponseError{Code: "workspace_not_found", Message: "workspace vanished"}
	h.createErr = rejection
	h.beforeCreateError = func() {
		root := f.store.Root
		if root == "" {
			root = f.store.Dir
		}
		markers, err := filepath.Glob(filepath.Join(root, "popup-creations", "*.json"))
		if err != nil || len(markers) != 1 {
			t.Fatalf("protection markers: %v %v", markers, err)
		}
		if err := os.WriteFile(markers[0], []byte("corrupt"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c, err := newCreateController(context.Background(), Options{CWD: f.work, Herdr: h, Git: f.git, Store: f.store})
	if err != nil {
		t.Fatal(err)
	}
	got := c.create(context.Background(), "chosen", c.m.Candidates[1])
	if !errors.Is(got.err, rejection) || !strings.Contains(got.err.Error(), "cannot mark rejected creation complete") {
		t.Fatalf("must preserve rejection and completion error: %v", got.err)
	}
}

func TestCreateNormalizesNameBeforeProtectingBranch(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[existing], func(t *testing.T) {
			f := newFixture(t)
			h := newCreateFake(t, f)
			if existing {
				run(t, f.work, "git", "branch", "--track", "chosen", "origin/main")
			}
			c, err := newCreateController(context.Background(), Options{CWD: f.work, Herdr: h, Git: f.git, Store: f.store})
			if err != nil {
				t.Fatal(err)
			}
			outcome := c.create(context.Background(), " chosen ", c.m.Candidates[1])
			if outcome.err != nil || len(outcome.warnings) != 0 {
				t.Fatalf("normalized creation: %+v", outcome)
			}
			protected, err := f.store.FresheningSuppressed(f.main.CommonDir, "chosen")
			if err != nil || !protected {
				t.Fatalf("actual branch unprotected: %t %v", protected, err)
			}
			repo, err := f.git.Discover(context.Background(), filepath.Join(f.base, "created", "chosen"))
			if err != nil {
				t.Fatal(err)
			}
			up, err := f.git.Upstream(context.Background(), repo)
			if existing && (err != nil || up.TrackingRef != "refs/remotes/origin/main") {
				t.Fatalf("existing upstream altered: %+v %v", up, err)
			}
			if !existing && !errors.Is(err, gitrepo.ErrNoUpstream) {
				t.Fatalf("new upstream remains: %+v %v", up, err)
			}
		})
	}
}

func TestCreateLoopWaitsForCreationAndCleanupOnCancel(t *testing.T) {
	f := newFixture(t)
	h := newCreateFake(t, f)
	h.entered, h.release = make(chan context.Context, 1), make(chan struct{})
	c, err := newCreateController(context.Background(), Options{CWD: f.work, Herdr: h, Git: f.git, Store: f.store})
	if err != nil {
		t.Fatal(err)
	}
	c.m.Selected = 1
	c.m.Name = "chosen"
	c.m.UserEdited = true
	keys := make(chan tui.Key)
	done := make(chan createOutcome, 1)
	models := make(chan CreateModel, 32)
	c.afterDraw = func(m CreateModel) {
		select {
		case models <- m:
		default:
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		done <- c.loop(ctx, keys, &syncBuffer{}, func() (int, int) { return 80, 24 }, func(fn func()) { go fn() })
	}()
	deadline := time.After(10 * time.Second)
	for ready := false; !ready; {
		select {
		case m := <-models:
			ready = m.Candidates[1].Status == "up to date"
		case <-deadline:
			t.Fatal("selected fetch not finished")
		}
	}
	keys <- tui.Key{Kind: tui.KeyEnter}
	var createCtx context.Context
	select {
	case createCtx = <-h.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("create not entered")
	}
	cancel()
	select {
	case result := <-done:
		close(h.release)
		t.Fatalf("returned before create: %+v", result)
	case <-createCtx.Done():
		close(h.release)
		t.Fatal("create context canceled")
	case <-time.After(100 * time.Millisecond):
	}
	close(h.release)
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cleanup not finished")
	}
	path := filepath.Join(f.base, "created", "chosen")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	repo, _ := f.git.Discover(context.Background(), path)
	if _, err := f.git.Upstream(context.Background(), repo); !errors.Is(err, gitrepo.ErrNoUpstream) {
		t.Fatalf("cleanup incomplete: %v", err)
	}
}

func TestUpdateCreateRejectsUnfetchedRemoteBase(t *testing.T) {
	for _, status := range []string{"fetching…", "not fetched", "fetch failed"} {
		m := createSample()
		m.Selected = 1
		m.Candidates[1].Status = status
		got, action := UpdateCreate(m, tui.Key{Kind: tui.KeyEnter})
		if action != CreateNone || !strings.Contains(got.Message, "fetch") {
			t.Fatalf("unsafe fetch status %q: %+v %v", status, got, action)
		}
	}
}

func TestCreateFetchesMissingCandidateAndLearnsDefault(t *testing.T) {
	for _, kind := range []string{"missing tracking ref", "unknown default"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			h := newCreateFake(t, f)
			wantRef := "refs/remotes/origin/dev"
			wantKind := CandidateMergeTarget
			if kind == "missing tracking ref" {
				run(t, f.seed, "git", "push", "--quiet", "origin", "main:dev")
				run(t, f.work, "git", "config", "--add", "git-upstream.mergeTarget", "origin/dev")
			} else {
				run(t, f.work, "git", "branch", "--unset-upstream")
				run(t, f.work, "git", "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
				wantRef = "refs/remotes/origin/main"
				wantKind = CandidateDefault
			}
			c, err := newCreateController(context.Background(), Options{CWD: f.work, Herdr: h, Git: f.git, Store: f.store})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "missing tracking ref" {
				if _, err := f.git.CommitOf(context.Background(), f.main, wantRef); err == nil {
					t.Fatal("fixture must start without tracking ref")
				}
			} else if c.lookupRemote != "origin" {
				t.Fatalf("default lookup should be deferred: %q", c.lookupRemote)
			}
			keys := make(chan tui.Key)
			done := make(chan createOutcome, 1)
			models := make(chan CreateModel, 32)
			c.afterDraw = func(m CreateModel) {
				select {
				case models <- m:
				default:
				}
			}
			go func() {
				done <- c.loop(context.Background(), keys, &syncBuffer{}, func() (int, int) { return 80, 24 }, func(fn func()) { go fn() })
			}()
			ready := false
			deadline := time.After(10 * time.Second)
			for !ready {
				select {
				case m := <-models:
					for _, candidate := range m.Candidates {
						if candidate.Ref == wantRef && candidate.Kind == wantKind && candidate.Status == "up to date" {
							ready = true
						}
					}
				case <-deadline:
					t.Fatal("remote candidate never became ready")
				}
			}
			keys <- tui.Key{Kind: tui.KeyEsc}
			if result := <-done; result.err != nil {
				t.Fatal(result.err)
			}
			if _, err := f.git.CommitOf(context.Background(), f.main, wantRef); err != nil {
				t.Fatalf("tracking ref not fetched: %v", err)
			}
		})
	}
}

func TestCreateKeepsTypingAndCancelResponsiveDuringFetch(t *testing.T) {
	f := newFixture(t)
	h := newCreateFake(t, f)
	c, err := newCreateController(context.Background(), Options{CWD: f.work, Herdr: h, Git: f.git, Store: f.store})
	if err != nil {
		t.Fatal(err)
	}
	keys := make(chan tui.Key)
	done := make(chan createOutcome, 1)
	models := make(chan CreateModel, 32)
	release := make(chan struct{})
	c.afterDraw = func(m CreateModel) {
		select {
		case models <- m:
		default:
		}
	}
	go func() {
		done <- c.loop(context.Background(), keys, &syncBuffer{}, func() (int, int) { return 80, 24 }, func(fn func()) { go func() { <-release; fn() }() })
	}()
	select {
	case <-models:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("first draw blocked by fetch")
	}
	keys <- key('x')
	select {
	case m := <-models:
		if m.Name != "x" {
			t.Fatalf("typing during fetch: %q", m.Name)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("typing blocked by fetch")
	}
	keys <- tui.Key{Kind: tui.KeyEsc}
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("cancel blocked by fetch")
	}
	close(release)
}

func TestCreateSlashRemoteTakenNames(t *testing.T) {
	f := newFixture(t)
	run(t, f.work, "git", "remote", "rename", "origin", "team/upstream")
	run(t, f.work, "git", "update-ref", "refs/remotes/team/upstream/main-2", "HEAD")
	run(t, f.work, "git", "branch", "origin/main")
	c, err := newCreateController(context.Background(), Options{CWD: f.work, Herdr: newCreateFake(t, f), Git: f.git, Store: f.store})
	if err != nil {
		t.Fatal(err)
	}
	if !c.m.Taken["main-2"] || !c.m.Taken["origin/main"] || c.m.Taken["upstream/main-2"] {
		t.Fatalf("taken: %v", c.m.Taken)
	}
	c.m.Selected = 1
	c.m = c.m.autoName()
	if c.m.Name != "main-3" {
		t.Fatalf("slash remote auto name: %q", c.m.Name)
	}
}
