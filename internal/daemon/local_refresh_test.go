package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"herdr-git-upstream/internal/config"
	"herdr-git-upstream/internal/gitrepo"
	"herdr-git-upstream/internal/herdrcli"
	"herdr-git-upstream/internal/state"
)

const daemonTestDir = "HERDR_GIT_UPSTREAM_DAEMON_TEST_DIR"

// The test executable replaces only the external herdr CLI. Git and the daemon loop are real.
func TestMain(m *testing.M) {
	if dir := os.Getenv(daemonTestDir); dir != "" && len(os.Args) > 2 && !strings.HasPrefix(os.Args[1], "-") {
		args := os.Args[1:]
		switch strings.Join(args[:2], " ") {
		case "pane list":
			if _, err := os.Stat(filepath.Join(dir, "block-list")); err == nil {
				_ = os.WriteFile(filepath.Join(dir, "list-started"), nil, 0600)
				for {
					if _, err := os.Stat(filepath.Join(dir, "release-list")); err == nil {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			raw, _ := os.ReadFile(filepath.Join(dir, "panes.json"))
			fmt.Print(string(raw))
		case "workspace list":
			fmt.Print(`{"result":{"workspaces":[]}}`)
		case "workspace report-metadata":
			if _, err := os.Stat(filepath.Join(dir, "fail-report")); err == nil {
				fmt.Fprint(os.Stderr, `{"error":{"code":"unavailable","message":"retry later"}}`)
				os.Exit(1)
			}
			tokens := map[string]string{}
			for i := 0; i+1 < len(args); i++ {
				if args[i] == "--token" {
					name, value, _ := strings.Cut(args[i+1], "=")
					tokens[name] = value
				}
			}
			raw, _ := json.Marshal(tokens)
			_ = os.WriteFile(filepath.Join(dir, "report.json"), raw, 0600)
			fmt.Print(`{"result":{"type":"ok"}}`)
		default:
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestDaemonReportsPullBeforeNextFetchInterval(t *testing.T) {
	testDaemonPull(t, false)
}

func TestDaemonReportsPullWhileRegularSweepIsBlocked(t *testing.T) {
	testDaemonPull(t, true)
}

func testDaemonPull(t *testing.T, blockSweep bool) {
	t.Helper()
	f := newRepoFixture(t)
	run(t, f.seed, "git", "commit", "--quiet", "--allow-empty", "-m", "remote advances")
	run(t, f.seed, "git", "push", "--quiet", "origin", "main")
	dir := t.TempDir()
	configDir := t.TempDir()
	writeFile(t, filepath.Join(configDir, "config.json"), `{"interval_seconds":60,"throttle_seconds":120}`)
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", configDir)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	t.Setenv("HERDR_BIN_PATH", os.Args[0])
	t.Setenv(daemonTestDir, dir)
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	raw, _ := json.Marshal(map[string]any{"result": map[string]any{"panes": []map[string]string{{"workspace_id": "w1", "cwd": f.work}}}})
	writeFile(t, filepath.Join(dir, "panes.json"), string(raw))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	readTokens := func() map[string]string {
		raw, _ := os.ReadFile(filepath.Join(dir, "report.json"))
		var tokens map[string]string
		_ = json.Unmarshal(raw, &tokens)
		return tokens
	}
	waitFor(t, 10*time.Second, func() bool { return readTokens()["behind"] == "↓1" })
	if blockSweep {
		writeFile(t, filepath.Join(dir, "block-list"), "")
		if err := Wake(state.New(), WakeHint{}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 5*time.Second, func() bool {
			_, err := os.Stat(filepath.Join(dir, "list-started"))
			return err == nil
		})
	}
	store := state.New()
	files, _ := filepath.Glob(filepath.Join(store.Root, "fetch", "*.json"))
	beforeFetch := map[string]string{}
	for _, path := range files {
		raw, _ := os.ReadFile(path)
		beforeFetch[path] = string(raw)
	}
	run(t, f.work, "git", "pull", "--quiet", "--ff-only", "origin")
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if tokens := readTokens(); tokens != nil && tokens["behind"] == "" {
			for path, before := range beforeFetch {
				raw, _ := os.ReadFile(path)
				if string(raw) != before {
					t.Fatalf("local update performed another fetch: %s", path)
				}
			}
			if blockSweep {
				writeFile(t, filepath.Join(dir, "release-list"), "")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("git pull completed, but sidebar still reports %q before the 60-second fetch interval", readTokens()["behind"])
}

func TestPeriodicReportDoesNotRestoreHeadFromBeforePull(t *testing.T) {
	s := newTestSyncer(t)
	f := newRepoFixture(t)
	run(t, f.seed, "git", "commit", "--quiet", "--allow-empty", "-m", "advance")
	run(t, f.seed, "git", "push", "--quiet", "origin", "main")
	run(t, f.work, "git", "fetch", "--quiet", "origin")
	oldTarget := s.resolveOne(t, f.work)
	if got := s.tokensFor(context.Background(), oldTarget, time.Now())["behind"]; got != "↓1" {
		t.Fatalf("fixture not behind: %q", got)
	}
	dir := t.TempDir()
	t.Setenv(daemonTestDir, dir)
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	s.Herdr = &herdrcli.Client{Binary: os.Args[0]}
	run(t, f.work, "git", "pull", "--quiet", "--ff-only", "origin")
	s.reportAll(context.Background(), []target{oldTarget})
	raw, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tokens map[string]string
	if err := json.Unmarshal(raw, &tokens); err != nil || tokens["behind"] != "" {
		t.Fatalf("late sweep restored stale HEAD: %s %v", raw, err)
	}
}

func TestLocalReportRetriesWithoutAnotherGitChange(t *testing.T) {
	s := newTestSyncer(t)
	f := newRepoFixture(t)
	dir := t.TempDir()
	t.Setenv(daemonTestDir, dir)
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	s.Herdr = &herdrcli.Client{Binary: os.Args[0]}
	s.reportAll(context.Background(), []target{s.resolveOne(t, f.work)})
	writeFile(t, filepath.Join(dir, "fail-report"), "")
	run(t, f.work, "git", "commit", "--quiet", "--allow-empty", "-m", "local commit")
	s.PollLocal(context.Background())
	if err := os.Remove(filepath.Join(dir, "fail-report")); err != nil {
		t.Fatal(err)
	}
	s.PollLocal(context.Background())
	raw, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tokens map[string]string
	if err := json.Unmarshal(raw, &tokens); err != nil || tokens["ahead"] != "↑1" {
		t.Fatalf("report did not recover without another commit: %s %v", raw, err)
	}
}

func unchangedLocalFixture(tb testing.TB) *Syncer {
	tb.Helper()
	common := tb.TempDir()
	refs := filepath.Join(common, "refs", "remotes", "upstream", "feature")
	if err := os.MkdirAll(refs, 0700); err != nil {
		tb.Fatal(err)
	}
	for i := 0; i < 1200; i++ {
		if err := os.WriteFile(filepath.Join(refs, fmt.Sprintf("branch-%04d", i)), []byte(strings.Repeat("a", 40)+"\n"), 0600); err != nil {
			tb.Fatal(err)
		}
	}
	shared, err := gitrepo.SharedLocalState(common)
	if err != nil {
		tb.Fatal(err)
	}
	s := &Syncer{Config: config.Resolved{Enabled: true}, watched: map[string]*watchedTarget{}}
	for i := 0; i < 27; i++ {
		gitDir := filepath.Join(common, "worktrees", fmt.Sprint(i))
		if err := os.MkdirAll(gitDir, 0700); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/topic\n"), 0600); err != nil {
			tb.Fatal(err)
		}
		checkout, err := gitrepo.CheckoutLocalState(gitDir)
		if err != nil {
			tb.Fatal(err)
		}
		s.watched[fmt.Sprint(i)] = &watchedTarget{paths: gitrepo.LocalPaths{CommonDir: common, GitDir: gitDir}, shared: shared, checkout: checkout}
	}
	return s
}

func TestUnchangedLocalPollNeedsNoGitOrHerdr(t *testing.T) {
	s := unchangedLocalFixture(t)
	// No Git executable or herdr client is available. An unchanged poll needs neither.
	t.Setenv("PATH", t.TempDir())
	for i := 0; i < 3; i++ {
		s.PollLocal(context.Background())
	}
}

func BenchmarkPollLocalUnchanged27Worktrees1200Refs(b *testing.B) {
	s := unchangedLocalFixture(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.PollLocal(context.Background())
	}
}
