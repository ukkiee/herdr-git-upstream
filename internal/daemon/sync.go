package daemon

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"herdr-git-upstream/internal/config"
	"herdr-git-upstream/internal/gitrepo"
	"herdr-git-upstream/internal/herdrcli"
	"herdr-git-upstream/internal/judge"
	"herdr-git-upstream/internal/state"
)

// Source는 herdr에 토큰을 보고할 때 밝히는 출처다. herdr는 출처마다 토큰을 따로 보관하므로,
// 이 이름이 다른 플러그인의 토큰과 섞이지 않게 해 준다.
const Source = "git-upstream"

// maxParallelFetch는 동시에 진행할 fetch 수다. 저장소가 많아도 네트워크와 자격 증명 도우미에
// 한꺼번에 부담을 주지 않으면서, 느린 원격 하나가 전체를 붙잡지 않을 만큼은 겹쳐 돌린다.
const maxParallelFetch = 4

// herdr가 받아들이는 토큰 수명의 상한이다. 이보다 큰 값을 보내면 요청이 거부된다.
const maxTokenTTL = 24 * time.Hour

// Syncer는 한 번의 갱신에 필요한 것들을 모아 둔다.
type Syncer struct {
	Config config.Resolved
	Herdr  *herdrcli.Client
	Git    gitrepo.Runner
	Store  state.Store
	Log    *slog.Logger
}

// NewSyncer는 설정에 맞춘 Syncer를 만든다.
func NewSyncer(cfg config.Resolved, log *slog.Logger) *Syncer {
	return &Syncer{
		Config: cfg,
		Herdr:  herdrcli.New(),
		Git:    gitrepo.Runner{Timeout: cfg.FetchTimeout},
		Store:  state.New(),
		Log:    log,
	}
}

// target은 워크스페이스 하나와 그것이 가리키는 저장소다.
type target struct {
	WorkspaceID string
	Dir         string
	Repo        gitrepo.Repo
	Upstream    gitrepo.Upstream
	// HasRepo는 Dir가 git 저장소 안이라는 뜻이다. upstream이 없어도 merged는 판정할 수 있으므로
	// Resolved와 따로 둔다. 아직 push 하지 않은 새 브랜치도 dev의 조상이면 merged다.
	HasRepo bool
	// Head는 체크아웃된 커밋이다. 커밋이 하나도 없는 저장소에서는 비어 있고, 그때는 판정할 것이 없다.
	Head string
	// Branch는 체크아웃된 브랜치 이름이다. HEAD가 분리되어 있으면 비어 있다. upstream이 없어도
	// 필요하다. merged 판정이 자기 사본(refs/remotes/<원격>/<브랜치>)을 근거에서 빼려면 알아야 한다.
	Branch string
	// Remote는 판정에 쓰는 원격이다. upstream의 원격이거나, 없으면 judge.RemoteFor가 고른 것이다.
	Remote string
	// Integration은 통합 브랜치들이다. merged 판정의 상대이자 함께 가져와야 할 참조다.
	// 통합 브랜치의 추적 참조가 낡으면 판정도 낡는다.
	Integration []gitrepo.Upstream
	// DefaultKnown이 거짓이면 원격 기본 브랜치를 알지 못해 Integration에 없다. 그때는 merged를 판정하지
	// 않는다. 통합 브랜치 자체를 체크아웃한 자리를 가려낼 수 없어 거짓 양성이 나기 때문이다.
	DefaultKnown bool
	// LookupDefaultBranch가 참이면 원격 기본 브랜치를 로컬에서도 기록에서도 알 수 없어 원격에 물어야
	// 한다. 네트워크를 타므로 fetch와 같은 자리에서 하고, 알아낸 답은 fetch 뒤 refreshIntegration이
	// 같은 회차에 다시 읽는다.
	LookupDefaultBranch bool
	// Resolved가 false면 저장소가 아니거나 비교할 upstream이 없다는 뜻이다.
	// 이 경우에도 토큰은 비워서 보고해야, 예전에 올려 둔 숫자가 사이드바에 남지 않는다.
	Resolved bool
}

// Sweep은 워크스페이스들을 갱신한다.
//
// onlyWorkspace가 비어 있지 않으면 그 워크스페이스만 다룬다. force가 참이면 스로틀을 무시한다.
// 사람이 직접 "지금 갱신"을 눌렀을 때 기다리지 않게 하기 위한 것이다.
func (s *Syncer) Sweep(ctx context.Context, onlyWorkspace string, force bool) error {
	if !s.Config.Enabled {
		return s.clearAll(ctx)
	}

	panes, err := s.Herdr.PaneList(ctx)
	if err != nil {
		return err
	}
	// worktree 로 만든 워크스페이스는 herdr 가 체크아웃 경로를 따로 기억해 둔다. 실패해도 페인만으로
	// 진행할 수 있으므로 오류로 만들지 않는다.
	anchors := map[string]string{}
	if workspaces, err := s.Herdr.WorkspaceList(ctx); err == nil {
		for _, ws := range workspaces {
			if ws.Worktree != nil && ws.Worktree.CheckoutPath != "" {
				anchors[ws.WorkspaceID] = ws.Worktree.CheckoutPath
			}
		}
	} else {
		s.Log.Debug("워크스페이스 목록을 읽지 못했다", "error", err)
	}
	targets := s.resolveTargets(ctx, panes, anchors, onlyWorkspace)
	if len(targets) == 0 {
		return nil
	}
	s.fetchAll(ctx, targets, force)
	s.refreshIntegration(ctx, targets)
	s.reportAll(ctx, targets)
	return nil
}

// resolveTargets는 워크스페이스마다 어느 저장소를 볼지 정한다.
//
// worktree 로 만든 워크스페이스는 herdr 가 기억해 둔 체크아웃 경로를 쓴다. 그 값은 페인이 어떻게
// 바뀌어도 흔들리지 않고, 무엇보다 이 플러그인이 존재하는 이유인 들여쓴 worktree 행을 정확히 짚는다.
//
// 그 값이 없는 워크스페이스는 첫 페인의 셸 작업 디렉터리를 쓴다. herdr 도 워크스페이스의 정체를
// 첫 탭의 뿌리 페인에서 얻으므로 보통은 같은 자리를 가리킨다. 다만 사용자가 페인을 맞바꾸면
// (herdr 는 그때 뿌리 페인을 새로 지정하지 않는다) 목록의 첫 페인이 뿌리 페인과 어긋날 수 있다.
// 드문 경우라 여기서는 첫 페인을 그대로 쓰되, 그런 한계가 있다는 것은 적어 둔다.
func (s *Syncer) resolveTargets(ctx context.Context, panes []herdrcli.Pane, anchors map[string]string, onlyWorkspace string) []target {
	seen := make(map[string]bool, len(panes))
	var targets []target
	for _, pane := range panes {
		if pane.WorkspaceID == "" || seen[pane.WorkspaceID] {
			continue
		}
		if onlyWorkspace != "" && pane.WorkspaceID != onlyWorkspace {
			continue
		}
		seen[pane.WorkspaceID] = true

		dir := pane.Dir()
		if anchor := anchors[pane.WorkspaceID]; anchor != "" {
			dir = anchor
		}
		targets = append(targets, s.resolveTarget(ctx, target{WorkspaceID: pane.WorkspaceID, Dir: dir}))
	}
	return targets
}

// resolveTarget은 워크스페이스 하나의 저장소, HEAD, upstream, 원격, 통합 브랜치를 차례로 채운다.
// 앞 단계가 안 되면 거기서 멈추되, 채운 것까지는 남긴다. upstream이 없어도 저장소이면 merged는
// 판정할 수 있으므로 HasRepo와 Resolved를 따로 둔다.
func (s *Syncer) resolveTarget(ctx context.Context, item target) target {
	repo, err := s.Git.Discover(ctx, item.Dir)
	if err != nil {
		return item
	}
	item.Repo = repo
	item.HasRepo = true
	if head, err := s.Git.HeadCommit(ctx, repo); err == nil {
		item.Head = head
	}
	if branch, err := s.Git.CurrentBranch(ctx, repo); err == nil {
		item.Branch = branch
	}

	var upstream *gitrepo.Upstream
	if item.Branch != "" {
		if up, err := s.Git.UpstreamFor(ctx, repo, item.Branch); err == nil {
			item.Upstream = up
			item.Resolved = true
			upstream = &up
		}
	}
	// upstream이 없는 것은 HEAD가 분리되었거나 아직 push 하지 않은 경우로, 사용자가 손볼 일이지
	// 오류로 떠들 일은 아니다. 그래도 어느 원격을 볼지 정할 수 있으면 merged는 판정한다.
	remote, ok := judge.RemoteFor(ctx, s.Git, repo, upstream)
	if !ok {
		return item
	}
	item.Remote = remote
	return s.fillIntegration(ctx, item)
}

// fillIntegration은 통합 브랜치 목록과 원격 기본 브랜치를 아는지를 채운다. resolveTarget이 한 번,
// 원격에 기본 브랜치를 물은 뒤 refreshIntegration이 한 번 더 부른다.
func (s *Syncer) fillIntegration(ctx context.Context, item target) target {
	integration, err := judge.IntegrationBranches(ctx, s.Git, item.Repo, item.Remote, s.Store, time.Now())
	if err != nil {
		// 풀지 못한 mergeTarget이 있어도 나머지로 진행한다. 판정이 빠질 뿐 틀리지는 않는다.
		s.Log.Debug("통합 브랜치를 모두 알아내지 못했다", "repo", item.Repo.Root, "error", err)
	}
	item.Integration = integration.Branches
	item.DefaultKnown = integration.DefaultKnown
	item.LookupDefaultBranch = integration.LookupDue
	return item
}

// refreshIntegration은 원격에 기본 브랜치를 물은 워크스페이스의 통합 브랜치 목록을 다시 읽는다.
//
// resolveTargets가 목록을 채운 뒤에야 fetchAll이 원격에 묻고 기록에 남기므로, 그대로 두면 그 답은
// 다음 회차에나 쓰인다. 그 한 회차 동안 통합 브랜치를 모르는 채 판정하면 통합 브랜치 자체를 체크아웃한
// 자리에 merged가 붙고, 원격이 닿지 않아 물음이 실패하면 그 상태가 하루 동안 이어진다. 다시 읽는 것은
// 로컬 읽기라 값이 싸고, 물은 워크스페이스에서만 한다.
func (s *Syncer) refreshIntegration(ctx context.Context, targets []target) {
	for i, item := range targets {
		if item.LookupDefaultBranch {
			targets[i] = s.fillIntegration(ctx, item)
		}
	}
}

// fetchJob은 한 번의 fetch다. 현재 브랜치의 upstream이거나 통합 브랜치다.
type fetchJob struct {
	Repo     gitrepo.Repo
	Upstream gitrepo.Upstream
}

// lookupJob은 원격 기본 브랜치를 원격에 묻는 한 번의 왕복이다. 저장소와 원격 하나에 한 번이다.
type lookupJob struct {
	Repo   gitrepo.Repo
	Remote string
}

// fetchAll은 갱신이 필요한 참조들을 가져온다.
//
// 같은 참조를 두 번 가져오지 않도록 묶는다. 연결된 worktree들은 참조 저장소를 공유하므로,
// 같은 브랜치를 보고 있는 두 워크스페이스는 한 번의 fetch로 함께 최신이 된다.
//
// 통합 브랜치도 저마다 별도의 작업으로 넣는다. 통합 브랜치의 추적 참조가 낡으면 merged 판정도
// 낡기 때문이다. 현재 브랜치가 곧 통합 브랜치면 FetchKey가 같아 한 번만 가져간다.
//
// 원격 기본 브랜치를 원격에 묻는 일(ls-remote)도 여기서 한다. 네트워크를 타는 일은 모두 이 자리의
// 병렬 상한 안에서 돌아야, 닿지 않는 원격 하나가 회차 전체의 토큰 보고를 막지 않는다. 하루에 한 번이라
// 스로틀은 따로 두지 않고, force도 그 간격을 앞당기지 않는다. "지금 갱신"은 참조를 가져오는 일이다.
func (s *Syncer) fetchAll(ctx context.Context, targets []target, force bool) {
	fetches := make(map[string]fetchJob)
	lookups := make(map[string]lookupJob)
	add := func(repo gitrepo.Repo, up gitrepo.Upstream) {
		// 원격 쪽 이름을 모르는 참조는 가져올 방법이 없다. 판정에는 그대로 쓴다.
		if up.RemoteRef == "" || up.TrackingRef == "" {
			return
		}
		key := up.FetchKey(repo.CommonDir)
		if _, exists := fetches[key]; !exists {
			fetches[key] = fetchJob{Repo: repo, Upstream: up}
		}
	}
	for _, item := range targets {
		if !item.HasRepo {
			continue
		}
		if item.Resolved {
			add(item.Repo, item.Upstream)
		}
		for _, up := range item.Integration {
			add(item.Repo, up)
		}
		if item.LookupDefaultBranch && item.Remote != "" {
			lookups[item.Repo.CommonDir+"\x00"+item.Remote] = lookupJob{Repo: item.Repo, Remote: item.Remote}
		}
	}

	// 순서를 정해 두면 로그가 읽기 쉽고, 문제가 났을 때 같은 순서로 재현된다.
	var runs []func(context.Context)
	for _, key := range sortedKeys(fetches) {
		item := fetches[key]
		stateKey := state.Key(key)
		record := s.Store.LoadRecord(stateKey)
		if !force && !record.DueAt(time.Now(), s.Config.Throttle) {
			continue
		}
		runs = append(runs, func(ctx context.Context) { s.fetchOne(ctx, item, stateKey, record) })
	}
	for _, key := range sortedKeys(lookups) {
		item := lookups[key]
		runs = append(runs, func(ctx context.Context) { s.lookupOne(ctx, item) })
	}
	if len(runs) == 0 {
		return
	}

	limit := make(chan struct{}, maxParallelFetch)
	var wait sync.WaitGroup
	for _, run := range runs {
		wait.Add(1)
		limit <- struct{}{}
		go func(run func(context.Context)) {
			defer wait.Done()
			defer func() { <-limit }()
			run(ctx)
		}(run)
	}
	wait.Wait()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// lookupOne은 원격 기본 브랜치를 원격에 물어 저장소 기록에 남긴다. 답은 refreshIntegration이 기록에서 읽는다.
func (s *Syncer) lookupOne(ctx context.Context, item lookupJob) {
	if err := judge.LookupDefaultBranch(ctx, s.Git, item.Repo, item.Remote, s.Store, time.Now()); err != nil {
		s.Log.Debug("원격 기본 브랜치를 알아내지 못했다", "repo", item.Repo.Root, "remote", item.Remote, "error", err)
		return
	}
	s.Log.Debug("원격 기본 브랜치를 알아냈다", "repo", item.Repo.Root, "remote", item.Remote)
}

func (s *Syncer) fetchOne(ctx context.Context, item fetchJob, stateKey string, record state.Record) {
	// 시도한 시각을 먼저 남긴다. 데몬과 다른 호출이 동시에 같은 저장소를 노릴 때,
	// 둘 다 fetch로 들어가는 창을 좁히기 위해서다.
	record.LastAttemptUnix = time.Now().Unix()
	if err := s.Store.SaveRecord(stateKey, record); err != nil {
		s.Log.Debug("fetch 기록을 남기지 못했다", "repo", item.Repo.Root, "error", err)
	}

	err := s.Git.Fetch(ctx, item.Repo, item.Upstream)
	if err != nil {
		// 원격에서 브랜치가 사라진 경우는 다시 시도해도 같은 결과다. 사용자가 손쓸 여지가 없는 일을
		// 사이드바에 계속 띄우면 소음이 되므로, 영구 실패로 표시해 경고 대상에서 뺀다.
		record = record.MarkFailure(time.Now(), err.Error(), gitrepo.IsMissingRemoteRef(err))
		s.Log.Debug("fetch 실패", "repo", item.Repo.Root, "error", err)
	} else {
		record = record.MarkSuccess(time.Now())
		s.Log.Debug("fetch 성공", "repo", item.Repo.Root, "ref", item.Upstream.TrackingRef)
	}
	if err := s.Store.SaveRecord(stateKey, record); err != nil {
		s.Log.Debug("fetch 기록을 남기지 못했다", "repo", item.Repo.Root, "error", err)
	}
}

// reportAll은 워크스페이스마다 사이드바 토큰을 보고한다.
func (s *Syncer) reportAll(ctx context.Context, targets []target) {
	now := time.Now()
	ttl := s.tokenTTL()
	for _, item := range targets {
		tokens := s.tokensFor(ctx, item, now)
		if err := s.Herdr.ReportMetadata(ctx, item.WorkspaceID, Source, tokens, ttl); err != nil {
			// 기본 로그 수준에서도 보이게 한다. 보고가 거절되면 사이드바에 아무것도 뜨지 않는데,
			// 그 이유가 어디에도 남지 않으면 사용자는 플러그인이 그냥 안 되는 줄로 안다.
			s.Log.Warn("토큰 보고 실패", "workspace", item.WorkspaceID, "error", err)
		}
	}
}

// tokensFor는 한 워크스페이스에 보고할 토큰을 만든다.
// 보여 줄 것이 없는 자리는 빈 문자열로 채운다. herdr는 빈 값을 "이 키를 지워라"로 읽으므로,
// 상황이 정리되었을 때 낡은 숫자가 사이드바에 남지 않는다.
//
// 판정 하나가 실패해도 다른 토큰은 보고한다. 이름이 빈 토큰은 넣지 않는다. 여섯 개라 herdr가
// 한 요청에 받는 상한(16개) 안이다.
func (s *Syncer) tokensFor(ctx context.Context, item target, now time.Time) map[string]string {
	tokens := map[string]string{}
	behind, ahead, stale, gone, merged, catchup := "", "", "", "", "", ""

	var upstream *gitrepo.Upstream
	if item.Resolved {
		upstream = &item.Upstream
		record := s.Store.LoadRecord(state.Key(item.Upstream.FetchKey(item.Repo.CommonDir)))
		if record.Stale(now, s.Config.StaleAfter) {
			stale = s.Config.StaleLabel
		}
		// gone은 fetch 기록만 본다. 원격에 다시 묻지 않으므로 값이 0이다. 커밋이 하나도 없는 저장소는
		// 빼는데, 빈 원격을 clone한 직후에도 git이 branch.main을 잡아 두어 같은 실패가 나지만 태어나지
		// 않은 브랜치는 사라질 수 없기 때문이다.
		if item.Head != "" && judge.Gone(record) {
			gone = s.Config.GoneLabel
		}
	}
	// 뒤처짐과 앞섬은 회차 첫머리에 읽어 둔 HEAD로 센다. 그래야 같은 회차의 merged, catchup과 같은
	// 커밋을 말한다. 커밋이 하나도 없는 저장소는 셀 것이 없다.
	if item.Resolved && item.Head != "" {
		counts, err := s.Git.CountsBetween(ctx, item.Repo, item.Head, item.Upstream.TrackingRef)
		if err != nil {
			// 한 번도 가져온 적 없는 브랜치는 셀 수 없다. 숫자를 지어내지 않고 비워 둔다.
			s.Log.Debug("앞뒤 커밋 수를 세지 못했다", "repo", item.Repo.Root, "error", err)
		} else {
			if counts.Behind > 0 {
				behind = s.Config.BehindPrefix + strconv.Itoa(counts.Behind)
			}
			if counts.Ahead > 0 {
				ahead = s.Config.AheadPrefix + strconv.Itoa(counts.Ahead)
			}
			// 뒤처졌을 때만 따라잡기를 본다. 뒤처지지 않았으면 따라잡을 것이 없다.
			if counts.Behind > 0 && s.catchupConflicts(ctx, item, now) {
				catchup = s.Config.CatchupConflictLabel
			}
		}
	}

	// merged는 upstream이 없어도 판정한다. 커밋이 하나도 없는 저장소는 판정할 것이 없고, 원격 기본
	// 브랜치를 끝내 알아내지 못한 저장소도 판정하지 않는다. 통합 브랜치 자체를 체크아웃한 자리를
	// 가려낼 수 없어, 거기서 갈라져 나간 원격 브랜치 하나만 있어도 조상 검사가 merged를 붙이기 때문이다.
	if item.HasRepo && item.Head != "" && item.DefaultKnown {
		integrationRefs := make([]string, 0, len(item.Integration))
		for _, up := range item.Integration {
			integrationRefs = append(integrationRefs, up.TrackingRef)
		}
		self := judge.SelfRefs(ctx, s.Git, item.Repo, item.Branch, upstream)
		verdict, err := judge.JudgeMerged(ctx, s.Git, item.Repo, item.Head, self, integrationRefs)
		if err != nil {
			s.Log.Debug("merged 를 판정하지 못했다", "repo", item.Repo.Root, "error", err)
		}
		if verdict.Yes {
			merged = s.Config.MergedLabel
			s.Log.Debug("merged", "repo", item.Repo.Root, "by", verdict.By)
		}
	}

	for name, value := range map[string]string{
		s.Config.BehindToken:  behind,
		s.Config.AheadToken:   ahead,
		s.Config.StaleToken:   stale,
		s.Config.GoneToken:    gone,
		s.Config.MergedToken:  merged,
		s.Config.CatchupToken: catchup,
	} {
		if name != "" {
			tokens[name] = value
		}
	}
	return tokens
}

// catchupKey는 따라잡기 기록의 열쇠다. 공식은 state.CatchupKey 에 있다. worktree 화면이 같은 캐시를 나눠 쓴다.
func catchupKey(item target) string {
	return state.CatchupKey(item.Upstream.FetchKey(item.Repo.CommonDir), item.Upstream.Branch)
}

// catchupConflicts는 따라잡을 때 충돌하는지 판정하고, 판정이 새로 계산되었으면 기록에 남긴다.
// 기록을 남겨야 다음 회차가 같은 (HEAD, 추적 참조 커밋) 쌍에서 merge-tree를 다시 돌리지 않는다.
//
// 따라잡기 기록은 fetch 기록과 다른 파일이다. fetch 기록을 여기서 되쓰면, 같은 저장소를 도는 다른
// herdr 세션의 데몬이 그 사이에 남긴 fetch 결과를 회차 첫머리에 읽어 둔 낡은 값으로 덮는다.
func (s *Syncer) catchupConflicts(ctx context.Context, item target, now time.Time) bool {
	trackingCommit, err := s.Git.CommitOf(ctx, item.Repo, item.Upstream.TrackingRef)
	if err != nil {
		return false
	}
	key := catchupKey(item)
	record := s.Store.LoadCatchupRecord(key)
	result, updated, err := judge.JudgeCatchup(ctx, s.Git, item.Repo, item.Head, trackingCommit, record, now)
	if err != nil {
		s.Log.Debug("catchup 을 판정하지 못했다", "repo", item.Repo.Root, "error", err)
	}
	if updated != record {
		if err := s.Store.SaveCatchupRecord(key, updated); err != nil {
			s.Log.Debug("따라잡기 기록을 남기지 못했다", "repo", item.Repo.Root, "error", err)
		}
	}
	return result == judge.CatchupConflict
}

// tokenTTL은 토큰의 수명을 정한다.
//
// 주기의 세 배로 두어, 한 번쯤 갱신을 건너뛰어도 값이 깜빡이지 않으면서, 데몬이 죽으면 곧 사라지게
// 한다. 사이드바에 낡은 숫자가 붙박이로 남는 것이 아무것도 없는 것보다 나쁘기 때문이다.
func (s *Syncer) tokenTTL() time.Duration {
	ttl := s.Config.Interval * 3
	if ttl < 3*time.Minute {
		ttl = 3 * time.Minute
	}
	if ttl > maxTokenTTL {
		ttl = maxTokenTTL
	}
	return ttl
}

// clearAll은 이 플러그인이 올린 토큰을 모두 지운다. 설정에서 꺼졌을 때 쓴다.
func (s *Syncer) clearAll(ctx context.Context) error {
	panes, err := s.Herdr.PaneList(ctx)
	if err != nil {
		return err
	}
	// 보고할 수 있는 토큰 전부를 지운다. 이름이 빈 것과 규칙에 어긋난 것은 TokenNames가 이미 뺐다.
	empty := map[string]string{}
	for _, name := range s.Config.TokenNames() {
		empty[name] = ""
	}
	if len(empty) == 0 {
		return nil
	}
	seen := map[string]bool{}
	for _, pane := range panes {
		if pane.WorkspaceID == "" || seen[pane.WorkspaceID] {
			continue
		}
		seen[pane.WorkspaceID] = true
		if err := s.Herdr.ReportMetadata(ctx, pane.WorkspaceID, Source, empty, 0); err != nil {
			s.Log.Debug("토큰을 지우지 못했다", "workspace", pane.WorkspaceID, "error", err)
		}
	}
	return nil
}
