package main

import (
	"context"
	"testing"

	"herdr-git-upstream/internal/worktreeui"
)

func TestRunScreenWorkspaceContext(t *testing.T) {
	for _, command := range []string{"worktrees", "new-worktree"} {
		for _, tc := range []struct {
			name, workspace, pluginContext, wantWorkspace string
			args                                          []string
			wantCode                                      int
		}{
			{name: "popup preserves invoking workspace", pluginContext: `{"invocation_source":"plugin-pane","workspace_id":"wMfe"}`, wantWorkspace: "wMfe"},
			{name: "normal pane identity", workspace: "wPane", wantWorkspace: "wPane"},
			{name: "popup context wins over inherited identity", workspace: "wOld", pluginContext: `{"workspace_id":"wMfe"}`, wantWorkspace: "wMfe"},
			{name: "explicit cwd wins", pluginContext: `{"workspace_id":"wMfe"}`, args: []string{"--cwd", t.TempDir()}},
			{name: "ordinary terminal", wantWorkspace: ""},
			{name: "malformed popup context fails closed", pluginContext: `{`, wantCode: 1},
			{name: "popup without workspace fails closed", pluginContext: `{"invocation_source":"plugin-pane"}`, wantCode: 1},
			{name: "null popup context fails closed", pluginContext: `null`, wantCode: 1},
			{name: "explicit cwd ignores broken context", pluginContext: `{`, args: []string{"--cwd", t.TempDir()}},
		} {
			t.Run(command+"/"+tc.name, func(t *testing.T) {
				t.Setenv("HERDR_WORKSPACE_ID", tc.workspace)
				t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", tc.pluginContext)
				called := false
				code := runScreen(tc.args, command, func(_ context.Context, opts worktreeui.Options) error {
					called = true
					if opts.WorkspaceID != tc.wantWorkspace {
						t.Errorf("screen workspace = %q; want invoking workspace %q (plugin cwd must not select its own repository)", opts.WorkspaceID, tc.wantWorkspace)
					}
					if len(tc.args) > 0 && opts.CWD != tc.args[1] {
						t.Errorf("explicit cwd = %q; want %q", opts.CWD, tc.args[1])
					}
					if tc.pluginContext != "" && len(tc.args) == 0 && opts.CWD != "" {
						t.Errorf("popup must not use process cwd %q as a fallback repository", opts.CWD)
					}
					return nil
				})
				if code != tc.wantCode || called != (tc.wantCode == 0) {
					t.Fatalf("code=%d called=%v; want code=%d", code, called, tc.wantCode)
				}
			})
		}
	}
}
