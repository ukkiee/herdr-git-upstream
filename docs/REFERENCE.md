# Reference

[Getting started](../README.md) · **English** | [한국어](REFERENCE.ko.md)

Read the README first for installation and everyday use. This reference covers token meanings,
configuration, screen behavior, and troubleshooting. Commands beginning with `./bin/` run from the
plugin checkout. Run `herdr plugin list --plugin git-upstream --json` and find `plugin_root` for its path.

[Tokens](#tokens) · [Configuration](#configuration) · [Worktree screen](#worktree-screen) ·
[Creation popup](#creation-popup) · [Troubleshooting](#troubleshooting)

## Local development

From the repository root on Linux or macOS:

```sh
sh scripts/build.sh
herdr plugin link "$PWD"
```

`plugin link` does not build the executable. Rebuild after changing Go code. On Windows, run
`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build.ps1`, then link the checkout path.

## Setup helper

From the plugin checkout, run:

```sh
./bin/herdr-git-upstream setup
```

This prints missing sidebar tokens and the worktree-screen binding for your `config.toml`, using any
custom token names from the plugin configuration. It leaves the file unchanged. Merge the output into
existing tables, then run `herdr config check && herdr server reload-config`.

The creation binding printed by `setup` is an optional, commented example that replaces herdr's
built-in creation key. The [README](../README.md#keyboard-shortcuts) instead shows a separate
`prefix+shift+i` binding. Choose one; the plugin does not assign keys automatically. `setup` omits the
creation example when all required sidebar tokens and the worktrees binding are present.

If you only need fetch and do not use worktrees, herdr's built-in `git_status` token may be enough:

```toml
[ui.sidebar.spaces]
rows = [
  ["state_icon", "workspace"],
  ["branch", "git_status"],
]
```

## Tokens

| Token | Value | Meaning |
|---|---|---|
| `$behind` | `↓3` | The remote has three commits you have not received |
| `$ahead` | `↑1` | You have one commit you have not pushed |
| `$gone` | `gone` | The upstream branch has disappeared from the remote, often after a merge |
| `$merged` | `merged` | HEAD's contents are already present in a remote branch |
| `$catchup` | `conflict` | Catching up would conflict. A value appears **only for conflicts**; it is empty when clean or not behind |
| `$sync_stale` | `stale` | Fetch has been failing long enough that the counts may be stale |

Zero or inapplicable values leave the token empty, and herdr removes its space. Nothing is shown outside
a repository or before its first commit. For a detached HEAD or a branch without an upstream, such as a
new branch not yet pushed, only `$merged` is assessed. A newly created branch with no commits of its own
is also `merged` if it is an ancestor of an integration branch.

## Configuration

Place `config.json` in the directory reported by `herdr plugin config-dir git-upstream`. If the file does
not exist, all settings use their defaults.

```json
{
  "interval_seconds": 60,
  "throttle_seconds": 120,
  "fetch_timeout_seconds": 20,
  "stale_after_seconds": 900,
  "behind_token": "behind",
  "ahead_token": "ahead",
  "stale_token": "sync_stale",
  "behind_prefix": "↓",
  "ahead_prefix": "↑",
  "stale_label": "stale",
  "gone_token": "gone",
  "gone_label": "gone",
  "merged_token": "merged",
  "merged_label": "merged",
  "catchup_token": "catchup",
  "catchup_conflict_label": "conflict",
  "fresh_worktrees": true,
  "enabled": true
}
```

| Setting | Default | Description |
|---|---|---|
| `interval_seconds` | 60 | Interval between passes over all workspaces, from 5 seconds to 24 hours |
| `throttle_seconds` | 120 | Minimum interval before fetching the same repository again |
| `fetch_timeout_seconds` | 20 | Fetch timeout per repository |
| `stale_after_seconds` | 900 | Show `stale` after failures have continued this long |
| `gone_token` | `gone` | Token name reported when the upstream disappears from the remote |
| `gone_label` | `gone` | Value reported in that case |
| `merged_token` | `merged` | Token name reported when HEAD is already present in a remote branch |
| `merged_label` | `merged` | Value reported in that case |
| `catchup_token` | `catchup` | Token name used to report catch-up conflicts |
| `catchup_conflict_label` | `conflict` | Value reported only for conflicts; empty when clean |
| `fresh_worktrees` | true | Bring newly created worktrees up to date with their remote base |
| `enabled` | true | When false, clear the plugin's tokens and stop doing work |

An empty token name disables reporting for that token. Configuration is reread on every pass, so you
can change the interval without restarting herdr.

## Worktree screen

With more than a handful of worktrees, remembering which jobs are finished becomes difficult. Sidebar
tokens have limited room. The worktree screen lists all worktrees in one repository with an assessment
and removes only those marked `safe`.

```
 app · 9 worktrees                     fetched 3s ago    ↑↓ move  ⏎ open  r refresh

 safe    add-search-api           gone, merged
 safe    fix-design-detail        merged
 review  test-checkout            ↓12, conflicts
 review  packages                 dirty
 review  fix-login                gone, unpushed?
 keep    agent-admin              up to date
 keep    fix-modal                ↓3, clean catch-up
 blocked main                     main checkout
 blocked fix-agent                agent working

 2 safe · d remove selected · D remove all safe · q close
```

**There are three ways to open it.**

| Entry point | Use |
| --- | --- |
| Key binding (`type = "plugin_action"`, `git-upstream.worktrees`) | Everyday use; `setup` prints this binding |
| `herdr plugin action invoke worktrees --plugin git-upstream` | Scripts and testing |
| `herdr-git-upstream worktrees [--cwd <path>]` | Directly in a terminal; works without the daemon or herdr |

The first two open a herdr popup pane. The last draws in the current pane. herdr has no action picker,
so **bind the action to a key to make it readily accessible**. Both screens select the repository in
this order: explicit `--cwd`, then the workspace in `HERDR_PLUGIN_CONTEXT_JSON`, then
`HERDR_WORKSPACE_ID`, then the process working directory. Popups use their invocation context because
their working directory is the plugin checkout. An invalid or missing workspace in a supplied context
is an error. A shell inside herdr also inherits the workspace ID; use `--cwd <path>` to select another
repository after `cd`.
If that location is not a Git repository, the command reports the error and exits with code 1. A popup
requires herdr to resolve its workspace and reports connection failures. When a direct invocation has
a repository working directory and herdr is unavailable, the screen falls back to `git worktree list`, shows
`herdr unavailable` in the header, and lacks information about open herdr workspaces. Enter cannot
switch workspaces in that mode.

**The screen checks the remote when opened.** The daemon visits only workspaces open in herdr, so it
does not know the state of every other worktree. The screen shows worktree names immediately with
`pending` assessments while it checks local state in the background. You can navigate and open a
worktree during this check; removal stays disabled. The selected worktree remains selected when
assessments reorder the list. After local checks finish, the screen runs
`git fetch` for all branches and `git ls-remote --heads` for deleted-branch detection in parallel, one
remote round trip each, and renders again. **It does not prune.** It does not delete your refs. On this
screen, `gone` means the branch is absent from the `ls-remote` result; only when that result is
unavailable does it fall back to the daemon's fetch records. If fetch fails, the header shows
`fetch failed` and the table retains its local data. Press `r` to retry.

**Assessments.**

| Assessment | Conditions |
| --- | --- |
| `blocked` | The main checkout, a worktree locked with `git worktree lock`, a missing directory, or an agent working in herdr. Cannot be removed |
| `safe` | Clean, including no untracked files, and either `merged` or `gone` with a verified ahead count of zero. Classified as finished |
| `review` | Dirty, would conflict when catching up, or `gone` with unpushed commits or an unknown ahead count because its tracking ref was already deleted |
| `keep` | Everything else: work in progress that is up to date or can catch up cleanly |

Rows are sorted by assessment (`safe`, `review`, `keep`, `blocked`), then branch name. Descriptions use
the same assessment functions as the sidebar tokens.

**Keys.**

| Key | Action |
| --- | --- |
| `↑` `↓` `j` `k` | Move |
| `Enter` | Select the workspace if open in herdr; otherwise run `herdr worktree open --cwd <main-checkout> --path <path> --focus`. Then close the screen |
| `d` | Remove the selected worktree without confirmation if `safe`; otherwise show the reason in the footer |
| `D` | Ask `Remove N worktrees? y/N` once, then remove all confirmed `safe` worktrees |
| `r` | Fetch again |
| `q` `Esc` `Ctrl-C` | Close |

**Removal rules.** Only `safe` worktrees can be removed. For one open in herdr, the screen runs
`herdr worktree remove --workspace <id>`; for one only on disk, `git worktree remove <path>`.
**Removal is never forced.** Immediately before removal, it rechecks the target, HEAD, and safety
assessment, and refuses if they changed. herdr and Git also check file changes at the final removal
boundary. `D` removes only the targets shown when the confirmation opened; background refresh cannot
expand that set. A close request during removal waits for the in-flight removal to finish. An open
worktree whose agent state cannot be queried is also protected from removal. Branches are kept.
Only `D` requires confirmation; [ADR 0002](adr/0002-removal-boundary.md) explains the decision.

On Windows, only cross-compilation has been checked. Console raw mode and relative commands in popup
panes have not been tested at runtime.

## Creation popup

`herdr-git-upstream new-worktree [--cwd <path>]` opens a screen for choosing a new worktree's base branch.
herdr's built-in popup starts from the current HEAD. This popup lists the current branch, its upstream,
the remote default branch, and the repository's configured integration branches first, followed by
all local branches and known remote-tracking branches in name order. Each distinct ref appears once;
remote HEAD aliases and pull-request/tag refs are excluded. The menu shows your position and total count.
`Base` starts collapsed: press `Tab` to focus it, then `Enter` to
open the dropdown. Use `↑`/`↓` and `Enter` to confirm a base. Return to the branch-name field with `Tab`
and press `Enter` to create. Browsing the dropdown does not change the confirmed base or suggested name.

Use `herdr plugin action invoke new-worktree --plugin git-upstream` to open it in a herdr pane.
Creation requires a running herdr server.

To bind it to a key, enable the commented `git-upstream.new-worktree` block from `setup`, or use the
block below. When the required sidebar tokens and worktrees binding are already configured, `setup`
omits this optional block. In your existing `[keys]` table, set `new_worktree = ""` to free
`prefix+shift+g` from herdr's built-in creation popup; edit that table instead of adding a second
`[keys]` header. Then uncomment and add this command binding after that table. The plugin does not
edit your configuration or key bindings for you.

```toml
# [[keys.command]]
# key = "prefix+shift+g"
# type = "plugin_action"
# command = "git-upstream.new-worktree"
# description = "git upstream: new worktree"
```

The suggested name comes from the base branch and avoids names already used by local branches,
remote-tracking branches, or worktrees. If occupied, it tries `-2`, `-3`, and so on. The next suggestion
after `feature-2` is `feature-3`. The cursor starts at the end: typing appends and `Backspace` removes
the last character. Long names scroll to keep the end and cursor visible.
Once you edit the name, changing the base no longer overwrites your input.

| Key | Action |
| --- | --- |
| `Tab` | Switch between the name and base fields; discard an open menu |
| `↑` `↓` | Move the highlight in the open base dropdown |
| Characters and `Backspace` | Enter or edit the name |
| `Enter` | Name field: create. Base field: open dropdown. Open dropdown: confirm the highlighted base |
| `Esc` | Close the dropdown first; when closed, cancel the popup |
| `Ctrl-C` | Cancel the popup |

Recommended remote candidates are fetched individually after the popup opens. Other remote branches
are fetched when confirmed as the base, so opening a large list does not fetch every branch.
You can edit the name and choose a
base while waiting. Creation waits for the selected remote candidate to finish fetching. If its fetch
fails, reopen the popup to retry. The path preview combines `[worktrees] directory`, the repository
name, and herdr's branch slug rules. The command does not pass `--path`; herdr chooses the actual path.

When creating a **new branch** from a remote-tracking ref, the plugin unsets the automatically assigned
upstream. Otherwise, `git pull` and the sidebar counts would keep following the base branch, such as
main, instead of the feature branch. Set an upstream on the first push to restore those tokens.
If you manually enter an existing branch name, herdr opens that branch and the plugin preserves its
upstream. The automatic freshening hook does not move an explicitly selected base other than the current branch, or an existing
branch. If a completed creation's name is later reused for a new branch based on the current branch,
the normal automatic freshening flow applies.

## How it works

```
herdr startup hook ─┐
                    ├─► daemon (runs outside herdr)
focus events ───────┘       │
  write a note, then exit   ├─ narrow fetches per repository (current and integration branches)
                           ├─ gone / merged / catchup assessment per workspace
                           └─ token reporting per workspace
```

Several design choices have specific reasons.

**Focus hooks do not fetch.** herdr limits concurrent plugin commands to 32. Focus changes frequently,
and fetching is slow. If a hook occupies a slot for tens of seconds, it delays other plugins' hooks,
such as those that rename tabs. Hooks therefore write a file and return immediately; a daemon outside
herdr does the actual work. The less frequent `worktree.created` hook fetches and freshens inline so
it does not defer moving the base until the user has started working.

**Focus events revive the daemon.** herdr fires its startup hook when the server restores a session,
but not when you have just linked or enabled a plugin. Focus events check whether the daemon is alive,
so the plugin can start working after installation without a herdr restart.

**Daemon fetches are narrow and leave your work alone.** They fetch one ref at a time: the current
branch's upstream and each integration branch. They leave tags, pruning, and submodules alone and
disable automatic garbage collection with `gc.auto`. `--no-write-fetch-head` prevents rewriting
`FETCH_HEAD`; otherwise, a user who manually fetched and was about to run `git merge FETCH_HEAD` could
merge the wrong commit. `GIT_OPTIONAL_LOCKS=0` also avoids optional locks.

**Your SSH settings are preserved.** Overriding `GIT_SSH_COMMAND` or `core.sshCommand` can break fetching
for users with a dedicated key or ProxyCommand. The plugin supplies noninteractive SSH only when
neither is configured. A timeout still bounds stalled commands.

**Git supplies the remote and tracking ref.** The remote comes from `branch.<name>.remote`. Remote names
can contain `/`, so the plugin checks registered remotes instead of guessing whether a value is a URL.
A local tracking branch configured with `.` has no remote to fetch and is skipped. Git is asked for the
tracking ref first. If the branch has never been fetched and Git cannot resolve it, the plugin derives
it from the remote's fetch refspec. That unfetched branch is one of the cases this plugin needs to help.

**Shared refs are fetched once.** Linked worktrees share their ref store, so workspaces tracking the
same branch benefit from a single fetch. Different branches are fetched separately.

**Tokens expire.** If the daemon dies, its values disappear automatically. A permanently stale number
in the sidebar would be worse than no number.

**Lock renewal runs separately from the work loop.** A pass has no fixed upper bound: an unresponsive
remote can consume its timeout, and many workspaces add up. Renewing the lock inside that loop would
let it grow stale while the daemon is still working, allowing another daemon to take over and run
alongside it.

**Each herdr server gets its own daemon.** herdr uses a separate server per session, but one plugin state
directory. Sharing a lock would let the first daemon prevent other sessions from starting theirs,
leaving those sessions without tokens.

**Directories follow herdr's rules.** Commands launched by herdr receive directory locations through
plugin environment variables; direct invocations may lack those directory variables. Looking elsewhere
would ignore configuration and split the locks, allowing two daemons to start.

**New worktrees move only by fast-forward.** The plugin identifies the base branch, fetches it, and
runs `merge --ff-only`. A fast-forward preserves existing commits. If the worktree is dirty, the new
branch already has its own commits, or multiple possible base branches make the choice ambiguous,
it does nothing.

**A first failure does not trigger a warning.** A brief network interruption or disconnected VPN should
not immediately show `stale` and compete with problems that need attention. The warning depends on how
long failures have continued.

**The daemon derives `gone` from fetch failures.** It fetches the current branch's upstream as a single
ref. If the branch was deleted remotely, fetching fails with a missing-remote-ref error; that record
is the `gone` signal. This adds no `ls-remote` call or pruning and does not delete your refs. Because
retrying cannot fix a missing branch, that failure does not count toward `stale`. No extra evidence is
required, so a branch first seen after remote deletion and local pruning can still be `gone`. Repositories
without any commits are excluded: Git configures `branch.main` even just after cloning an empty remote,
which produces the same fetch error, but an unborn branch cannot have disappeared.

**`merged` uses ancestry checks and merge-tree comparisons.** First,
`git for-each-ref --contains HEAD refs/remotes/` checks whether a remote branch other than the branch's
own refs contains HEAD. Own refs include copies on every remote (`refs/remotes/<remote>/<branch>`) and
the upstream tracking ref. A push alone makes those copies contain HEAD, so they are not evidence.
Destinations of refspecs that fetch non-branch refs, such as `refs/pull/*/head`, do not count either.
This is one local command, even with hundreds of refs. If ancestry does not establish inclusion, the
plugin runs `git merge-tree --write-tree <integration> HEAD` for each integration branch. If the result
tree equals the integration branch's tree, merging changes nothing and the branch is considered
`merged`. This comparison detects squash and rebase merges. See
[ADR 0001](adr/0001-merged-judgement.md).

Integration branches combine the remote default branch with repository-specific additions. The remote
default comes from `refs/remotes/<remote>/HEAD` when it points to an existing ref; a stale alias left
after a remote default change is not trusted. Otherwise, the fetch phase queries `ls-remote --symref`
once per day, records the answer, and uses it from that pass onward. If the default remains unknown,
`merged` is not assessed: the plugin could not recognize a checkout of the integration branch itself,
and a remote descendant branch would be enough for the ancestry check to label it `merged`.
For repositories that merge into a branch such as `origin/develop` instead of `origin/main`, configure
that repository as follows. Multiple values are allowed, and all linked worktrees share them.

```sh
git config --add git-upstream.mergeTarget origin/develop
```

**`merged` does not prove that a branch was merged.** If the integration branch changes the same files
again after a merge, the merge-tree comparison can miss it. `gone` is therefore the primary signal of
finished work, with `merged` as supporting evidence. Own refs are excluded, so merely pushing a branch
does not mark it `merged`. A checkout of an integration branch itself is not assessed. However, the
ancestry check cannot distinguish a branch created from HEAD from a branch that received HEAD through
a merge. If someone branches from your branch and pushes it, that descendant ref contains your HEAD
and can cause your branch to be marked `merged`.

**`catchup` tests a merge without changing the worktree.** The exit code of
`git merge-tree --write-tree <tracking-ref> HEAD` indicates whether catching up would conflict. It runs
only when the behind count is greater than zero. Results are stored per branch with the pair of HEAD
and tracking-ref commits, and reused while that pair is unchanged. Branches sharing `origin/main` do
not evict each other's cached results. These records also live separately from fetch records, so an
assessment cannot overwrite fetch results written by another session's daemon. Pairs that could not
be assessed, such as unrelated histories or timeouts, are recorded too and are not retried for an hour.
`merge-tree --write-tree` was introduced in Git 2.38. Older versions omit this token and use ancestry
alone for `merged`. The `merge_tree_supported` field in `status` reports support.

**Integration branches are fetched too.** Stale tracking refs would make `merged` stale. Each integration
branch gets its own fetch job per repository, deduplicated when the current branch already uses that
ref. The same throttling rules apply.

## Commands

```sh
herdr plugin action invoke worktrees --plugin git-upstream # Open the worktree screen in a popup pane
herdr plugin action invoke refresh   --plugin git-upstream # Refresh now, ignoring the throttle
herdr plugin action invoke start     --plugin git-upstream # Start the daemon
herdr plugin action invoke stop      --plugin git-upstream # Stop the daemon
```

You can also invoke the executable directly:

```sh
./bin/herdr-git-upstream setup                 # Print settings to paste into config.toml; does not edit files
./bin/herdr-git-upstream status                # Print daemon and configuration status as JSON
./bin/herdr-git-upstream refresh
./bin/herdr-git-upstream worktrees             # Draw in the current pane; --cwd <path> selects a repository
./bin/herdr-git-upstream open-worktrees        # Open a herdr popup pane; called by the action
./bin/herdr-git-upstream new-worktree          # Open the base-selection popup; accepts --cwd <path>
./bin/herdr-git-upstream open-new-worktree     # Open the creation popup in a herdr pane
```

`worktrees` and `new-worktree` require terminal standard input and output. They exit with code 2 when piped.

## Troubleshooting

Start with the log file reported by `status`:

```sh
./bin/herdr-git-upstream status
tail -f "$(./bin/herdr-git-upstream status | sed -n 's/.*"log_path": "\(.*\)".*/\1/p')"
```

For detailed logging, restart the daemon with `HERDR_GIT_UPSTREAM_DEBUG=1`. This level also reports why
`merged` was assigned: an ancestry check or a merge-tree comparison with a particular integration branch.

If `catchup` never appears, inspect `git_version` and `merge_tree_supported` in `status`. The token is
disabled below Git 2.38.

If nothing appears, usually the sidebar `rows` do not contain the tokens, or the sidebar is collapsed.
When collapsed, herdr renders only workspace numbers and state dots. Missing configuration appears as
`sidebar_configured: false` in `status`; `setup` prints the entries to paste.

## Limitations and notes

- Periodic remote updates only fetch. A newly created worktree may also be fast-forwarded when its
  checks pass; setting `fresh_worktrees` to false disables that automatic freshening. Separately,
  explicit actions in the creation popup or worktree screen create or remove worktrees. Removal is
  never forced, and branches are kept.
- Git 2.31 or later is required. Fetching uses `--no-write-fetch-head`, introduced in
  [Git 2.29](https://github.com/git/git/blob/v2.29.0/Documentation/RelNotes/2.29.0.txt#L43-L44), while the
  worktree screen reads `locked` and `prunable` fields added to porcelain output in
  [Git 2.31](https://github.com/git/git/blob/v2.31.0/Documentation/RelNotes/2.31.0.txt#L56-L58).
  Merge-tree comparisons and `catchup` require Git 2.38 or later. On earlier supported versions,
  `merged` uses ancestry alone and `catchup` is omitted. Runtime tests used Git 2.55.0; Git 2.31 itself
  has not been tested.
- `merge-tree --write-tree` leaves result tree objects in the object store. Repeating a merge of the same
  two commits produces the same tree rather than another object. `catchup` skips the command entirely
  while the HEAD and tracking-ref commit pair stays the same. Repeating those checks therefore does not
  keep growing the repository. Unreferenced objects can be collected by `git gc`; automatic cleanup
  (`gc.auto`) is disabled for this command too.
- If sidebar tokens are missing, check `invalid_token_names` in `status`. herdr validates all token names
  in a request together: one invalid name rejects the whole request. Names may contain `[A-Za-z0-9_-]`
  and must be at most 32 characters. The plugin filters invalid names beforehand and lists them there.
- In workspaces where panes have been swapped, herdr does not reassign the root pane. A non-worktree
  workspace can therefore be counted against a different repository from the one its sidebar branch
  represents. Worktree workspaces use the checkout path remembered by herdr and avoid this problem.
- Repositories on a remote host connected through `herdr machine` need the plugin running on that host's
  server separately.
- Automatic prefetch from `git maintenance` is not a substitute: it updates `refs/prefetch/*`, leaving
  the `refs/remotes/*` values used by herdr unchanged.
