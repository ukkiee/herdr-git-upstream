package daemon

import (
	"context"
	"time"

	"herdr-git-upstream/internal/gitrepo"
)

type watchedTarget struct {
	item     target
	paths    gitrepo.LocalPaths
	shared   gitrepo.LocalStamp
	checkout gitrepo.LocalStamp
	// Retry failed reports even if the files have not changed again.
	retry bool
}

// watchLocal keeps local pulls/commits responsive even while a remote fetch is waiting.
// Reporting is serialized with regular sweeps, which reread HEAD before publishing.
func (s *Syncer) watchLocal(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.PollLocal(ctx)
		}
	}
}

// PollLocal checks metadata only. Shared refs are read once per repository, regardless of how many
// open worktrees use them. Only changed repositories/checkouts recalculate and report their tokens.
func (s *Syncer) PollLocal(ctx context.Context) {
	s.localMu.Lock()
	defer s.localMu.Unlock()
	if !s.Config.Enabled {
		return
	}
	shared := map[string]gitrepo.LocalStamp{}
	failed := map[string]bool{}
	for _, watched := range s.watched {
		if ctx.Err() != nil {
			return
		}
		common := watched.paths.CommonDir
		stamp, ok := shared[common]
		if !ok && !failed[common] {
			var err error
			stamp, err = gitrepo.SharedLocalState(common)
			if err != nil {
				failed[common] = true
			} else {
				shared[common] = stamp
			}
		}
		checkout, err := gitrepo.CheckoutLocalState(watched.paths.GitDir)
		if failed[common] || err != nil {
			continue
		}
		if !watched.retry && watched.shared.Equal(stamp) && watched.checkout.Equal(checkout) {
			continue
		}
		// Capture before reading HEAD: a change during calculation must remain visible on the next poll.
		watched.shared, watched.checkout = stamp, checkout
		s.reportLocalLocked(ctx, watched)
	}
}

func (s *Syncer) reportLocalLocked(ctx context.Context, watched *watchedTarget) {
	item := s.resolveTarget(ctx, target{WorkspaceID: watched.item.WorkspaceID, Dir: watched.item.Dir})
	tokens := s.tokensFor(ctx, item, time.Now())
	err := s.Herdr.ReportMetadata(ctx, item.WorkspaceID, Source, tokens, s.tokenTTL())
	watched.retry = err != nil
	if err != nil {
		s.Log.Warn("토큰 보고 실패", "workspace", item.WorkspaceID, "error", err)
	}
	watched.item = item
}

func (s *Syncer) forgetMissingTargets(targets []target, onlyWorkspace string) {
	s.localMu.Lock()
	defer s.localMu.Unlock()
	current := map[string]string{}
	for _, item := range targets {
		current[item.WorkspaceID] = item.Dir
	}
	for id, watched := range s.watched {
		if onlyWorkspace != "" && id != onlyWorkspace {
			continue
		}
		if dir, ok := current[id]; !ok || dir != watched.item.Dir {
			delete(s.watched, id)
		}
	}
}
