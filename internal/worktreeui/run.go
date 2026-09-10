package worktreeui

// 이 파일은 컨트롤러다. 터미널을 열고, 배경에서 자료를 모으고 fetch 하고 지우며, 키를 모델에 적용하고 그린다.
// 모델은 loop 를 도는 고루틴만 만진다. 배경 일은 결과를 채널로 돌려주고 loop 가 그것을 모델에 옮긴다.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"herdr-git-upstream/internal/gitrepo"
	"herdr-git-upstream/internal/herdrcli"
	"herdr-git-upstream/internal/judge"
	"herdr-git-upstream/internal/state"
	"herdr-git-upstream/internal/tui"
)

// Herdr는 컨트롤러가 herdr 에게 묻고 시키는 것들이다. herdrcli.Client 가 그대로 맞고, 시험은 가짜를 넣는다.
type Herdr interface {
	WorktreeList(ctx context.Context, workspaceID, cwd string) (herdrcli.WorktreeListResult, error)
	WorkspaceList(ctx context.Context) ([]herdrcli.Workspace, error)
	WorktreeRemove(ctx context.Context, workspaceID string) error
	WorktreeOpen(ctx context.Context, path string) error
	WorkspaceFocus(ctx context.Context, id string) error
}

// Options는 Run 에 주는 것들이다. 영값은 명령줄에서 부를 때의 기본값이다.
type Options struct {
	// CWD는 저장소를 찾는 자리다. WorkspaceID 가 비어 있을 때 herdr 에게 --cwd 로 건네고, herdr 없이는 git 이 여기서 찾는다.
	CWD string
	// WorkspaceID가 있으면 그 워크스페이스가 속한 저장소다. 액션으로 열리면 HERDR_WORKSPACE_ID 가 온다.
	WorkspaceID string
	// Herdr가 nil 이면 herdrcli.New() 를 쓴다.
	Herdr Herdr
	// Git은 fetch 제한 시간을 들고 온다. 영값은 gitrepo 의 기본값이다.
	Git gitrepo.Runner
	// Store가 영값이면 state.New() 를 쓴다. 데몬의 fetch 기록과 따라잡기 캐시를 나눠 쓰는 자리다.
	Store state.Store
}

// ErrNoTerminal은 표준 입출력이 터미널이 아니거나 raw 모드로 바꾸지 못해 화면을 열 수 없다는 뜻이다.
// 명령은 이것을 종료 코드 2 로 옮긴다. 저장소가 아닌 것(1)과 갈라야 스크립트가 원인을 안다.
var ErrNoTerminal = errors.New("cannot open the screen")

// Run은 worktree 화면을 띄우고 닫힐 때까지 돈다.
//
// 저장소를 정하는 일은 터미널을 열기 전에 한다. 저장소가 아니면 그 문구가 대체 화면이 아니라 셸에 남아야 한다.
// 자료 모으기와 fetch 는 터미널을 연 뒤 배경에서 한다. 스물일곱 개의 worktree 에 git 을 묻는 동안 빈 화면이라도
// 먼저 떠야 사람이 멈춘 줄로 알지 않는다.
func Run(ctx context.Context, opts Options) (err error) {
	c, err := newController(ctx, opts)
	if err != nil {
		return err
	}
	term, err := tui.Open()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNoTerminal, err)
	}
	defer func() {
		if closeErr := term.Close(); err == nil {
			err = closeErr
		}
	}()
	// 화면이 닫히면 배경 일도 끊는다. fetch 는 끊어도 잃을 것이 없다. 삭제는 loop 가 끝나기 전에 기다린다.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	return c.loop(ctx, term.Keys(ctx), term.Output(), term.Size, term.Go)
}

// controller는 화면 하나의 살림이다. m 과 heads 와 fetchedAt 은 loop 고루틴만 만진다.
type controller struct {
	herdr Herdr
	deps  deps
	// cwd는 목록을 다시 물을 때 herdr 에게 건네는 자리다. 삭제 뒤에는 목록이 달라지므로 다시 묻는다.
	cwd string
	// main은 본 체크아웃이다. fetch 와 git 삭제가 여기서 돈다.
	main gitrepo.Repo

	m Model
	// heads는 마지막 ls-remote 결과다. nil 이면 아직 없거나 실패한 것이고, 그때 gone 은 데몬 기록으로 판정한다.
	heads     map[string]bool
	fetchedAt time.Time
	// lookupRemotes는 다음 fetch 때 기본 브랜치를 물어볼 원격들이다.
	lookupRemotes []string

	// afterDraw는 시험이 그려진 모델을 받아 보는 고리다. 실제 화면에서는 nil 이다.
	afterDraw func(Model)
}

// newController는 저장소를 정하고 fetch 할 원격을 고른다. 여기까지가 터미널 없이 하는 일이다.
func newController(ctx context.Context, opts Options) (*controller, error) {
	if opts.Herdr == nil {
		opts.Herdr = herdrcli.New()
	}
	if opts.Store == (state.Store{}) {
		opts.Store = state.New()
	}
	inv, err := resolve(ctx, opts.Herdr, opts.Git, opts.WorkspaceID, opts.CWD)
	if err != nil {
		return nil, err
	}
	// 다음부터는 본 체크아웃의 경로로 목록을 묻는다. 시작한 자리는 지워질 수 있는 worktree 안일 수 있고(herdr 는
	// 그 워크스페이스에서 열린 화면이 자기 worktree 를 지우는 것을 막지 않는다), 그 워크스페이스도 삭제와 함께
	// 사라진다. 본 체크아웃은 지울 수 없으므로 화면이 사는 동안 남아 있다.
	c := &controller{
		herdr: opts.Herdr,
		cwd:   inv.Main.Root,
		main:  inv.Main,
		m:     Model{RepoName: inv.RepoName, HerdrUnavailable: !inv.HerdrAvailable},
	}
	c.deps = deps{
		Git:    opts.Git,
		Store:  opts.Store,
		Remote: remoteFor(ctx, opts.Git, inv.Main),
		Spawn:  func(fn func()) { go fn() },
	}
	return c, nil
}

// gathered는 자료를 모은 결과다.
type gathered struct {
	rows             []Row
	herdrUnavailable bool
	lookupRemotes    []string
	err              error
}

// gather는 목록을 다시 묻고 worktree 마다 자료를 모은다. 배경 고루틴에서 돈다. c 의 바뀌지 않는 것만 읽는다.
//
// 목록을 매번 다시 묻는 이유가 있다. 삭제 뒤에는 그 worktree 가 목록에서 사라져야 하고, 그 사이 사람이 herdr 에서
// worktree 를 열었을 수도 있다. herdr 에 닿는지도 그때그때 다르다.
func (c *controller) gather(ctx context.Context, heads map[string]bool) gathered {
	inv, err := resolve(ctx, c.herdr, c.deps.Git, "", c.cwd)
	if err != nil {
		return gathered{err: err}
	}
	agents := agentStatuses(ctx, c.herdr, inv)
	now := time.Now()
	targets := integrationFor(ctx, c.deps.Git, c.deps.Store, inv.Main, c.deps.Remote, now)
	rows := collect(ctx, c.deps, inv, agents, heads, targets, now)
	return gathered{rows: rows, herdrUnavailable: !inv.HerdrAvailable, lookupRemotes: targets.LookupRemotes}
}

// fetched는 배경 fetch 의 결과다.
type fetched struct {
	heads    map[string]bool
	fetchErr error
	at       time.Time
}

// fetch는 전체 fetch 와 ls-remote 를 나란히 돌린다. 각각 왕복 한 번이라 겹쳐야 기다리는 시간이 반이 된다.
// 원격 기본 브랜치를 모르면 그것도 함께 묻는다. 네트워크를 타는 일은 모두 이 자리에서 한다.
func (c *controller) fetch(ctx context.Context, lookupRemotes []string) fetched {
	var result fetched
	pending := 2 + len(lookupRemotes)
	done := make(chan struct{}, pending)
	c.deps.Spawn(func() {
		defer func() { done <- struct{}{} }()
		result.fetchErr = c.deps.Git.FetchAll(ctx, c.main, c.deps.Remote)
	})
	c.deps.Spawn(func() {
		defer func() { done <- struct{}{} }()
		if heads, err := c.deps.Git.RemoteHeads(ctx, c.main, c.deps.Remote); err == nil {
			result.heads = heads
		}
	})
	for _, remote := range lookupRemotes {
		c.deps.Spawn(func() {
			defer func() { done <- struct{}{} }()
			_ = judge.LookupDefaultBranch(ctx, c.deps.Git, c.main, remote, c.deps.Store, time.Now())
		})
	}
	for ; pending > 0; pending-- {
		<-done
	}
	result.at = time.Now()
	return result
}

// removed는 배경 삭제의 결과다. message 가 마지막 줄에 보인다.
type removed struct {
	message string
}

// remove는 행들을 차례로 지운다. herdr 에 열려 있으면 herdr 에게, 아니면 git 에게 시킨다. 강제는 없다(ADR 0002).
// 각 삭제 직전에 목록과 판정을 다시 읽는다. Git 의 dirty 검사는 새로 커밋한 작업을 보호하지 못하므로
// 확인한 경로, 브랜치, HEAD, 워크스페이스가 그대로이고 여전히 safe 인 것만 삭제한다.
//
// 하나가 실패해도 나머지는 계속한다. 열 개를 지우다 셋째가 손댄 것이 있어 막혔다고 나머지 일곱을 남겨 두면
// 사람이 D 를 다시 눌러야 하고, 그때 셋째가 또 막는다.
func (c *controller) remove(ctx context.Context, rows []Row, heads map[string]bool) removed {
	count := 0
	var firstErr error
	var firstFailed string
	for _, row := range rows {
		current := c.gather(ctx, heads)
		err := removalCheck(row, current)
		if err == nil {
			if row.OpenWorkspaceID != "" {
				err = c.herdr.WorktreeRemove(ctx, row.OpenWorkspaceID)
			} else {
				err = c.deps.Git.RemoveWorktree(ctx, c.main, row.Path)
			}
		}
		if err != nil {
			if firstErr == nil {
				firstErr, firstFailed = err, row.Label()
			}
			continue
		}
		count++
	}
	noun := "worktrees"
	if count == 1 {
		noun = "worktree"
	}
	message := "removed " + strconv.Itoa(count) + " " + noun
	if firstErr != nil {
		failed := len(rows) - count
		message += ", " + strconv.Itoa(failed) + " failed: " + firstFailed + ": " + firstErr.Error()
	}
	return removed{message: message}
}

func removalCheck(want Row, current gathered) error {
	if current.err != nil {
		return current.err
	}
	for _, row := range current.rows {
		if canonical(row.Path) != canonical(want.Path) {
			continue
		}
		if row.Branch != want.Branch || row.Head == "" || row.Head != want.Head || row.OpenWorkspaceID != want.OpenWorkspaceID || row.IsMain != want.IsMain {
			return errors.New("worktree changed; refresh and review it again")
		}
		if row.Verdict != judge.Safe {
			return fmt.Errorf("worktree is %s (%s); only safe worktrees can be removed", row.Verdict, row.Detail)
		}
		return nil
	}
	return errors.New("worktree is no longer listed")
}

// loop는 화면의 본체다. 키와 배경 결과를 하나의 select 로 받아 모델에 옮기고 그린다.
//
// 흐름은 docs/PLAN.md 의 것이다. 로컬 자료로 먼저 그리고, 그 뒤 fetch 를 시작하고, fetch 가 끝나면 다시 모아
// 그린다. 삭제 뒤에는 fetch 없이 다시 모은다. r 은 fetch 부터 다시 한다.
//
// 배경 일은 종류마다 하나씩만 돈다. 모으는 중에 또 모으라는 요청이 오면(fetch 가 끝났는데 삭제 뒤의 모으기가
// 아직 도는 중) 표시만 해 두고 끝난 뒤 한 번 더 한다. fetch 중의 r 은 무시한다. 삭제 중의 d/D/Enter 는 거절한다.
// 지우는 동안 표는 낡은 것이라 그 위에서 또 지우면 엉뚱한 것을 지운다.
//
// 삭제 중의 닫기는 삭제 완료 뒤 처리한다. 반복 키, 입력 EOF, 부모 컨텍스트 취소도 진행 중인 삭제를
// 끊지 않는다. 삭제 명령 자체의 제한 시간은 gitrepo/herdrcli 가 지킨다.
func (c *controller) loop(ctx context.Context, keys <-chan tui.Key, out io.Writer, size func() (cols, rows int), spawn func(func())) error {
	c.deps.Spawn = spawn
	// 배경 일은 결과를 하나씩만 낸다. 버퍼가 하나면 loop 가 먼저 끝나도 그 고루틴이 채널에 매달리지 않는다.
	gatherCh := make(chan gathered, 1)
	fetchCh := make(chan fetched, 1)
	removeCh := make(chan removed, 1)
	var gathering, gatherAgain, fetching, removing, quitAsked bool
	defer func() {
		if removing {
			<-removeCh
		}
	}()

	startGather := func() {
		if gathering {
			gatherAgain = true
			return
		}
		gathering = true
		heads := c.heads
		spawn(func() { gatherCh <- c.gather(ctx, heads) })
	}
	startFetch := func() {
		if fetching || c.deps.Remote == "" {
			return
		}
		fetching = true
		c.m.Fetch = FetchFetching
		lookupRemotes := c.lookupRemotes
		spawn(func() { fetchCh <- c.fetch(ctx, lookupRemotes) })
	}
	draw := func() {
		if c.m.Fetch == FetchDone {
			c.m.FetchedAgo = Ago(time.Since(c.fetchedAt))
		}
		cols, rows := size()
		tui.Frame(out, Render(c.m, cols, rows), cols, rows)
		if c.afterDraw != nil {
			c.afterDraw(c.m)
		}
	}

	// 첫 모으기가 끝나기 전에도 화면은 떠 있어야 한다. 그동안 마지막 줄이 무엇을 하는 중인지 말한다.
	c.m.Message = "collecting…"
	draw()
	startGather()
	firstGather := true

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case got := <-gatherCh:
			gathering = false
			if firstGather {
				c.m.Message = ""
			}
			if got.err != nil {
				c.m.Message = got.err.Error()
			} else {
				c.m.Rows = got.rows
				c.m.HerdrUnavailable = got.herdrUnavailable
				c.lookupRemotes = got.lookupRemotes
				c.m = clampCursor(c.m)
			}
			if firstGather {
				firstGather = false
				// 로컬 자료가 손에 들어온 뒤에야 fetch 를 시작한다. 첫 표가 fetch 를 기다리지 않아야 한다.
				startFetch()
			}
			if gatherAgain {
				gatherAgain = false
				startGather()
			}
			draw()

		case got := <-fetchCh:
			fetching = false
			c.fetchedAt = got.at
			if got.fetchErr != nil {
				c.m.Fetch = FetchFailed
			} else {
				c.m.Fetch = FetchDone
			}
			c.heads = got.heads
			startGather()
			draw()

		case got := <-removeCh:
			removing = false
			if quitAsked {
				return nil
			}
			c.m.Message = got.message
			startGather()
			draw()

		case k, ok := <-keys:
			if !ok {
				// 입력이 끝났다. 키를 받을 길이 없는 화면은 닫는 수밖에 없다.
				return nil
			}
			var action Action
			confirmed := c.m.Confirm
			c.m, action = Update(c.m, k)
			switch action {
			case ActionQuit:
				if !removing {
					return nil
				}
				quitAsked = true
				c.m.Message = "removal in progress; the screen closes when it finishes"
			case ActionOpen:
				if removing {
					c.m.Message = "removal in progress"
					break
				}
				if err := c.open(ctx); err != nil {
					c.m.Message = err.Error()
					break
				}
				return nil
			case ActionRemoveSelected:
				if removing {
					c.m.Message = "removal in progress"
					break
				}
				row, ok := c.m.Selected()
				if !ok || row.Verdict != judge.Safe {
					break
				}
				removing = true
				c.m.Message = "removing " + row.Label() + "…"
				heads := c.heads
				spawn(func() { removeCh <- c.remove(context.WithoutCancel(ctx), []Row{row}, heads) })
			case ActionConfirmYes:
				if removing {
					c.m.Message = "removal in progress"
					break
				}
				if confirmed == nil {
					break
				}
				rows := confirmed.Rows
				if len(rows) == 0 {
					break
				}
				removing = true
				c.m.Message = "removing " + strconv.Itoa(len(rows)) + " worktrees…"
				heads := c.heads
				spawn(func() { removeCh <- c.remove(context.WithoutCancel(ctx), rows, heads) })
			case ActionRefresh:
				startFetch()
			case ActionRemoveAllSafe, ActionConfirmNo, ActionNone:
				// 물음은 모델에 실려 있다. 그리기만 하면 된다.
			}
			draw()
		}
	}
}

// open은 선택한 worktree 로 간다. herdr 에 열려 있으면 그 워크스페이스로, 아니면 herdr 에게 열게 한다.
// 본 체크아웃은 source_workspace_id 가 OpenWorkspaceID 에 들어 있어 같은 길이다.
func (c *controller) open(ctx context.Context) error {
	row, ok := c.m.Selected()
	if !ok {
		return errors.New("no worktree selected")
	}
	if c.m.HerdrUnavailable {
		return errors.New("herdr unavailable; cannot open a workspace")
	}
	if row.OpenWorkspaceID != "" {
		return c.herdr.WorkspaceFocus(ctx, row.OpenWorkspaceID)
	}
	return c.herdr.WorktreeOpen(ctx, row.Path)
}
