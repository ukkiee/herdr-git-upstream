package worktreeui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"herdr-git-upstream/internal/freshen"
	"herdr-git-upstream/internal/gitrepo"
	"herdr-git-upstream/internal/herdrcli"
	"herdr-git-upstream/internal/herdrconfig"
	"herdr-git-upstream/internal/judge"
	"herdr-git-upstream/internal/state"
	"herdr-git-upstream/internal/tui"
)

// CreateHerdr는 생성 팝업만 쓰는 명령을 더한다. worktree 화면과 그 시험의 Herdr 구현은 바꾸지 않는다.
type CreateHerdr interface {
	Herdr
	WorktreeCreate(ctx context.Context, workspaceID, cwd, branch, base string) (string, error)
}

type createController struct {
	herdr CreateHerdr
	git   gitrepo.Runner
	store state.Store
	// repo는 호출한 체크아웃, main은 herdr 가 생성 요청을 받을 본 체크아웃이다.
	repo             gitrepo.Repo
	main             gitrepo.Repo
	workspaceID      string
	worktreeBranches []string
	lookupRemote     string
	warnings         []string
	m                CreateModel
	afterDraw        func(CreateModel)
}

// RunCreate는 생성 팝업을 연다. 생성은 herdr 를 통해서만 하며, 경고는 대체 화면을 닫은 뒤 출력한다.
func RunCreate(ctx context.Context, opts Options) (err error) {
	c, err := newCreateController(ctx, opts)
	if err != nil {
		return err
	}
	term, err := tui.Open()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNoTerminal, err)
	}
	defer term.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	result := c.loop(ctx, term.Keys(ctx), term.Output(), term.Size, term.Go)
	closeErr := term.Close()
	for _, warning := range append(c.warnings, result.warnings...) {
		fmt.Fprintln(os.Stderr, "new-worktree: warning: "+warning)
	}
	return errors.Join(result.err, closeErr)
}

func newCreateController(ctx context.Context, opts Options) (*createController, error) {
	if opts.Herdr == nil {
		opts.Herdr = herdrcli.New()
	}
	herdr, ok := opts.Herdr.(CreateHerdr)
	if !ok {
		return nil, errors.New("herdr does not support worktree creation")
	}
	if opts.Store == (state.Store{}) {
		opts.Store = state.New()
	}
	listed, err := herdr.WorktreeList(ctx, opts.WorkspaceID, opts.CWD)
	if err != nil {
		return nil, fmt.Errorf("herdr is required to create a worktree: %w", err)
	}
	inv, err := fromHerdr(ctx, opts.Git, listed, opts.CWD)
	if err != nil {
		return nil, err
	}
	currentPath := opts.CWD
	if opts.WorkspaceID != "" {
		currentPath = ""
		if opts.WorkspaceID == listed.Source.SourceWorkspaceID {
			currentPath = inv.Main.Root
		}
		for _, item := range listed.Worktrees {
			if item.OpenWorkspaceID == opts.WorkspaceID {
				currentPath = item.Path
				break
			}
		}
		if currentPath == "" {
			return nil, errors.New("cannot locate the invoking workspace checkout")
		}
	}
	if currentPath == "" {
		currentPath = inv.Main.Root
	}
	repo, err := opts.Git.Discover(ctx, currentPath)
	if err != nil {
		return nil, notRepository{dir: currentPath}
	}
	if repo.CommonDir != inv.Main.CommonDir {
		return nil, errors.New("herdr and the invoking checkout refer to different repositories")
	}
	c := &createController{herdr: herdr, git: opts.Git, store: opts.Store, repo: repo, main: inv.Main, workspaceID: listed.Source.SourceWorkspaceID}
	for _, item := range listed.Worktrees {
		if item.Branch != "" {
			c.worktreeBranches = append(c.worktreeBranches, item.Branch)
		}
	}
	candidates, lookupRemote, err := c.candidates(ctx)
	if err != nil {
		return nil, err
	}
	c.lookupRemote = lookupRemote
	taken, err := c.takenNames(ctx)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		taken[strings.TrimPrefix(candidate.nameRef(), "refs/heads/")] = true
	}
	scan, scanErr := herdrconfig.Load()
	if scanErr != nil {
		c.warnings = append(c.warnings, "cannot read herdr configuration; using the default path preview: "+scanErr.Error())
	}
	c.m = CreateModel{RepoName: inv.RepoName, PathRoot: scan.WorktreeDirectory(), Candidates: candidates, Taken: taken}
	c.m = c.m.autoName()
	return c, nil
}

// candidates는 로컬 자료만 읽는다. 기본 브랜치를 모르고 조회할 때가 되었으면 별도 fetch 작업을 요청한다.
func (c *createController) candidates(ctx context.Context) ([]Candidate, string, error) {
	branch, err := c.git.CurrentBranch(ctx, c.repo)
	if err != nil {
		return nil, "", err
	}
	var candidates []Candidate
	seen := map[string]bool{}
	add := func(candidate Candidate) {
		if candidate.Ref != "" && !seen[candidate.Ref] {
			seen[candidate.Ref] = true
			candidates = append(candidates, candidate)
		}
	}
	var upstream *gitrepo.Upstream
	if branch != "" {
		if up, err := c.git.UpstreamFor(ctx, c.repo, branch); err == nil {
			upstream = &up
		}
		add(Candidate{Label: branch, Ref: "refs/heads/" + branch, Kind: CandidateCurrent, Status: c.currentStatus(ctx, upstream)})
	}
	remoteCandidate := func(up gitrepo.Upstream, kind CandidateKind) Candidate {
		status := "up to date"
		if _, err := c.git.CommitOf(ctx, c.repo, up.TrackingRef); err != nil {
			status = "not fetched"
		}
		return Candidate{Label: strings.TrimPrefix(up.TrackingRef, "refs/remotes/"), Ref: up.TrackingRef, Kind: kind, Fetch: up, Status: status}
	}
	if upstream != nil {
		add(remoteCandidate(*upstream, CandidateUpstream))
	}
	remote, known := judge.RemoteFor(ctx, c.git, c.repo, upstream)
	lookup := ""
	if known {
		found, err := judge.IntegrationBranches(ctx, c.git, c.repo, remote, c.store, time.Now())
		if err != nil {
			return nil, "", err
		}
		for i, up := range found.Branches {
			kind := CandidateMergeTarget
			if i == 0 && found.DefaultKnown {
				kind = CandidateDefault
			}
			add(remoteCandidate(up, kind))
		}
		if found.LookupDue {
			lookup = remote
		}
	}
	refs, err := c.git.BranchRefs(ctx, c.repo)
	if err != nil {
		return nil, "", err
	}
	for _, ref := range refs {
		if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
			add(Candidate{Label: name, Ref: ref, Kind: CandidateLocal, Status: "up to date"})
		}
	}
	branches, err := c.git.RemoteBranches(ctx, c.repo)
	if err != nil {
		return nil, "", err
	}
	for _, up := range branches {
		add(Candidate{Label: strings.TrimPrefix(up.TrackingRef, "refs/remotes/"), Ref: up.TrackingRef, Kind: CandidateRemote, Fetch: up, Status: "not fetched"})
	}
	return candidates, lookup, nil
}

func (c *createController) currentStatus(ctx context.Context, up *gitrepo.Upstream) string {
	if up == nil {
		return ""
	}
	counts, err := c.git.CountsFor(ctx, c.repo, *up)
	if err != nil {
		return "not fetched"
	}
	if counts.Behind > 0 {
		return "↓" + strconv.Itoa(counts.Behind) + " behind"
	}
	return "up to date"
}

// takenNames는 전체 참조의 종류를 먼저 가른다. 실제 원격 이름의 가장 긴 접두사를 떼어 slash 원격도 처리한다.
func (c *createController) takenNames(ctx context.Context) (map[string]bool, error) {
	refs, err := c.git.BranchRefs(ctx, c.repo)
	if err != nil {
		return nil, err
	}
	remotes, err := c.git.Remotes(ctx, c.repo)
	if err != nil {
		return nil, err
	}
	sort.Slice(remotes, func(i, j int) bool { return len(remotes[i]) > len(remotes[j]) })
	taken := map[string]bool{}
	for _, ref := range refs {
		if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
			taken[name] = true
			continue
		}
		short, ok := strings.CutPrefix(ref, "refs/remotes/")
		if !ok {
			continue
		}
		for _, remote := range remotes {
			if name, ok := strings.CutPrefix(short, remote+"/"); ok {
				if name != "HEAD" {
					taken[name] = true
				}
				break
			}
		}
	}
	for _, branch := range c.worktreeBranches {
		taken[branch] = true
	}
	return taken, nil
}

type createOutcome struct {
	err      error
	warnings []string
}

// create는 새 브랜치인지 생성 직전에 다시 확인한다. 기존 브랜치는 upstream 을 유지하고, 의도한 기준과
// 다른 곳으로 생성 이벤트가 최신화하지 않도록 WorktreeCreate 전에 공용 억제 기록을 남긴다.
func (c *createController) create(ctx context.Context, name string, base Candidate) createOutcome {
	// Match herdr's normalization before existence checks, suppression, and cleanup.
	name = strings.TrimSpace(name)
	if name == "" {
		return createOutcome{err: errors.New("enter a branch name")}
	}
	existing, err := c.git.LocalBranchExists(ctx, c.main, name)
	if err != nil {
		return createOutcome{err: err}
	}
	protected := existing || base.Kind != CandidateCurrent
	if protected {
		if err := freshen.SuppressCreation(c.store, c.main, name); err != nil {
			return createOutcome{err: fmt.Errorf("cannot protect the selected base: %w", err)}
		}
	} else if err := freshen.AllowCreation(c.store, c.main, name); err != nil {
		return createOutcome{err: err}
	}
	path, err := c.herdr.WorktreeCreate(ctx, c.workspaceID, c.main.Root, name, base.Ref)
	if err != nil {
		var rejected *herdrcli.ResponseError
		if protected && errors.As(err, &rejected) {
			// These source checks run before herdr creates a branch or worktree. Other
			// failures may leave creation running or partly complete, so retain pending.
			switch rejected.Code {
			case "workspace_not_found", "not_git_worktree", "linked_worktree_source":
				if completionErr := freshen.CompleteCreation(c.store, c.main, name); completionErr != nil {
					err = errors.Join(err, fmt.Errorf("cannot mark rejected creation complete: %w", completionErr))
				}
			}
		}
		return createOutcome{err: err}
	}
	outcome := createOutcome{}
	if protected {
		if err := freshen.CompleteCreation(c.store, c.main, name); err != nil {
			outcome.warnings = append(outcome.warnings, "worktree created at "+path+"; cannot mark creation complete: "+err.Error())
		}
	}
	if existing || base.Kind == CandidateCurrent {
		return outcome
	}
	warn := func(err error) createOutcome {
		outcome.warnings = append(outcome.warnings, "worktree created at "+path+"; cannot unset its upstream: "+err.Error())
		return outcome
	}
	repo, err := c.git.Discover(ctx, path)
	if err != nil {
		return warn(err)
	}
	branch, err := c.git.CurrentBranch(ctx, repo)
	if err != nil {
		return warn(err)
	}
	if branch != name {
		return warn(errors.New("the checked out branch changed"))
	}
	up, err := c.git.Upstream(ctx, repo)
	if errors.Is(err, gitrepo.ErrNoUpstream) {
		return outcome
	}
	if err != nil {
		return warn(err)
	}
	if up.Remote != base.Fetch.Remote || up.RemoteRef != base.Fetch.RemoteRef {
		return warn(errors.New("the upstream changed"))
	}
	if err := c.git.UnsetUpstream(ctx, repo); err != nil {
		return warn(err)
	}
	return outcome
}

type createFetched struct {
	ref, status    string
	currentStatus  string
	currentUpdated bool
	taken          map[string]bool
	candidates     []Candidate
	err            error
}

// loop는 입력, 후보 fetch, 생성 결과를 한 고루틴에서 모델에 적용한다. 생성만 취소로부터 보호하고
// EOF/취소/닫기에도 결과와 upstream 정리를 기다린다. 네트워크 fetch 는 팝업을 닫으면 취소한다.
func (c *createController) loop(ctx context.Context, keys <-chan tui.Key, out io.Writer, size func() (int, int), spawn func(func())) (result createOutcome) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	fetchedCh := make(chan createFetched, 16)
	createdCh := make(chan createOutcome, 1)
	creating := false
	defer func() {
		if creating {
			result = <-createdCh
		}
	}()
	limit := make(chan struct{}, maxParallel)
	fetching := map[string]bool{}
	run := func(fn func() createFetched) {
		spawn(func() {
			select {
			case limit <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-limit }()
			got := fn()
			select {
			case fetchedCh <- got:
			case <-ctx.Done():
			}
		})
	}
	startFetch := func() {
		c.m.Candidates = append([]Candidate(nil), c.m.Candidates...)
		for i, candidate := range c.m.Candidates {
			if candidate.Fetch.Remote == "" || fetching[candidate.Ref] || (candidate.Kind == CandidateRemote && i != c.m.Selected) {
				continue
			}
			fetching[candidate.Ref] = true
			c.m.Candidates[i].Status = "fetching…"
			run(func() createFetched {
				got := createFetched{ref: candidate.Ref, status: "up to date"}
				if err := c.git.Fetch(ctx, c.repo, candidate.Fetch); err != nil {
					got.status = "fetch failed"
				}
				if up, err := c.git.Upstream(ctx, c.repo); err == nil && up.TrackingRef == candidate.Ref {
					got.currentUpdated = true
					got.currentStatus = c.currentStatus(ctx, &up)
					if got.status == "fetch failed" {
						got.currentStatus = "fetch failed"
					}
				}
				got.taken, got.err = c.takenNames(ctx)
				return got
			})
		}
	}
	draw := func() {
		cols, rows := size()
		c.m.Cols, c.m.Rows = cols, rows
		if c.m.MessageOpen {
			wrapped := wrapCreateMessage(c.m.Message, max(1, min(cols, 64)-4))
			c.m.MessageOffset = max(0, min(c.m.MessageOffset, len(wrapped)-max(1, rows-4)))
		}
		tui.Frame(out, RenderCreate(c.m, cols, rows), cols, rows)
		if c.afterDraw != nil {
			c.afterDraw(c.m)
		}
	}
	startFetch()
	draw()
	if c.lookupRemote != "" {
		run(func() createFetched {
			if err := judge.LookupDefaultBranch(ctx, c.git, c.repo, c.lookupRemote, c.store, time.Now()); err != nil {
				return createFetched{err: fmt.Errorf("default branch lookup failed: %w", err)}
			}
			candidates, _, err := c.candidates(ctx)
			return createFetched{candidates: candidates, err: err}
		})
	}
	for {
		select {
		case <-ctx.Done():
			if creating {
				return createOutcome{}
			}
			return createOutcome{err: ctx.Err()}
		case got := <-fetchedCh:
			c.m.Candidates = append([]Candidate(nil), c.m.Candidates...)
			if got.candidates != nil {
				selected, _ := c.m.base()
				old := map[string]string{}
				for _, candidate := range c.m.Candidates {
					old[candidate.Ref] = candidate.Status
				}
				c.m.Candidates = got.candidates
				c.m.Selected = 0
				for i, candidate := range c.m.Candidates {
					if status, ok := old[candidate.Ref]; ok {
						c.m.Candidates[i].Status = status
					}
					if candidate.Ref == selected.Ref {
						c.m.Selected = i
					}
				}
				startFetch()
			}
			for i, candidate := range c.m.Candidates {
				if candidate.Ref == got.ref {
					c.m.Candidates[i].Status = got.status
				}
				if candidate.Kind == CandidateCurrent && got.currentUpdated {
					c.m.Candidates[i].Status = got.currentStatus
				}
			}
			if got.taken != nil {
				merged := map[string]bool{}
				for name := range c.m.Taken {
					merged[name] = true
				}
				for name := range got.taken {
					merged[name] = true
				}
				c.m.Taken = merged
			}
			if got.err != nil {
				c.m.Message = got.err.Error()
			}
			if !c.m.Busy {
				c.m = c.m.autoName()
			}
			draw()
		case got := <-createdCh:
			creating = false
			c.m.Busy = false
			if got.err == nil {
				return got
			}
			c.m.Message = got.err.Error()
			draw()
		case k, ok := <-keys:
			if !ok {
				return createOutcome{}
			}
			var action CreateAction
			previousBase, _ := c.m.base()
			c.m, action = UpdateCreate(c.m, k)
			if base, ok := c.m.base(); ok && base.Ref != previousBase.Ref {
				startFetch()
			}
			switch action {
			case CreateCancel:
				return createOutcome{}
			case CreateSubmit:
				base, _ := c.m.base()
				name := c.m.Name
				creating = true
				c.m.Busy = true
				c.m.Message = "creating…"
				spawn(func() { createdCh <- c.create(context.WithoutCancel(ctx), name, base) })
			}
			draw()
		}
	}
}
