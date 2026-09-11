# 구현 계획

## 이 플러그인은 무엇인가

**작업이 최신 바닥 위에 서 있게 한다.**

herdr는 로컬 git 상태를 잘 보여 주지만 원격에는 한 번도 말을 걸지 않는다. 그래서 herdr가 그리는
숫자는 마지막으로 누군가 fetch 한 시점의 이야기이고, 새로 만든 worktree는 손에 쥔 낡은 커밋에서
시작한다. 이 플러그인은 원격 쪽 일을 대신 맡아 그 두 가지를 고친다.

용어는 [CONTEXT.md](../CONTEXT.md)를 따른다. 되돌리기 어려운 결정은 [adr/](adr/)에 있다.

## 경계

무엇을 넣을지 판단하는 기준은 하나다. **원격을 알아야만 답할 수 있는 질문인가.**

넣는다.

- 얼마나 뒤처졌나
- 따라잡으면 충돌하나
- 이 브랜치는 원격에서 끝난 작업인가
- 새 작업을 어디서 시작할까

넣지 않는다. 이유는 이미 잘하는 쪽이 있기 때문이다.

| 하지 않을 것 | 이미 하는 쪽 |
| --- | --- |
| PR 상태 표시 | git-shepherd, mergr, herdr-pr-watch 등 여럿 |
| lazygit 연동 | herdr-lazygit 등 셋 |
| 스택 브랜치 깊이 | herdr-git-stack |
| 로컬 작업 트리 상태(+2 ~1 ?3) | herdr-git-status, herdr-git-detail |
| worktree 목록 이동 | herdr 내장 `open_worktree` |
| 로컬 브랜치 정리 | 이 플러그인의 경계 바깥. [ADR 0002](adr/0002-removal-boundary.md) |

worktree 삭제는 경계에 걸쳐 있다. herdr-shear가 이미 잘하지만 판정 기준이 **로컬** 기본 브랜치라,
로컬 main이 뒤처져 있으면 이미 병합된 브랜치를 살아 있는 것으로 잘못 분류한다. 우리는 원격 기준으로
판정할 수 있으므로 그 차이만큼만 들어간다. 자세한 것은 아래 3번.

## 지금까지 만든 것

| 기능 | 상태 |
| --- | --- |
| 주기적 fetch (기본 60초, 저장소당 같은 참조 120초 스로틀) | 완료 |
| 로컬 pull·커밋·브랜치 전환 후 사이드바 토큰 갱신 | 1초 주기 참조 변경 감지, 변경 대상만 로컬 재계산. [검증·성능](reviews/2026-09-11-local-refresh.md) |
| `$behind` / `$ahead` / `$sync_stale` 토큰 보고 | 완료 |
| 새 worktree 를 기준 브랜치 최신으로 빨리 감기 | 완료 |
| 0. `setup` 설정 안내와 `status` 설정 진단 | 완료 (`cb9ea33`) |
| 1·2. `$gone` / `$merged` / `$catchup` 판정 | 완료 (`d38cb4c`) |
| 3. worktree 화면, 안전 판정 재확인과 삭제 | 완료 (`14a9820`, `889c9fe`) |
| 4. 기준 브랜치를 고르는 생성 팝업 | 완료 (`4dcf54c`) |
| 영문·한국어 README, 0.2.0 문서 정리 | 완료 |
| 전체 교차 기능 검토 | 완료. [검토 기록](reviews/0.2.0-verification.md) |
| herdr 연결 실측 | 연결·팝업·임시 저장소 생성/삭제·설정 적용 확인. 사용자가 mfe 사이드바 토큰 표시 확인 |
| 팝업의 호출 저장소 선택 | 완료 (`28f7c92`). 사용자가 실제 `mfe · 27 worktrees` 확인 |
| 첫 목록 표시 속도 | 완료 (`765b6f1`). 최초 inventory 표시와 배경 판정, 선택 경로 보존 |
| mfe 통합 기준 설정 | 추가 전후 로컬 판정 비교 완료. 적용은 사용자 결정 |

2026-09-10~11 전체 13개 패키지의 race 시험, vet, 여섯 플랫폼(linux/darwin/windows × amd64/arm64)
교차 컴파일과 매니페스트 TOML 검사를 통과했다. herdr 0.9.0에 0.2.0을 연결해 실행했다.
Windows 실행은 미실측이다. 후속 요청으로 사용자 config.toml에 검증한 설정을 추가했고 사용자가
reload를 실행해 적용했다. 자세한 범위는 검토 기록에 남긴다.

```
cmd/herdr-git-upstream/   명령 진입점
internal/config/          설정 읽기, 토큰 이름 검사
internal/daemon/          갱신 루프, 잠금, 쪽지, 워크스페이스 훑기
internal/freshen/         새 worktree 최신화, 명시적으로 고른 생성 기준 보호
internal/gitrepo/         저장소 찾기, upstream 해석, fetch, 빨리 감기, 판정 재료
internal/herdrcli/        herdr CLI 감싸기
internal/herdrpaths/      herdr 와 같은 규칙으로 디렉터리 찾기
internal/state/           fetch·따라잡기·생성 보호 기록, 데몬 잠금
internal/herdrconfig/     사용자 config.toml 훑기 (setup 과 경로 미리보기가 공유)
internal/setup/           붙여 넣을 설정을 만들어 출력
internal/judge/           gone / merged / 따라잡기 판정과 worktree 판정 규칙
internal/tui/             터미널 화면 기반 (raw 모드, 키 입력, 그리기)
internal/worktreeui/      worktree 화면과 생성 팝업
```

## 실측으로 확인한 사실

설계는 아래 사실에 기댄다. 짐작으로 적은 것은 없다.

- 지워진 원격 브랜치를 지금처럼 좁게 fetch 하면 `couldn't find remote ref` 로 실패한다.
  이 문구는 이미 `IsMissingRemoteRef` 가 잡아 영구 실패로 기록한다.
- refspec 여러 개를 한 번의 fetch 에 담으면 하나만 없어도 전체가 실패하고 아무것도 갱신되지 않는다.
  그래서 참조는 하나씩 가져온다.
- `git merge-tree --write-tree <통합 브랜치> HEAD` 의 결과 트리가 통합 브랜치의 트리와 같으면
  squash 병합도 잡아낸다. 다만 병합 뒤 통합 브랜치가 같은 파일을 다시 고쳤으면 놓친다.
- `git worktree add -b <새> <경로> origin/<브랜치>` 처럼 원격 추적 참조를 시작점으로 주면 git 이
  새 브랜치의 upstream 을 그 원격 브랜치로 자동 설정한다. 지역 브랜치를 시작점으로 주면 생기지 않는다.
- `herdr worktree list` 는 현재 워크스페이스(또는 `--cwd`)가 속한 저장소의 worktree 전부를
  `path`, `branch`, `open_workspace_id`, `is_linked_worktree`, `is_prunable`, `is_detached` 와 함께 준다.
  `source.repo_name` 이 저장소 이름이다.
- `herdr worktree create --branch --base --path --cwd --focus`, `herdr worktree open --path --focus`,
  `herdr worktree remove --workspace <id>`, `herdr workspace focus <id>` 가 있다.
- 플러그인이 실행 중에 pane 을 여는 통로는 `herdr plugin pane open --plugin <id> --entrypoint <pane-id>
  [--placement overlay|split|tab|zoomed] [--focus]` 다. CLI 의 `--placement` 목록에는 `popup` 이 없고,
  매니페스트 `[[panes]]` 의 `placement` 에는 `popup` 과 `width`/`height` 가 있다.
  `--placement` 를 비우면 매니페스트의 popup을 따른다. 실제 연결과 사용자 화면으로 확인했다.
- herdr 기본 키에 `rename_workspace = "prefix+shift+w"` 가 있다. `prefix+shift+u` 는 비어 있다.
- herdr 는 `[worktrees] directory` (기본 `~/.herdr/worktrees`) 로 worktree 뿌리를 바꿀 수 있다.
- 실제 저장소(mfe) 의 worktree 27개 가운데 22개가 git 자체 판정으로 upstream `[gone]` 이다.
  그중 조상 관계로 병합이 잡히는 것은 0개, merge-tree 비교로 잡히는 것은 7개다. 병합 대상은
  `origin/main` 이 아니라 `origin/widget-studio/dev` 인 경우가 많다.

## 구현 명세 (0~4 완료)

### 0. `setup` 명령

**왜.** 이 플러그인은 설정을 두 군데 손봐야 제 몫을 한다. 사이드바 행에 토큰을 넣어야 숫자가 보이고,
키를 묶어야 화면이 열린다. 둘 다 하지 않으면 fetch 만 돌아 herdr 내장 `git_status` 가 정확해지는
데까지다. 그것만으로도 값어치가 있지만, 나머지 절반이 조용히 잠들어 있는 셈이다.

**무엇을.** 붙여 넣을 설정을 출력한다.

```
$ herdr-git-upstream setup

~/.config/herdr/config.toml 에 아래를 더하세요.

[ui.sidebar.spaces]
rows = [
  ["state_icon", "workspace"],
  [
    "branch",
    { token = "$behind",  fg = "#f38ba8", bold = true },
    { token = "$ahead",   fg = "#a6e3a1" },
    { token = "$gone",    fg = "#6c7086" },
    { token = "$merged",  fg = "#6c7086" },
    { token = "$catchup", fg = "#fab387", bold = true },
    { token = "$sync_stale", fg = "#6c7086", dim = true },
  ],
]

[[keys.command]]
key = "prefix+shift+u"
type = "plugin_action"
command = "git-upstream.worktrees"
description = "git upstream: worktrees"

# 기준 브랜치를 고르는 생성 팝업. herdr 내장 팝업 대신 쓰려면 주석을 풀고 new_worktree 키를 비운다.
# [[keys.command]]
# key = "prefix+shift+g"
# type = "plugin_action"
# command = "git-upstream.new-worktree"
# description = "git upstream: new worktree"

통합 브랜치가 origin/HEAD 가 아닌 저장소에서는 그 저장소에서 이렇게 알려 주세요.
  git config --add git-upstream.mergeTarget origin/develop

그다음: herdr config check && herdr server reload-config
```

**어떻게.** TOML 을 파싱하지 않는다. 표준 라이브러리에 파서가 없고, 의존성을 더할 만한 일이 아니다.
config.toml 본문에서 `[ui.sidebar.spaces]`, `$behind` 같은 토큰 이름, `git-upstream.worktrees` 문자열이
있는지만 찾는다. 이미 `[ui.sidebar.spaces]` 를 쓰고 있으면 rows 전체 대신 "이 토큰 항목들을 그 행에
더하라"고 토큰 항목만 보여 준다. 이미 우리 토큰이나 키가 있으면 그 부분은 빼고 남은 것만 알려 준다.
토큰 이름은 설정(config.json)에서 바꾼 값을 그대로 쓴다. 이 훑기는 `internal/herdrconfig` 에 두어
생성 팝업의 경로 미리보기(`[worktrees] directory`)와 `status` 가 함께 쓴다.

**파일을 말없이 고치지 않는다.** 남의 설정 파일을 손대는 플러그인은 신뢰를 잃는다. 출력만 하고
붙여 넣는 것은 사람이 한다.

**`status` 에 두 필드를 더한다.** `sidebar_configured` (우리 토큰이 하나라도 rows 에 있음),
`key_bound` (worktrees 액션이 키에 묶여 있음). 아무것도 안 보인다고 느낀 사람이 `status` 를 쳤을 때
답을 얻게 한다. 부르지 않은 알림은 띄우지 않는다.

### 1. 병합·삭제 판정 (`$gone`, `$merged`)

**왜.** worktree 를 열 개 넘게 열어 두면 어느 것이 끝난 작업인지 사람이 기억하지 못한다.
원격을 봐야만 답이 나오는 질문이다.

**무엇을.** 워크스페이스마다 토큰 둘을 더 보고한다. 값은 라벨 문자열이고, 해당 없으면 빈 값이다.

| 토큰 | 값 | 뜻 |
| --- | --- | --- |
| `$gone` | `gone` | upstream 브랜치가 원격에서 사라졌다. 대개 병합 후 삭제된 것이다 |
| `$merged` | `merged` | HEAD 의 내용이 이미 어느 원격 브랜치에 들어가 있다 |

설정 키는 `gone_token`/`gone_label`, `merged_token`/`merged_label`. 이름을 비우면 보고하지 않는다.

**`gone` 은 어떻게.** 마지막 좁은 fetch 가 "원격 참조 없음"으로 영구 실패했으면 `gone` 이다.
fetch 기록의 `LastErrorPermanent` 가 이미 그 사실을 담고 있으므로 `ls-remote` 도 prune 도 필요 없다.
비용이 0이다. upstream 이 없는 브랜치는 `gone` 을 판정하지 않는다.

**`merged` 는 어떻게.** [ADR 0001](adr/0001-merged-judgement.md). 둘 중 하나면 `merged` 다.

1. 조상 검사. `git for-each-ref --contains HEAD refs/remotes/` 에 자기 추적 참조가 아닌 것이 하나라도
   있으면. 로컬 명령 하나라 참조가 수백 개여도 값이 싸다. 설정이 없어도 "어딘가에 병합되었다"를 잡는다.
2. merge-tree 비교. 통합 브랜치마다 `git -c gc.auto=0 merge-tree --write-tree <통합> HEAD` 의 결과
   트리가 `<통합>^{tree}` 와 같으면. git 2.38 이상에서만 하고, 그 아래에서는 조상 검사만 한다.

통합 브랜치는 원격 기본 브랜치에 `git config --get-all git-upstream.mergeTarget` 의 값들을 더한 것이다.
원격 기본 브랜치는 `refs/remotes/<원격>/HEAD` 에서 읽고, 없으면 `git ls-remote --symref <원격> HEAD` 를
저장소당 한 번 불러 fetch 기록에 함께 저장하고, 그것마저 실패하면 판정하지 않는다.

**upstream 이 없어도 판정한다.** 아직 push 하지 않은 새 브랜치도 `merged` 일 수 있다(dev 에서 방금
만든 빈 브랜치는 HEAD 가 `origin/dev` 의 조상이다). 화면과 사이드바가 같은 답을 내야 한다. 그때 원격은
하나뿐이면 그것, 아니면 `origin`, 그것도 없으면 판정하지 않는다.

**데몬은 통합 브랜치도 가져온다.** 통합 브랜치의 추적 참조가 낡으면 판정도 낡는다. 저장소(CommonDir)마다
통합 브랜치 각각을 별도의 fetch 작업으로 둔다. 현재 브랜치가 곧 통합 브랜치면 FetchKey 가 같아 한 번만
가져간다. 스로틀은 기존 규칙을 그대로 따른다.

**한계는 문서에 적는다.** `merged` 는 HEAD 내용의 포함 여부다. 실제 병합 이력이나 작업 완료를 보증하지 않으며, 다른 브랜치가 HEAD에서 갈라진 경우에도 조상 검사가 성립한다. 주된 신호는 `gone` 이다.

### 2. 따라잡을 때 충돌하는지 미리 보기 (`$catchup`)

**왜.** `↓12` 만으로는 결정을 못 한다. 열두 개 뒤처졌지만 깨끗하게 따라잡히는 것과, 세 개인데 손이
가는 것은 사람이 할 일이 다르다. 조사한 1,045개 플러그인 중 아무도 하지 않는다.

**무엇을.** `$catchup` 토큰. **충돌할 때만** 라벨(기본 `conflict`)을 채우고, 깨끗하거나 뒤처지지
않았으면 빈 값이다. 사이드바에서 눈에 띄어야 할 것은 충돌뿐이고, `clean` 은 화면(3번)에서 보여 준다.
설정 키는 `catchup_token`/`catchup_conflict_label`.

**어떻게.** `git -c gc.auto=0 merge-tree --write-tree <추적 참조> HEAD` 의 종료 코드로 판단한다.
0 이면 깨끗, 1 이면 충돌, 그 밖은 판정 불가. **작업 트리를 전혀 건드리지 않는다.** 결과 트리 객체가
객체 저장소에 남지만, 아래 캐시 덕에 그 양은 무시할 만하다.

- 뒤처짐이 0보다 클 때만 계산한다.
- 결과를 (HEAD, 추적 참조 커밋) 쌍과 함께 fetch 기록에 저장하고, 쌍이 같으면 다시 계산하지 않는다.
- git 2.38 미만이면 이 토큰만 조용히 쉰다. fetch 옵션은 git 2.29 이상이 필요하며, worktree 잠금·prunable 정보를 포함한 전체 기본 기능은 git 2.31 이상을 요구한다.

### 3. worktree 화면

**왜.** 1번과 2번의 판정이 모이면 "어느 것부터 손봐야 하나"에 답하는 표가 된다.
사이드바 토큰은 한 줄에 조금씩만 보여 줄 수 있어서, 스물일곱 개를 한눈에 견주려면 화면이 필요하다.

**무엇을.** 한 저장소의 worktree 전부를 판정과 함께 보이는 화면 하나. 세 갈래로 연다.

| 통로 | 쓰임새 |
| --- | --- |
| 키 설정 (`type = "plugin_action"`, `git-upstream.worktrees`) | 평소 사용. 이것이 정상적인 통로다 |
| `herdr plugin action invoke worktrees --plugin git-upstream` | 스크립트나 시험 |
| `herdr-git-upstream worktrees` | 터미널에서 직접. 데몬 없이도 돈다 |

앞의 둘은 액션이 `herdr plugin pane open --plugin git-upstream --entrypoint worktrees --focus` 를 불러
매니페스트의 `placement = "popup"` 을 따르게 한다. 붙여 본 뒤 매니페스트 값을 따르지 않는 것으로
확인되면 `--placement overlay` 로 물러난다. 마지막은 지금 있는 페인에 그대로 그린다.

herdr 에는 액션을 골라 실행하는 화면이 없고, 액션의 `contexts` 는 아직 쓰이지 않는다
(자세한 것은 [HERDR.md](HERDR.md)). 그래서 **키에 묶기 전에는 없는 것과 같다.**
0번의 `setup` 명령이 그 설정을 내놓는 이유가 이것이다.

```
 mfe · 27 worktrees                     fetched 3s ago    ↑↓ move  ⏎ open  r refresh

 safe    add-shopping-widget-api   gone, merged
 safe    fix-design-detail         merged
 review  design-qa-3               ↓12, conflicts
 review  packages                  dirty
 review  DEMO-1570                 gone, unpushed?
 keep    agent-admin               up to date
 keep    fix-modal                 ↓3, clean catch-up
 blocked widget-studio/dev         main checkout
 blocked fix-agent                 agent working

 2 safe · d remove selected · D remove all safe · q close
```

**저장소는 하나다.** 두 화면의 선택 순서는 명시한 `--cwd`, 플러그인 호출 문맥
(`HERDR_PLUGIN_CONTEXT_JSON.workspace_id`), `HERDR_WORKSPACE_ID`, 현재 디렉터리다.
팝업의 현재 디렉터리는 플러그인 뿌리이므로 호출 문맥을 먼저 읽는다. 주어진 문맥이 잘못되었거나
워크스페이스가 없으면 다른 저장소로 넘어가지 않고 오류로 종료한다.
herdr 안의 일반 셸에도 워크스페이스 ID가 있으므로 `cd` 뒤 다른 저장소를 보려면 `--cwd`로 지정한다.
선택한 저장소를 `herdr worktree list` 로 나열한다. 그 자리가 git 저장소가 아니면 그렇게 알리고 닫는다.
팝업 호출 문맥만 있을 때 herdr에 닿지 않으면 원래 연결 오류로 종료한다. 저장소 디렉터리를 아는
직접 실행에서 herdr에 닿지 않으면 `git worktree list --porcelain` 으로 대신하되,
그때는 "herdr 에 열려 있음" 정보가 없다.

**열릴 때 스스로 원격을 본다.** 데몬은 herdr 에 열린 워크스페이스만 돌기 때문에(mfe 는 27개 중 2개)
나머지의 상태를 모른다. 먼저 목록 조회로 얻은 이름과 경로를 `pending`으로 즉시 표시한다.
검사 중에도 이동·열기는 가능하지만 `d`·`D`는 막는다. 로컬 판정은 배경에서 계산하고, 완료 후
판정 순서로 정렬하되 현재 선택된 worktree의 경로를 최초 선택부터 보존한다. 그 뒤 두 원격 명령을
나란히 돌리고 결과를 다시 반영한다.

- `git fetch --quiet --no-tags --no-prune --no-write-fetch-head <원격>` (전체 브랜치, 왕복 한 번)
- `git ls-remote --heads <원격>` (사라진 브랜치 판정, 왕복 한 번)

prune 은 하지 않는다. README 의 약속을 지키고, 사용자의 참조를 건드리지 않는다. 화면에서의 `gone` 은
`ls-remote` 결과에 브랜치가 없는 것이다. `r` 키로 다시 돌린다.

**판정 규칙.** CONTEXT.md 의 정의와 같다.

| 판정 | 조건 |
| --- | --- |
| `blocked` | 본 체크아웃이거나(`is_linked_worktree` 가 거짓), `git worktree lock` 으로 잠겼거나, 디렉터리가 사라졌거나(`is_prunable`), herdr 에서 에이전트가 일하는 중(`agent_status` 가 working) |
| `safe` | 손대지 않았고(`git status --porcelain --untracked-files=all` 이 비어 있음), 그리고 `merged` 이거나 (`gone` 이면서 앞선 커밋이 0임을 확인할 수 있음) |
| `review` | 손댄 것이 있거나, 따라잡을 때 충돌하거나, `gone` 인데 앞선 커밋이 있거나 확인할 수 없음(추적 참조가 이미 지워짐) |
| `keep` | 그 밖의 전부. 최신이거나 깨끗하게 따라잡을 수 있는 진행 중인 작업 |

정렬은 판정 순서(safe, review, keep, blocked) 다음에 브랜치 이름이다.

**키.**

| 키 | 동작 |
| --- | --- |
| `↑` `↓` `j` `k` | 이동 |
| `Enter` | herdr 에 열려 있으면 `herdr workspace focus <id>`, 아니면 `herdr worktree open --cwd <본 체크아웃> --path <경로> --focus`. 그리고 화면을 닫는다 |
| `d` | 선택한 것이 `safe` 면 확인 없이 지운다. 아니면 이유를 아래 줄에 보여 준다 |
| `D` | `safe` 전부를 "Remove N worktrees? y/N" 한 번 묻고 지운다 |
| `r` | 다시 fetch |
| `q` `Esc` | 닫기 |

**삭제.** [ADR 0002](adr/0002-removal-boundary.md). herdr 에 열려 있으면 `herdr worktree remove
--workspace <id>`, 디스크에만 있으면 `git worktree remove <경로>`. 강제 삭제는 없다. 브랜치는 남긴다.

**뒤로 미룰 것.** 디스크 사용량은 worktree 마다 디렉터리를 훑어야 해서 화면이 느려진다.
먼저 만들고 반응을 본 뒤 넣는다. `--json` 출력도 나중에 쉽게 붙는다.

**넣지 않을 것.** CI 용 리포트 출력. 이 플러그인은 사람이 보는 화면을 위한 것이고, CI 에서 낡은
worktree 를 세는 일은 herdr 와 무관한 자리에서 하는 편이 맞다.

### 4. 기준 브랜치를 고르는 생성 팝업

**왜.** herdr 기본 팝업은 브랜치 이름만 받고 기준은 언제나 `HEAD` 다. 어디서 갈라져 나오는지
사람이 고를 수 없다.

**무엇을.**

```
┌ New worktree ─────────────────────────────────── mfe ─┐
│                                                        │
│  Branch  [widget-studio/dev-2]                         │
│  Path    ~/.herdr/worktrees/mfe/widget-studio-dev-2    │
│                                                        │
│  Base  [widget-studio/dev · current                  ▾] │
│                                                        │
│  Enter 로 후보를 펼치고 ↑↓ 로 이동한 뒤 Enter 로 확정   │
│                                                        │
│  Tab switch · Enter create · Esc cancel                │
└────────────────────────────────────────────────────────┘
```

정해 둔 규칙.

- 기준 후보는 현재 브랜치, 그 upstream, 원격 기본 브랜치, 저장소에 지정된 통합 브랜치를 먼저
  모은다. 이어 로컬 브랜치와 로컬에 알려진 원격 추적 브랜치 전체를 이름순으로 표시한다.
  전체 참조로 중복을 없애며 원격 HEAD 별명과 PR/tag 참조는 후보에서 뺀다
- 팝업이 열릴 때 추천 후보의 추적 참조를 좁게 fetch 해서 "↓3 behind" / "up to date" 를 채운다.
  추가 원격 후보는 기준으로 확정할 때 fetch한다. fetch가 끝나기 전에도 입력은 받는다
- 커서는 브랜치 칸의 자동 이름 끝에서 시작한다. `←`/`→`로 이동하며 입력은 커서 위치에 삽입하고,
  Backspace는 커서 앞 글자를 지운다. 긴 이름은 현재 커서가 보이도록 표시한다.
  끝에서는 추가 공백 없이 닫는 괄호를 커서 위치로 쓰고, 포커스가 없으면 커서를 숨긴다
- 자동 이름은 기준 브랜치 이름(원격이면 `origin/` 을 뗀 것)에서 따오되 **언제나 비어 있는 이름**이다.
  겹치면 `-2`, `-3` 으로 넘어간다. 로컬 브랜치, 원격 추적 참조, 열려 있는 worktree 셋을 모두 보고
  빈 번호를 찾는다
- 기준의 기본 선택은 현재 브랜치. 뒤처져 있어도 생성 직후 자동 최신화가 앞당긴다
- 기준은 접힌 선택 필드에서 고른다. `Tab`으로 Base에 이동하고 `Enter`로 드롭다운을 열며,
  이름 일부를 입력하면 대소문자 구분 없이 후보를 검색하고 `Backspace`로 검색어를 고친다.
  `↑`/`↓`로 검색 결과를 둘러보고 `Enter`로 확정한다. 결과가 없으면 확정하지 않는다.
  `Esc`는 드롭다운부터 닫으며 닫을 때 검색어를 지운다. 이름 칸의 `Enter`는 생성한다.
  검색 결과 안의 위치·결과 개수·전체 개수를 표시한다. 접힌 필드는 이름 길이에 맞추고, 목록은
  최대 38칸 폭에 4개 후보를 보여 준다. 검색은 목록 위쪽 테두리, 개수는 아래쪽 테두리에 합치며
  강조한 후보의 종류·상태를 목록 아래에 표시한다. 긴 이름은 앞뒤를 남기고 중간을 줄인다.
  검색어가 일치하는 글자는 원래 표기를 유지하며 굵은 청록색으로 강조한다. 말줄임 전 이름에서
  범위를 구하고 보이는 부분에만 적용하며, 선택 행 표시와 함께 유지한다.
  2026-09-10~11 사용자 피드백을 반영했다
- 기준을 확정하면 이름과 경로가 따라오되, 이름을 한 글자라도 직접 고친 뒤에는 덮어쓰지 않는다
- 경고는 여러 줄로 표시한다. `F1`로 전체 메시지를 열어 `↑`/`↓`로 읽고 `Enter`/`Esc`/`F1`로
  입력 화면에 돌아온다. 메시지 화면의 Enter는 생성하지 않는다
- 경로 미리보기는 `<[worktrees] directory>/<저장소 이름>/<슬러그>` 다. 뿌리는 config.toml 을
  `internal/herdrconfig` 로 훑어 읽고, 없으면 `~/.herdr/worktrees`. 슬러그는 herdr 의
  `branch_to_path_slug` 규칙(영숫자는 소문자로, 나머지는 대시 하나로, 앞뒤 대시 제거)을 그대로 옮긴다
- 새 브랜치인지 기존 브랜치인지는 표시하지 않는다. herdr 가 이름을 보고 알아서 가른다
- UI 문구는 영문

**어떻게.** `herdr worktree create --branch <입력> --base <선택> --focus` 를 부른다. `--path` 는 비워
herdr 의 자리 규칙을 그대로 쓴다.

**원격 추적 참조를 기준으로 했으면 upstream 을 푼다.** git 은 그 경우 새 브랜치의 upstream 을 기준
원격 브랜치로 자동 설정하는데, 그대로 두면 `$behind` 가 main 대비 숫자를 보이고 `git pull` 이 main 을
기능 브랜치에 병합한다. 생성 직후 그 worktree 에서 `git branch --unset-upstream` 을 한다. 처음 push 할
때까지 토큰이 비는 것은 HEAD 에서 만들었을 때와 같은 동작이다.

**옵트인이다.** 액션 `git-upstream.new-worktree` 로만 노출하고 기본 키를 잡지 않는다. `setup` 은
주석 처리된 키 설정을 덧붙여 보여 준다.

## 순서와 이유

```
0. setup 명령            → 작다. 먼저 넣으면 나머지를 시험하기 편하다
1. 병합·삭제 판정        → 3번의 재료
2. 충돌 미리 보기        → 3번의 재료이자 그 자체로 값어치가 큼
3. worktree 화면         → 1,2 의 결과를 표로 그림. 터미널 화면 기반을 여기서 만듦
4. 생성 팝업             → 3 에서 만든 화면 기반을 재사용
```

3번과 4번이 같은 터미널 화면 코드를 쓴다. 한 번 만들어 두면 두 번째는 값이 싸다.

**herdr 에는 다 만든 뒤에 붙인다.** 항목마다 커밋을 따로 두되, `herdr plugin link` 와 실측은 0~4 를
모두 마친 뒤 한 번에 한다. 삭제처럼 되돌릴 수 없는 동작은 실제 저장소가 아니라 임시 저장소에서만
시험한다.

## 터미널 화면 기반 (3번에서 구현)

의존성 없이 간다. raw 모드는 플랫폼마다 다르지만 표준 라이브러리로 닿는다.

- 유닉스: `syscall.Termios` 와 `TCGETS`/`TIOCGETA`
- 윈도우: `GetConsoleMode`/`SetConsoleMode`

`internal/tui` 에 두고, 화면은 `internal/worktreeui` 에 둔다. 그리기 로직은 플랫폼과 무관한 순수
함수(모델 → 줄 목록)로 두어 시험한다. 윈도우 구현은 같은 변경에 넣되 교차 컴파일까지만 확인하고,
실측하지 못했다는 사실을 README 에 적는다. 매니페스트의 `[[panes]]` 와 화면 액션은 세 플랫폼 모두에
선언한다(file-viewer 가 0.7.1 시절 윈도우에서 상대 경로 pane 명령이 실패한다고 적어 두었는데,
0.9.0 에서도 그런지는 사용자 보고로 안다).

## 문서 마무리

- 영문 `README.md` 를 기본으로 두고, 한국어 `README.ko.md` 와 서로 링크한다.
- 실행 파일과 매니페스트의 판 번호는 0.2.0 이다.
- HERDR.md 에 herdr 0.9.0 의 CLI 통로, 오류 봉투, 기본 키와 worktree 뿌리 설정을 기록했다.

## 열린 질문

모두 정리되었다. 정리 과정은 CONTEXT.md 와 adr/ 에 남아 있다.
