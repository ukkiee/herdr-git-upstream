package daemon

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"herdr-pull-status/internal/config"
	"herdr-pull-status/internal/gitrepo"
	"herdr-pull-status/internal/herdrcli"
	"herdr-pull-status/internal/state"
)

// Source는 herdr에 토큰을 보고할 때 밝히는 출처다. herdr는 출처마다 토큰을 따로 보관하므로,
// 이 이름이 다른 플러그인의 토큰과 섞이지 않게 해 준다.
const Source = "pull-status"

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
	targets := s.resolveTargets(ctx, panes, onlyWorkspace)
	if len(targets) == 0 {
		return nil
	}
	s.fetchAll(ctx, targets, force)
	s.reportAll(ctx, targets)
	return nil
}

// resolveTargets는 페인 목록을 워크스페이스별 저장소로 정리한다.
//
// 워크스페이스마다 첫 페인의 디렉터리를 쓴다. herdr 자신도 워크스페이스의 정체를 첫 탭의 뿌리
// 페인으로 정하므로, 같은 기준을 따라야 사이드바에 보이는 브랜치와 이 플러그인이 세는 숫자가
// 서로 다른 저장소를 가리키는 일이 생기지 않는다.
func (s *Syncer) resolveTargets(ctx context.Context, panes []herdrcli.Pane, onlyWorkspace string) []target {
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

		item := target{WorkspaceID: pane.WorkspaceID, Dir: pane.Dir()}
		repo, err := s.Git.Discover(ctx, item.Dir)
		if err != nil {
			targets = append(targets, item)
			continue
		}
		item.Repo = repo
		up, err := s.Git.Upstream(ctx, repo)
		if err != nil {
			// 저장소이긴 하지만 비교할 대상이 없다. HEAD가 분리되었거나 upstream이 없는 경우로,
			// 사용자가 손볼 일이지 오류로 떠들 일은 아니다.
			targets = append(targets, item)
			continue
		}
		item.Upstream = up
		item.Resolved = true
		targets = append(targets, item)
	}
	return targets
}

// fetchAll은 갱신이 필요한 참조들을 가져온다.
//
// 같은 참조를 두 번 가져오지 않도록 묶는다. 연결된 worktree들은 참조 저장소를 공유하므로,
// 같은 브랜치를 보고 있는 두 워크스페이스는 한 번의 fetch로 함께 최신이 된다.
func (s *Syncer) fetchAll(ctx context.Context, targets []target, force bool) {
	jobs := make(map[string]target)
	for _, item := range targets {
		if !item.Resolved {
			continue
		}
		key := item.Upstream.FetchKey(item.Repo.CommonDir)
		if _, exists := jobs[key]; !exists {
			jobs[key] = item
		}
	}
	if len(jobs) == 0 {
		return
	}

	// 순서를 정해 두면 로그가 읽기 쉽고, 문제가 났을 때 같은 순서로 재현된다.
	keys := make([]string, 0, len(jobs))
	for key := range jobs {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	limit := make(chan struct{}, maxParallelFetch)
	var wait sync.WaitGroup
	for _, key := range keys {
		item := jobs[key]
		stateKey := state.Key(key)
		record := s.Store.LoadRecord(stateKey)
		if !force && !record.DueAt(time.Now(), s.Config.Throttle) {
			continue
		}

		wait.Add(1)
		limit <- struct{}{}
		go func(item target, stateKey string, record state.Record) {
			defer wait.Done()
			defer func() { <-limit }()
			s.fetchOne(ctx, item, stateKey, record)
		}(item, stateKey, record)
	}
	wait.Wait()
}

func (s *Syncer) fetchOne(ctx context.Context, item target, stateKey string, record state.Record) {
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
		s.Log.Debug("fetch 성공", "repo", item.Repo.Root, "branch", item.Upstream.Branch)
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
			s.Log.Debug("토큰 보고 실패", "workspace", item.WorkspaceID, "error", err)
		}
	}
}

// tokensFor는 한 워크스페이스에 보고할 토큰을 만든다.
// 보여 줄 것이 없는 자리는 빈 문자열로 채운다. herdr는 빈 값을 "이 키를 지워라"로 읽으므로,
// 상황이 정리되었을 때 낡은 숫자가 사이드바에 남지 않는다.
func (s *Syncer) tokensFor(ctx context.Context, item target, now time.Time) map[string]string {
	tokens := map[string]string{}
	behind, ahead, stale := "", "", ""

	if item.Resolved {
		stateKey := state.Key(item.Upstream.FetchKey(item.Repo.CommonDir))
		if s.Store.LoadRecord(stateKey).Stale(now, s.Config.StaleAfter) {
			stale = s.Config.StaleLabel
		}
		counts, err := s.Git.CountsFor(ctx, item.Repo, item.Upstream)
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
		}
	}

	if s.Config.BehindToken != "" {
		tokens[s.Config.BehindToken] = behind
	}
	if s.Config.AheadToken != "" {
		tokens[s.Config.AheadToken] = ahead
	}
	if s.Config.StaleToken != "" {
		tokens[s.Config.StaleToken] = stale
	}
	return tokens
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
	empty := map[string]string{}
	for _, name := range []string{s.Config.BehindToken, s.Config.AheadToken, s.Config.StaleToken} {
		if name != "" {
			empty[name] = ""
		}
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
