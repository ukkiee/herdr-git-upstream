# herdr 동작 참고

이 플러그인의 설계는 herdr가 실제로 어떻게 도는지에 기대고 있다. 여기에 적은 것은 전부 herdr
v0.9.0 소스에서 확인했거나 실측한 것이며, 출처를 함께 적었다. 짐작으로 적은 것은 없다.

herdr가 판을 올리면 이 문서의 근거부터 다시 확인해야 한다.

## 원격을 건드리지 않는다

herdr는 사이드바에 앞뒤 커밋 수를 그리지만 `git fetch`를 실행하지 않는다. 유지보수자가
[이슈 1253번](https://github.com/herdrdev/herdr/issues/1253)에서 밝혔고, 소스 전체를 뒤져도
`git fetch`를 부르는 곳은 시험 코드(`src/workspace/git/status.rs`)와 플러그인 설치
코드(`src/cli/plugin.rs`)뿐이다.

**이 플러그인이 존재하는 이유가 여기에 있다.**

## 사이드바 git 표시

| 항목 | 값 | 출처 |
| --- | --- | --- |
| Space 행 기본값 | `[["state_icon","workspace"],["branch","git_status"]]` | `src/config/sidebar.rs:466` |
| 갱신 주기 | 1500 ms | `src/app/mod.rs:39` |
| 계산식 | `git rev-list --left-right --count HEAD...<추적 참조>` | `src/workspace/git/status.rs:317` |
| 비교 대상 | 로컬에 저장된 원격 추적 참조 | `src/workspace/git/discovery.rs:331` |
| 갱신 조건 | 클라이언트가 붙어 있을 때만 | `src/server/headless.rs:3333` |

`git_status`는 앞뒤 수가 0이 아닐 때만 그려진다(`src/ui/sidebar/tokens.rs:157`).

### 들여쓴 worktree 행에서는 내장 토큰이 지워진다

같은 저장소를 공유하는 워크스페이스가 둘 이상이고 그중 하나가 본 체크아웃이면, herdr는 이들을 묶어
자식 행을 들여쓴다(`src/client/shell/sidebar.rs:448-535`). 들여쓴 행에는 `suppress_git_details`가
서고, 그때 `branch`와 `git_status` 토큰이 버려진다(`src/ui/sidebar/tokens.rs:153-161`,
`src/client/shell/sidebar.rs:610`).

**커스텀 `$이름` 토큰은 버려지지 않는다.** worktree 행에 무언가를 보이려면 토큰을 직접 보고해야 하는
이유가 이것이다.

접은 사이드바(compact rail)는 워크스페이스 번호와 상태 점만 그린다. 브랜치도 앞뒤 수도 보이지
않는다(`src/client/shell/sidebar.rs:27-181`).

## worktree 생성

기준 커밋의 기본값은 원본 체크아웃의 `HEAD`다.

```rust
// src/app/api/worktrees/deferred.rs:118
let base = params.base.unwrap_or_else(|| "HEAD".into());
```

단축키로 여는 흐름도 `base: None`을 넘겨 같은 기본값을 쓴다(`src/app/api/worktrees.rs:2160`).
fetch는 하지 않는다. 그래서 손에 쥔 브랜치가 뒤처져 있으면 새 worktree도 뒤처진 자리에서 시작한다.

브랜치 이름이 이미 로컬에 있으면 그것을 체크아웃하고, 없으면 기준에서 새로 만든다.

```rust
// src/worktree.rs:308
if local_branch_exists(...) { add_existing_branch } else { add -b ... base }
```

이름을 비우면 `worktree/brave-river-0f3a` 꼴을 지어 준다(`src/worktree.rs:21`).

### 브랜치 이름에서 디렉터리 이름을 만드는 규칙

```rust
// src/worktree.rs:34 branch_to_path_slug
// 영숫자는 소문자로, 나머지 문자는 대시 하나로 줄이고, 앞뒤 대시를 뗀다
```

| 브랜치 | 디렉터리 |
| --- | --- |
| `widget-studio/agent-admin` | `widget-studio-agent-admin` |
| `DEMO-1601-widget-preset-ui` | `demo-1601-widget-preset-ui` |

### 삭제

herdr는 지우기 전에 `git status --porcelain --untracked-files=all`로 손댄 것이 있는지 보고,
있으면 `dirty_worktree_requires_force`로 거절한다(`src/app/api/worktrees/deferred.rs:262`,
`src/worktree.rs:214`). git 자체도 `--force` 없이는 거절한다.

깨끗한 worktree를 지우면 체크아웃만 사라지고 브랜치와 커밋은 남는다(실측).

CLI는 `--workspace ID`를 요구하므로 herdr에 열려 있지 않은 worktree는 지우지 못한다.
디스크에만 남은 것을 지우려면 `git worktree remove`를 직접 불러야 한다.

## 플러그인 실행 환경

### 동시 실행 상한

herdr는 동시에 도는 플러그인 명령을 32개로 제한한다(`src/app/api/plugins/runtime.rs:12`).
포커스 이벤트처럼 자주 발화하는 훅이 네트워크를 타면 그 자리를 오래 차지해 다른 플러그인의 훅까지
밀린다. **이벤트 훅은 즉시 끝나야 한다.**

### startup 훅은 일회성이며 link/enable 때 발화하지 않는다

서버가 세션을 복구할 때와 live handoff 때만 부른다(`src/app/api/plugins/runtime.rs:183`).
사용자가 플러그인을 방금 붙였을 때는 발화하지 않으므로, 다른 이벤트에서 데몬 생존을 확인해야 한다.

### 환경변수

`HERDR_BIN_PATH`, `HERDR_PLUGIN_CONFIG_DIR`, `HERDR_PLUGIN_STATE_DIR`, `HERDR_PLUGIN_ROOT`,
`HERDR_SOCKET_PATH`, `HERDR_PLUGIN_EVENT`, `HERDR_PLUGIN_EVENT_JSON`, 그리고 문맥이 있을 때
`HERDR_WORKSPACE_ID` / `HERDR_TAB_ID` / `HERDR_PANE_ID`(`src/app/api/plugins/runtime.rs:42-80`).

셸에서 직접 부를 때는 이 값들이 없다. 그때 herdr와 다른 자리를 대안으로 고르면 설정이 무시되고
잠금이 갈려 데몬이 둘 뜬다. herdr의 경로 계산을 그대로 옮겨 두었다(`internal/herdrpaths`).

| 용도 | 자리 | 출처 |
| --- | --- | --- |
| 설정 | `<설정 뿌리>/plugins/config/<id>` | `src/plugin_paths.rs:15` |
| 상태 | `<상태 뿌리>/plugins/<id>` | `src/plugin_paths.rs:21` |

뿌리는 `XDG_*`를 먼저 보고, 없으면 플랫폼 기본값을 쓴다(`src/config/io.rs:30-100`).

### 이벤트 페이로드

`HERDR_PLUGIN_EVENT_JSON`은 봉투 전체다.

```json
{"event": "worktree.created", "data": {"type": "worktree_created", "workspace": {...}, "worktree": {"path": "...", "branch": "..."}}}
```

### 플러그인 pane 은 진짜 터미널을 받는다

`placement`에 `overlay`, `popup`, `split`, `tab`, `zoomed`가 있다(`src/api/schema/plugins.rs:445`).
방향키를 받는 대화형 화면이 실제로 돈다. 이미 설치된 fullerzz.sesh 피커가 Bubble Tea를,
herdr-file-viewer가 crossterm을 쓴다.

### 매니페스트가 지원하는 것

`build`, `startup`, `actions`, `events`, `panes`, `link_handlers` 여섯 가지뿐이다
(`src/api/schema/plugins.rs`). **herdr의 대화상자에 필드를 끼워 넣는 훅은 없다.**
기본 worktree 팝업을 고칠 수 없고, 우리 팝업으로 갈아끼우는 것만 가능한 이유다.

각 항목은 `platforms`로 플랫폼을 좁힐 수 있다. 액션의 `contexts`는
`global`, `workspace`, `tab`, `pane`, `selection`이다.

## 메타데이터 토큰

`herdr workspace report-metadata <ws> --source ID --token 이름=값 [--ttl-ms N]`

| 제약 | 값 |
| --- | --- |
| 이름 | `^[A-Za-z0-9_-]{1,32}$` |
| 값 | 앞뒤 공백 제거, 80자로 자름 |
| 빈 값 | 그 키를 지움 |
| 한 요청당 | 16개 |
| 자원당 보관 | 32개 |
| `ttl_ms` | 1 ~ 86,400,000 |

**이름 검사는 요청 단위다.** 하나라도 규칙에 어긋나면 요청 전체가 `invalid_metadata_token`으로
거절되어 나머지 토큰까지 함께 사라진다(`src/app/api_helpers.rs:256`, `src/app/api/workspaces.rs:257`).

토큰은 서버를 다시 켜면 사라진다. TTL을 두는 이유가 이것이다.

실측으로 확인한 것: `↓12` 같은 유니코드 값이 그대로 저장되고, 빈 값을 보내면 키가 지워지며,
상한을 넘긴 TTL은 `invalid_metadata_ttl`로 거절된다.

## 워크스페이스의 정체

herdr는 워크스페이스가 어느 디렉터리에 속하는지를 첫 탭의 뿌리 페인이 보는 셸 작업 디렉터리로
정한다(`src/workspace.rs` `resolved_identity_cwd_from`, `src/workspace/tab.rs` `cwd_for_pane`).
`foreground_cwd`는 정체에도 라벨에도 git 상태에도 쓰지 않는다.

`herdr pane list`가 주는 순서는 보통 이 규칙과 같지만, 사용자가 페인을 맞바꾸면 herdr가 뿌리
페인을 새로 지정하지 않아 어긋날 수 있다. worktree 워크스페이스는 herdr가 기억해 둔
`worktree.checkout_path`가 더 안정적이라 그쪽을 먼저 쓴다.

## 탭 바 명령 항목

`[ui] tab_bar_right`의 `command` 항목은 herdr 서버가 직접 실행한다. 설정을 반영한 직후 한 번 돌고
이후 `interval_seconds`마다 돈다(`src/app/tab_bar_status.rs:114`). 클라이언트가 붙어 있지 않아도
서버 루프에서 계속 돈다(`src/server/headless.rs:3370`). 마지막 출력 한 줄만 쓰이고 80자로 잘린다.

이 플러그인은 데몬을 쓰므로 이 경로를 쓰지 않지만, herdr가 주기 실행을 제공한다는 사실 자체는
기억해 둘 값어치가 있다.
