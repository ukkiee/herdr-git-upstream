# herdr-git-upstream

**English** | [한국어](README.ko.md)

## What is this plugin?

**A [herdr](https://herdr.dev) plugin for tracking remote Git changes and managing worktrees.**
It refreshes remote-tracking refs in the background, shows signals in the sidebar, and provides
popups for reviewing worktrees and choosing where to start a new one.

herdr reads locally cached Git information. This plugin runs the fetches that keep it current, so a
teammate's push can appear in your sidebar without a manual fetch.

```text
● app                main                ↓3
  · fix-checkout     fix/checkout        ↓3 ↑1 conflict
  · add-search       feat/search         gone merged
```

## What does it do?

| Feature | What you get |
| --- | --- |
| Background refresh | Periodic fetches for repositories open in herdr |
| Sidebar signals | Ahead/behind counts, disappeared upstreams, content already present remotely, and catch-up conflicts |
| Worktree screen | All worktrees in the repository, with assessments and removal of eligible worktrees |
| Creation popup | A base-branch dropdown and an editable name for a new worktree |
| Automatic freshening | A guarded fast-forward of a newly created worktree when its base is behind the remote |

Periodic refresh only fetches refs. Automatic freshening uses `--ff-only` when its checks pass and can
be disabled. Worktree removal is an explicit action, never forced, and leaves branches in place.

## How do I install it?

Requires **herdr 0.7.5+**, **Go 1.24+**, and **Git 2.31+**. Use Git 2.38+ for conflict checks and
squash/rebase merge detection. Installation builds the executable from Go's standard library.
Linux, macOS, and Windows are targeted; Windows has only been cross-compiled, not runtime-tested.

### 1. Install the plugin

```sh
herdr plugin install ukkiee/herdr-git-upstream
```

For a local checkout, see [local development](docs/REFERENCE.md#local-development).

### 2. Add the sidebar tokens

Open herdr's `config.toml`. `herdr --help` prints its path; on Linux and macOS the default is
`~/.config/herdr/config.toml`. Add the following, or merge the token entries into your existing `rows`.
If `[ui.sidebar.spaces]` already exists, edit it instead of adding another table with the same name.

```toml
[ui.sidebar.spaces]
rows = [
  ["state_icon", "workspace"],
  [
    "branch",
    { token = "$behind", fg = "#f38ba8", bold = true },
    { token = "$ahead", fg = "#a6e3a1" },
    { token = "$gone", fg = "#6c7086" },
    { token = "$merged", fg = "#6c7086" },
    { token = "$catchup", fg = "#fab387", bold = true },
    { token = "$sync_stale", fg = "#6c7086", dim = true },
  ],
]
```

Custom tokens also appear on indented worktree rows, where herdr omits its built-in Git tokens.
Without these entries the plugin can still fetch, but its own signals remain hidden.

### 3. Apply the configuration

```sh
herdr config check && herdr server reload-config
herdr plugin action invoke start --plugin git-upstream
```

The second command starts the daemon in the running herdr session. Later startup and focus events
keep it running. Open a Git workspace to see its signals; zero or inapplicable values stay empty.

## How do I use it?

### Read the sidebar

| Display | Meaning |
| --- | --- |
| `↓3` / `↑1` | Three commits to receive / one commit to push, compared with the upstream |
| `gone` | The upstream branch has disappeared from the remote |
| `merged` | Git checks find HEAD's contents already present in another remote branch |
| `conflict` | Catching up with the upstream would conflict |
| `stale` | Fetch failures have continued long enough that the counts may be outdated |

`merged` is an assessment, not proof that a PR was merged. A new branch with no commits of its own can
also receive it. See [token details](docs/REFERENCE.md#tokens) and
[assessment limits](docs/REFERENCE.md#how-it-works).

To refresh immediately:

```sh
herdr plugin action invoke refresh --plugin git-upstream
```

### Open the worktree screen

Run from the repository's workspace in herdr:

```sh
herdr plugin action invoke worktrees --plugin git-upstream
```

The popup shows **all worktrees in that repository**, including ones not open in herdr. Names appear
first with `pending` while assessments run in the background. `safe` means eligible for removal;
`review` needs attention, `keep` is ongoing work, and `blocked` is protected.

| Key | Action |
| --- | --- |
| `↑` / `↓` or `j` / `k` | Move the selection |
| `Enter` | Open or focus the selected worktree |
| `d` / `D` | Remove the selected `safe` worktree immediately / confirm removal of all `safe` worktrees |
| `r` | Refresh |
| `q` / `Esc` | Close |

Removal stays disabled while the first assessment runs. Before each removal, the plugin rechecks the
target and safety assessment. See [assessment and removal rules](docs/REFERENCE.md#worktree-screen).

### Create a worktree

```sh
herdr plugin action invoke new-worktree --plugin git-upstream
```

1. Edit the suggested branch name. The cursor starts at the end; typing appends and `Backspace` removes one character.
2. Press `Tab` to focus `Base`, then `Enter` to open the dropdown.
3. Choose a branch with `↑` / `↓`, then confirm it with `Enter`.
4. Press `Tab` to return to the name field, then `Enter` to create.

The menu shows the current branch, upstream, remote default, and configured integration branches first,
followed by all local and known remote-tracking branches. It shows the current position and total count.
Remote branches are those known locally, not a live list of every branch on the server.

The selected remote base is fetched before creation. `Esc` closes the dropdown first, then cancels the
popup. Creation requires a running herdr server. See [creation details](docs/REFERENCE.md#creation-popup)
for path selection, existing branch names, and upstream handling.

### Keyboard shortcuts

Optionally add these **example bindings** to `config.toml`, choosing unused keys in your configuration.
They take effect after `herdr config check && herdr server reload-config`.

```toml
[[keys.command]]
key = "prefix+shift+u"
type = "plugin_action"
command = "git-upstream.worktrees"
description = "git upstream: worktrees"

[[keys.command]]
key = "prefix+shift+i"
type = "plugin_action"
command = "git-upstream.new-worktree"
description = "git upstream: new worktree"
```

Press your configured prefix, then `Shift+U` for the worktree screen or `Shift+I` for creation.
For example, with a `Ctrl+A` prefix, creation is `Ctrl+A` → `Shift+I`.
These keys are user configuration, not shortcuts assigned automatically by the plugin.

### Settings and help

Default settings work without a plugin configuration file. To change the refresh interval or disable
automatic freshening, place `config.json` in the directory printed by
`herdr plugin config-dir git-upstream`. See [configuration](docs/REFERENCE.md#configuration) and
[config.example.json](config.example.json). Extra integration branches are optional and configured per
repository; no project-specific branch name is required.

If signals or popups are missing, start with [troubleshooting](docs/REFERENCE.md#troubleshooting).
For development, read the [implementation plan](docs/PLAN.md), [herdr integration notes](docs/HERDR.md),
and [agent instructions](AGENTS.md).

Inspired by [mariotmc/herdr-source-control](https://github.com/mariotmc/herdr-source-control), which
demonstrated the missing-fetch problem and a throttled refresh solution. Licensed under [MIT](LICENSE).
