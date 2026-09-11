# herdr-git-upstream

[English](README.md) | **한국어**

**pull 받아야 하는 저장소를 herdr 사이드바에서 바로 알아본다.** Linux, macOS, Windows를 대상으로 한다.
Windows는 교차 컴파일만 확인했으며, 실제 콘솔과 팝업 실행은 검증하지 않았다.

```
● mfe                 widget-studio/dev   ↓3
  · design-qa-3       fix/modal           ↓3 ↑1 conflict
  · add-widget-api    feat/widget-api     gone merged
● msa                 widget-studio/dev
```

## 왜 필요한가

herdr는 사이드바에 앞뒤 커밋 수를 그린다. 그런데 그 숫자는 `HEAD`와 **로컬에 저장된** 원격 추적
참조를 비교한 값이고, herdr는 그 참조를 스스로 갱신하지 않는다. 유지보수자도 이를 명시했다.

> herdr performs small, cached reads in a background thread and never runs `git fetch`.
> ([herdr#1253](https://github.com/herdrdev/herdr/issues/1253))

그래서 동료가 방금 올린 커밋이 있어도, 누군가 그 저장소에서 `git fetch`를 하기 전까지는 사이드바에
아무 변화가 없다. 이 플러그인이 그 fetch를 맡는다.

## 무엇을 하는가

1. herdr에 열려 있는 저장소들의 원격 추적 참조를 주기적으로 갱신한다. 이것만으로 herdr의 내장
   `git_status` 토큰이 실제 원격 상태를 가리키게 된다.
2. 워크스페이스마다 `$behind`와 `$ahead` 토큰을 보고하고, 그 브랜치가 원격에서 끝난 작업인지
   (`$gone`, `$merged`)와 따라잡을 때 충돌하는지(`$catchup`)를 함께 보고한다.
3. 새로 만든 worktree를 원격의 최신 상태로 맞춘다.
4. 한 저장소의 worktree 전부를 판정과 함께 표로 보이고, 끝난 것을 지우는 [worktree 화면](#worktree-화면)을 연다.
5. [생성 팝업](#생성-팝업)에서 최신 원격 후보를 비교하고 새 worktree의 기준 브랜치를 고른다.

두 번째가 필요한 이유가 있다. herdr는 같은 저장소를 공유하는 워크스페이스들을 묶어서 들여쓰는데,
**들여쓴 행에서는 내장 `branch`와 `git_status` 토큰을 지운다.** worktree를 여러 개 열어 두고 쓰는
사람에게는 오히려 그 행이 더 중요하다. 커스텀 토큰은 그 행에서도 그려지므로, 토큰을 쓰면 어느
행에서나 같은 정보가 보인다.

세 번째는 다른 문제를 푼다. herdr는 worktree를 만들 때 fetch를 하지 않고, 기준 커밋으로 원본
체크아웃의 `HEAD`를 그대로 쓴다.

```rust
// herdr src/app/api/worktrees/deferred.rs:118
let base = params.base.unwrap_or_else(|| "HEAD".into());
```

그래서 손에 쥔 main이 원격보다 다섯 커밋 뒤처져 있으면, 방금 만든 worktree도 다섯 커밋 뒤처진
자리에서 시작한다. 새 작업을 낡은 바닥 위에 쌓는 셈이라 나중에 병합할 때 값을 치른다. 참조를
부지런히 갱신하는 것만으로는 풀리지 않는다. fetch는 `refs/remotes/*`를 움직일 뿐 로컬 브랜치의
`HEAD`는 그대로 두기 때문이다. 그래서 만들어진 직후에 한 번 앞으로 감아 준다.

## 설치

```sh
herdr plugin install <owner>/herdr-git-upstream
```

로컬에서 개발 중이라면 이렇게 붙인다.

```sh
sh scripts/build.sh
herdr plugin link /Users/ukyi/personal/herdr-git-upstream
```

`plugin link`는 빌드 훅을 실행하지 않는다. 저장소 뿌리에서 먼저 빌드한다. Windows에서는 셸 스크립트 대신
`powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build.ps1`을 실행한다.

herdr 0.7.5 이상, Go 1.24 이상, Git 2.31 이상이 필요하다. 설치할 때 herdr가 `go build`를 한 번 돌린다.
표준 라이브러리만 쓰므로 내려받을 의존성은 없다.

## 사이드바 설정

**이 설정을 넣기 전에는 아무것도 보이지 않는다.** herdr는 사이드바 설정이 요청한 토큰만 그린다.

붙여 넣을 내용은 `setup` 명령이 대신 만들어 준다. `config.toml`을 훑어 이미 들어 있는 것은 빼고
남은 것만 보여 주며, 토큰 이름을 바꿨다면 바꾼 이름으로 만든다. **파일은 고치지 않는다.** 출력을
붙여 넣는 것은 사람이 한다. 사이드바 토큰과 worktrees 키가 모두 들어 있으면 "설정이 모두 들어
있습니다."라고만 답한다. 생성 팝업 키는 옵트인이라, 빠져 있어도 모두 들어 있는 것으로 본다.

```sh
./bin/herdr-git-upstream setup
```

손으로 넣는다면 `~/.config/herdr/config.toml`에 다음을 넣는다.

```toml
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
```

```sh
herdr config check && herdr server reload-config
```

worktree를 쓰지 않는다면 내장 `git_status`만으로도 충분하다. 그때는 토큰 없이 이렇게 두고,
이 플러그인은 fetch만 맡게 하면 된다.

```toml
[ui.sidebar.spaces]
rows = [
  ["state_icon", "workspace"],
  ["branch", "git_status"],
]
```

## 토큰

| 토큰 | 값 | 뜻 |
|---|---|---|
| `$behind` | `↓3` | 원격에 내가 아직 받지 않은 커밋이 3개 있다 |
| `$ahead` | `↑1` | 내가 아직 올리지 않은 커밋이 1개 있다 |
| `$gone` | `gone` | upstream 브랜치가 원격에서 사라졌다. 대개 병합된 뒤 지워진 것이다 |
| `$merged` | `merged` | HEAD의 내용이 이미 어느 원격 브랜치에 들어가 있다 |
| `$catchup` | `conflict` | 뒤처진 커밋을 따라잡으면 충돌한다. **충돌할 때만** 값이 있고, 깨끗하거나 뒤처지지 않았으면 빈 값이다 |
| `$sync_stale` | `stale` | fetch가 오래 실패하고 있어 위 숫자를 믿을 수 없다 |

값이 0이거나 해당 사항이 없으면 토큰이 비고, herdr가 그 자리를 지운다. 저장소가 아니거나 커밋이
하나도 없으면 아무것도 표시하지 않는다. HEAD가 분리되어 있거나 upstream이 없는 브랜치(아직 push 하지
않은 새 브랜치)에서는 `$merged`만 판정한다. 방금 만든 빈 브랜치도 통합 브랜치의 조상이면 `merged`다.

## 설정

`herdr plugin config-dir git-upstream`가 알려 주는 디렉터리에 `config.json`을 둔다. 파일이 없으면
모든 값이 기본값으로 동작한다.

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

| 항목 | 기본값 | 설명 |
|---|---|---|
| `interval_seconds` | 60 | 전체 워크스페이스를 한 바퀴 도는 간격 (5초 ~ 24시간) |
| `throttle_seconds` | 120 | 같은 저장소를 다시 가져오기까지의 최소 간격 |
| `fetch_timeout_seconds` | 20 | 저장소 하나당 fetch 제한 시간 |
| `stale_after_seconds` | 900 | 실패가 이만큼 이어지면 `stale`을 띄운다 |
| `gone_token` | `gone` | upstream이 원격에서 사라졌을 때 보고할 토큰 이름 |
| `gone_label` | `gone` | 그때 채우는 값 |
| `merged_token` | `merged` | HEAD가 이미 어느 원격 브랜치에 들어가 있을 때 보고할 토큰 이름 |
| `merged_label` | `merged` | 그때 채우는 값 |
| `catchup_token` | `catchup` | 따라잡을 때 충돌하는지 알릴 토큰 이름 |
| `catchup_conflict_label` | `conflict` | 충돌할 때만 채우는 값. 깨끗하면 빈 값이다 |
| `fresh_worktrees` | true | 새로 만든 worktree를 원격의 최신 상태로 맞춘다 |
| `enabled` | true | false로 두면 이 플러그인이 올린 토큰을 지우고 쉰다 |

토큰 이름을 빈 문자열로 두면 그 토큰은 보고하지 않는다. 설정은 매 회차마다 다시 읽으므로
herdr를 재시작하지 않아도 주기를 바꿀 수 있다.

## worktree 화면

worktree를 열 개 넘게 열어 두면 어느 것이 끝난 작업인지 사람이 기억하지 못한다. 사이드바 토큰은 한 줄에
조금씩만 보여 줄 수 있어서, 한눈에 견주려면 화면이 필요하다. 한 저장소의 worktree 전부를 판정과 함께
표로 보이고, `safe`인 것만 지운다.

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

**여는 길은 셋이다.**

| 통로 | 쓰임새 |
| --- | --- |
| 키 설정 (`type = "plugin_action"`, `git-upstream.worktrees`) | 평소 사용. `setup`이 이 설정을 내놓는다 |
| `herdr plugin action invoke worktrees --plugin git-upstream` | 스크립트나 시험 |
| `herdr-git-upstream worktrees [--cwd <path>]` | 터미널에서 직접. 데몬 없이도, herdr 없이도 돈다 |

앞의 둘은 herdr 팝업 pane으로 열리고, 마지막은 지금 있는 페인에 그대로 그린다. herdr에는 액션을 골라
실행하는 화면이 없으므로 **키에 묶기 전에는 없는 것과 같다.** 두 화면 모두 명시한 `--cwd`,
`HERDR_PLUGIN_CONTEXT_JSON`의 워크스페이스, `HERDR_WORKSPACE_ID`, 현재 디렉터리 순으로
저장소를 고른다. 팝업의 작업 디렉터리는 플러그인 저장소이므로 호출 문맥에서 실제 저장소를 찾는다.
호출 문맥이 있지만 형식이 잘못되었거나 워크스페이스가 빠졌으면 오류로 종료한다.
herdr 안의 셸에도 워크스페이스 ID가 있으므로 `cd` 뒤 다른 저장소를 보려면 `--cwd <경로>`를 준다. 그 자리가 git
저장소가 아니면 오류를 알리고 종료 코드 1로 끝난다. 팝업은 워크스페이스 조회에 herdr가 필요하며
연결이 실패하면 오류로 종료한다. 저장소 디렉터리를 아는 직접 실행 경로에서 herdr에 닿지 않으면
`git worktree list`로 대신하되 제목 줄에 `herdr unavailable`이 보이고, 그때는 "herdr에 열려 있음" 정보가
없어 Enter로 옮겨 갈 수 없다.

**열릴 때 스스로 원격을 본다.** 데몬은 herdr에 열린 워크스페이스만 돌기 때문에 나머지 worktree의 상태를
모른다. 이름 목록을 즉시 `pending`으로 표시하고 로컬 상태를 배경에서 검사한다. 검사 중에도
이동·열기는 가능하며 삭제는 막는다. 최초 표시 때의 선택을 포함해, 판정으로 목록이 재정렬되어도
현재 선택된 worktree는 유지된다. 로컬 검사를 마치면 `git fetch`(전체 브랜치, 왕복 한 번)와
`git ls-remote --heads`(사라진 브랜치 판정, 왕복 한 번)를 나란히 돌린 뒤 다시 그린다. **prune은 하지
않는다.** 사용자의 참조를 건드리지 않는다는 약속은 여기서도 같다. 화면의 `gone`은 `ls-remote` 결과에
그 브랜치가 없는 것이고, 그 결과가 없을 때만 데몬의 fetch 기록으로 판정한다. fetch가 실패하면 제목 줄에
`fetch failed`로만 보이고 표는 로컬 자료로 남는다. `r`로 다시 돌린다.

**판정.**

| 판정 | 조건 |
| --- | --- |
| `blocked` | 본 체크아웃이거나, `git worktree lock`으로 잠겼거나, 디렉터리가 사라졌거나, herdr에서 에이전트가 일하는 중. 지울 수 없다 |
| `safe` | 손대지 않았고(추적되지 않은 파일까지 없음), `merged`이거나 (`gone`이면서 앞선 커밋이 0임을 확인할 수 있음). 끝난 일이다 |
| `review` | 손댄 것이 있거나, 따라잡을 때 충돌하거나, `gone`인데 앞선 커밋이 있거나 확인할 수 없음(추적 참조가 이미 지워짐) |
| `keep` | 그 밖의 전부. 최신이거나 깨끗하게 따라잡을 수 있는 진행 중인 작업 |

정렬은 판정 순서(safe, review, keep, blocked) 다음 브랜치 이름이다. 설명 열은 사이드바 토큰과 같은
판정 함수가 낸 조각들이라 두 자리가 다른 답을 내지 않는다.

**키.**

| 키 | 동작 |
| --- | --- |
| `↑` `↓` `j` `k` | 이동 |
| `Enter` | herdr에 열려 있으면 그 워크스페이스로, 아니면 `herdr worktree open --cwd <본 체크아웃> --path <경로> --focus`. 그리고 화면을 닫는다 |
| `d` | 선택한 것이 `safe`면 확인 없이 지운다. 아니면 이유를 아래 줄에 보여 준다 |
| `D` | `safe` 전부를 "Remove N worktrees? y/N" 한 번 묻고 지운다 |
| `r` | 다시 fetch |
| `q` `Esc` `Ctrl-C` | 닫기 |

**삭제 규칙.** `safe`인 것만 지운다. herdr에 열려 있으면 `herdr worktree remove --workspace <id>`,
디스크에만 있으면 `git worktree remove <경로>`다. **강제 삭제는 없다.** 삭제 직전에 대상과 HEAD,
안전 판정을 다시 확인하고, 바뀌었으면 거절한다. 파일 변경은 herdr와 git도 마지막 문턱에서 확인한다.
`D`는 확인창을 열 때의 대상만 지우며, 배경 갱신이 대상을 늘리지 않는다. 삭제 도중 닫기를 요청하면
진행 중인 삭제가 끝난 뒤 닫는다. 에이전트 상태를 조회하지 못한 열린 worktree도 삭제하지 않는다.
브랜치는 남긴다. 확인 물음은 `D`에만 둔다. 근거는
[ADR 0002](docs/adr/0002-removal-boundary.md)에 있다.

윈도우에서는 교차 컴파일까지만 확인했다. 콘솔 raw 모드와 팝업 pane 의 상대 경로 명령은 실측하지 못했다.

## 생성 팝업

`herdr-git-upstream new-worktree [--cwd <path>]`는 새 worktree의 기준 브랜치를 고르는 화면이다.
herdr 기본 팝업은 현재 HEAD에서 시작하지만, 이 팝업에서는 현재 브랜치, 그 upstream, 원격 기본
브랜치, 저장소에 설정한 통합 브랜치를 먼저 보여 준다. 그 뒤 로컬 브랜치와 로컬에 알려진 원격
추적 브랜치 전체를 이름순으로 보여 준다. 같은 참조는 한 번만 나오며 원격 HEAD 별명과 PR/tag
참조는 제외한다. 목록에는 현재 위치와 전체 개수가 표시된다.
`Base`는 접힌 선택 필드다. `Tab`으로 이동하고 `Enter`로 드롭다운을 연 뒤, `↑`/`↓`와 `Enter`로
기준을 확정한다. `Tab`으로 이름 칸에 돌아와 `Enter`를 누르면 생성한다. 후보를 둘러보는 동안에는
확정한 기준과 자동 이름이 바뀌지 않는다.

`herdr plugin action invoke new-worktree --plugin git-upstream`으로 herdr pane에 열 수 있다.
생성에는 실행 중인 herdr가 필요하다.

키로 사용하려면 `setup` 출력의 `git-upstream.new-worktree` 주석 블록이나 아래 블록을 활성화한다.
필수 사이드바 토큰과 worktrees 키가 이미 설정되어 있으면 `setup`은 이 선택 항목을 생략한다.
기존 `[keys]` 테이블에서 `new_worktree = ""`로 바꿔 herdr 내장 생성 팝업의 `prefix+shift+g` 키를 비운다.
`[keys]` 헤더를 중복해서 추가하지 말고 기존 테이블을 수정한다. 그런 다음 아래 블록의 주석을 풀어
그 테이블 뒤에 추가한다. 플러그인이 설정 파일이나 키를 대신 바꾸지는 않는다.

```toml
# [[keys.command]]
# key = "prefix+shift+g"
# type = "plugin_action"
# command = "git-upstream.new-worktree"
# description = "git upstream: new worktree"
```

이름은 기준 브랜치에서 가져오되 로컬 브랜치, 원격 추적 브랜치, worktree에서 쓰는 이름을 피한다.
이미 사용 중이면 `-2`, `-3` 순으로 찾는다. `feature-2` 다음 후보는 `feature-3`이다.
커서는 자동 이름 끝에 놓인다. 입력하면 뒤에 붙고, `Backspace`는 마지막 글자를 하나씩 지운다.
긴 이름은 끝과 커서가 보이도록 표시한다. 이름을 직접 고친 뒤에는
기준을 바꿔도 입력을 덮어쓰지 않는다.

| 키 | 동작 |
| --- | --- |
| `Tab` | 이름 칸과 기준 필드 전환. 열린 목록의 임시 선택은 취소 |
| `↑` `↓` | 열린 기준 드롭다운에서 강조 이동 |
| 문자·`Backspace` | 이름 입력·수정 |
| `Enter` | 이름 칸: 생성. 기준 필드: 드롭다운 열기. 열린 드롭다운: 기준 확정 |
| `Esc` | 드롭다운이 열렸으면 먼저 닫기, 닫혔으면 팝업 취소 |
| `Ctrl-C` | 팝업 취소 |

추천 원격 후보는 화면을 연 뒤 좁게 fetch한다. 나머지 원격 브랜치는 기준으로 확정할 때 fetch하므로
목록을 여는 것만으로 전체 브랜치를 가져오지는 않는다. 기다리는 동안에도 이름과 기준을 고를 수 있다.
선택한 원격 후보를 가져오기 전에는 생성하지 않는다. fetch가 실패했다면 팝업을 다시 열어 재시도한다.
경로는 `[worktrees] directory` 아래의 저장소 이름과 herdr 슬러그 규칙으로 미리 보여 준다.
실제 위치는 `--path`를 지정하지 않고 herdr가 결정한다.

원격 추적 참조에서 **새 브랜치**를 만들었으면 자동으로 설정된 upstream을 해제한다. 그렇지 않으면
`git pull`과 사이드바 숫자가 기능 브랜치 대신 main 같은 기준 브랜치를 계속 따라가기 때문이다.
첫 push에서 upstream을 설정하면 토큰이 다시 나타난다. 기존 브랜치 이름을 직접 입력한 경우에는
herdr가 그 브랜치를 열고, 기존 upstream 설정을 보존한다. 현재 브랜치 이외에 명시적으로 고른 기준과 기존
브랜치는 자동 최신화 훅으로 옮기지 않는다. 이전에 완료된 생성의 이름을 새 현재 브랜치 기준으로 다시 쓰면
일반 자동 최신화 흐름을 따른다.

## 동작 방식

```
herdr startup 훅 ─┐
                  ├─► 데몬 (herdr 바깥에서 계속 돎)
포커스 이벤트 ────┘        │
  쪽지만 남기고 즉시 끝남   ├─ 저장소마다 좁은 fetch (현재 브랜치와 통합 브랜치)
                           ├─ 워크스페이스마다 gone / merged / catchup 판정
                           └─ 워크스페이스마다 토큰 보고
```

몇 가지 결정에는 이유가 있다.

**이벤트 훅은 fetch를 하지 않는다.** herdr는 동시에 도는 플러그인 명령을 32개로 제한한다. 포커스는
자주 바뀌고 fetch는 느리다. 훅이 그 자리를 수십 초씩 차지하면 탭 이름을 바꾸는 것 같은 다른
플러그인의 훅까지 밀린다. 그래서 훅은 파일 하나를 쓰고 곧바로 끝나며, 실제 작업은 herdr 바깥의
데몬이 맡는다.

**포커스 이벤트가 데몬을 되살린다.** herdr의 startup 훅은 서버가 세션을 복구할 때만 발화하고,
사용자가 플러그인을 방금 link하거나 enable했을 때는 발화하지 않는다. 포커스 이벤트가 데몬 생존을
확인하도록 해 두어, 설치 직후 herdr를 재시작하지 않아도 곧 동작하기 시작한다.

**fetch는 좁게 하고, 사용자의 작업을 건드리지 않는다.** 참조를 하나씩 좁게 가져오고(현재 브랜치의
upstream과 통합 브랜치 각각), 태그와 prune과 하위 모듈은 손대지 않으며 자동 정리(`gc.auto`)도 꺼 둔다. `--no-write-fetch-head`로
`FETCH_HEAD`를 다시 쓰지 않는데, 이것이 없으면 사용자가 손으로 fetch한 뒤 `git merge FETCH_HEAD`를
하려던 참에 엉뚱한 커밋을 병합하게 된다. `GIT_OPTIONAL_LOCKS=0`으로 부가적인 잠금도 잡지 않는다.

**ssh 설정은 사용자 것을 그대로 둔다.** 전용 키나 ProxyCommand를 쓰려고 `GIT_SSH_COMMAND`나
`core.sshCommand`를 지정해 둔 사람의 설정을 덮으면 fetch 자체가 실패한다. 둘 다 없을 때만 비대화형
ssh를 지정하며, 그 경우에도 멈춤은 제한 시간이 걷어 낸다.

**원격과 추적 참조는 짐작하지 않고 git에게 묻는다.** 원격 이름은 `branch.<이름>.remote`에서 읽고,
이름에 `/`가 들어갈 수 있으므로 모양으로 URL 여부를 단정하지 않고 등록 여부를 확인한다.
`.`으로 적힌 로컬 추적 브랜치는 가져올 원격이 없으므로 건너뛴다. 추적 참조의 이름은 git에게 먼저 묻되,
아직 한 번도 가져온 적 없는 브랜치에서는 git이 답하지 못하므로 원격에 설정된 fetch 참조 사양을 보고
계산한다. 바로 그 브랜치가 이 플러그인이 도와야 할 자리이기 때문이다.

**한 저장소를 두 번 가져오지 않는다.** 연결된 worktree들은 참조 저장소를 공유하므로, 같은 브랜치를
보고 있는 워크스페이스들은 한 번의 fetch로 함께 최신이 된다. 서로 다른 브랜치라면 각각 가져온다.

**토큰에는 수명을 둔다.** 데몬이 죽으면 값이 저절로 사라진다. 사이드바에 낡은 숫자가 붙박이로
남는 것이, 아무것도 없는 것보다 나쁘기 때문이다.

**잠금 갱신은 일하는 흐름과 떼어 놓는다.** 한 회차의 길이에는 상한이 없다. 응답 없는 원격 하나가
제한 시간만큼 붙잡고 워크스페이스가 많으면 그것이 쌓인다. 갱신을 일하는 흐름 안에서 찍으면 그동안
잠금이 낡아, 멀쩡히 일하는 데몬이 죽은 것으로 몰리고 다른 데몬이 잠금을 빼앗아 둘이 함께 돌게 된다.

**데몬은 herdr 서버마다 하나씩 둔다.** herdr는 세션마다 서버를 따로 두지만 플러그인 상태 디렉터리는
하나다. 잠금을 나눠 쓰면 먼저 뜬 데몬이 다른 세션의 데몬까지 막아, 그 세션에는 영영 토큰이 오지 않는다.

**디렉터리는 herdr와 같은 규칙으로 찾는다.** herdr가 띄운 명령에는 위치가 환경변수로 들어오지만
일반 셸에서 직접 부를 때는 플러그인 전용 경로 환경변수가 없을 수 있다. 그때 다른 자리를 보면 설정이 통째로 무시되고, 잠금이 갈려 데몬이 둘 뜬다.

**새 worktree는 빨리 감기로만 옮긴다.** 기준이 된 브랜치를 찾아 그것만 가져온 뒤 `merge --ff-only`로
앞당긴다. 빨리 감기는 정의상 잃을 것이 없는 이동이므로, 사용자가 만든 것을 버릴 수 없다. 작업 트리가
깨끗하지 않거나, 새 브랜치에 이미 커밋이 있거나, 기준으로 삼을 브랜치가 둘 이상이어서 모호하면
아무것도 하지 않는다. 짐작해서 옮기느니 그대로 두는 편이 낫기 때문이다.

**첫 실패로는 경고하지 않는다.** 잠시 끊긴 네트워크나 아직 연결하지 않은 VPN 때문에 곧바로
`stale`이 뜨면, 정작 사람이 손봐야 하는 상황과 구별되지 않는다. 실패가 이어진 시간을 기준으로 삼는다.

**`gone`은 fetch 실패에서 읽는다.** 현재 브랜치의 upstream을 참조 하나로 좁게 가져오므로, 원격에서
그 브랜치가 지워지면 fetch가 "원격 참조 없음"으로 실패한다. 그 기록이 곧 `gone`이다. `ls-remote`도
prune도 부르지 않으므로 값이 0이고, 사용자의 참조를 지우지도 않는다. 이 실패는 다시 시도해도 같으므로
`stale`로 세지 않는다. 근거를 더 요구하지 않으므로 원격에서 지워지고 로컬에서 prune까지 된 뒤에 처음
본 브랜치도 `gone`이다. 커밋이 하나도 없는 저장소만 뺀다. 빈 원격을 clone한 직후에도 git이
`branch.main`을 잡아 두어 같은 실패가 나지만, 태어나지 않은 브랜치는 사라질 수 없기 때문이다.

**`merged`는 조상 검사와 merge-tree 비교로 판정한다.** 먼저 `git for-each-ref --contains HEAD
refs/remotes/`로 자기 참조가 아닌 원격 브랜치가 HEAD를 품고 있는지 본다. 자기 참조란 원격마다 있는
이 브랜치의 사본(`refs/remotes/<원격>/<브랜치>`)과 upstream 추적 참조다. push만 해도 사본은 HEAD를
품으므로 그것은 근거가 아니고, `refs/pull/*/head`처럼 브랜치가 아닌 것을 가져오는 참조 사양의
목적지도 세지 않는다. 로컬 명령 하나라 참조가 수백 개여도 값이 싸다. 그것으로 잡히지 않으면 통합
브랜치마다 `git merge-tree --write-tree <통합> HEAD`를 돌려, 결과 트리가 통합 브랜치의 트리와
같으면(병합해도 아무것도 바뀌지 않으면) `merged`로 본다. 이 비교가 squash 병합과 rebase 병합을
잡는다. 근거는 [ADR 0001](docs/adr/0001-merged-judgement.md)에 있다.

통합 브랜치는 원격 기본 브랜치에 저장소별로 지정한 것을 더한 목록이다. 원격 기본 브랜치는
`refs/remotes/<원격>/HEAD`가 실제 참조를 가리키면 그것이고(원격이 기본 브랜치를 바꾼 뒤 남은 낡은
별명은 믿지 않는다), 없으면 fetch와 같은 자리에서 `ls-remote --symref`로 하루에 한 번 물어 기록하고
그 회차부터 쓴다. 그것마저 실패해 끝내 모르면 `merged`를 판정하지 않는다. 통합 브랜치 자체를
체크아웃한 자리를 가려낼 수 없어, 거기서 갈라져 나간 원격 브랜치 하나만 있어도 조상 검사가 `merged`를
붙이기 때문이다. 작업이 `origin/main`이 아니라 `origin/develop` 같은 곳으로
병합되는 저장소에서는 그 저장소에서 이렇게 알려 준다. 값은 여러 개 둘 수 있고, 연결된 worktree
전부가 함께 쓴다.

```sh
git config --add git-upstream.mergeTarget origin/develop
```

**`merged`는 병합 완료를 보증하지 않는다.** 병합 뒤 통합 브랜치가 같은 파일을 다시 고쳤으면
merge-tree 비교로는 잡히지 않는다. 그래서 끝난 작업의 주된 신호는 `gone`이고, `merged`는 그것을
보태는 신호다. 자기 참조는 근거로 삼지 않으므로 push만 한 브랜치가 그 이유만으로 `merged`가 되지는
않고, 통합 브랜치 자체를 체크아웃한 자리(main 위의 main)는 아예 판정하지 않는다. 다만 조상 검사는
"HEAD에서 갈라져 나간 브랜치"와 "HEAD가 병합된 브랜치"를 구별하지 못한다. 내 브랜치에서 남이
갈라져 나가 push하면 그 참조가 HEAD를 품으므로 내 브랜치에 `merged`가 붙는다.

**`catchup`은 병합해 보되 작업 트리는 건드리지 않는다.** `git merge-tree --write-tree <추적 참조>
HEAD`의 종료 코드로 충돌 여부를 안다. 뒤처짐이 0보다 클 때만 계산하고, 결과를 (HEAD, 추적 참조 커밋)
쌍과 함께 브랜치마다의 따라잡기 기록에 남겨 쌍이 같으면 다시 계산하지 않는다. 같은 `origin/main`을
따라가는 브랜치가 여럿이어도 서로의 캐시를 지우지 않고, fetch 기록과 다른 파일이라 판정이 다른
세션의 데몬이 남긴 fetch 결과를 덮지도 않는다. 판정하지 못한 쌍(관계없는 역사, 제한 시간 초과)도
남겨 한 시간 안에는 다시 시도하지 않는다. `merge-tree --write-tree`는 git 2.38에서 생겼으므로 그
아래에서는 이 토큰만 조용히 쉬고, `merged`는 조상 검사만 한다. `status`의 `merge_tree_supported`가
그 사실을 알려 준다.

**통합 브랜치도 함께 가져온다.** 통합 브랜치의 추적 참조가 낡으면 `merged` 판정도 낡는다. 그래서
저장소마다 통합 브랜치 각각을 별도의 fetch 작업으로 두되, 현재 브랜치가 곧 통합 브랜치면 한 번만
가져간다. 스로틀은 다른 fetch와 같은 규칙을 따른다.

## 명령

```sh
herdr plugin action invoke worktrees --plugin git-upstream # worktree 화면을 팝업 pane 으로 연다
herdr plugin action invoke refresh   --plugin git-upstream # 스로틀을 무시하고 지금 갱신
herdr plugin action invoke start     --plugin git-upstream # 데몬 시작
herdr plugin action invoke stop      --plugin git-upstream # 데몬 중지
```

실행 파일을 직접 부를 수도 있다.

```sh
./bin/herdr-git-upstream setup                 # config.toml 에 붙여 넣을 설정을 출력 (파일은 고치지 않는다)
./bin/herdr-git-upstream status                # 데몬과 설정 상태를 JSON 으로 출력
./bin/herdr-git-upstream refresh
./bin/herdr-git-upstream worktrees             # worktree 화면을 지금 페인에 그린다 (--cwd <path> 로 저장소를 고른다)
./bin/herdr-git-upstream open-worktrees        # worktree 화면을 herdr 팝업 pane 으로 연다 (액션이 부르는 명령)
./bin/herdr-git-upstream new-worktree          # 기준 브랜치를 고르는 생성 팝업 (--cwd <path> 가능)
./bin/herdr-git-upstream open-new-worktree     # 생성 팝업을 herdr pane 으로 연다
```

`worktrees`와 `new-worktree`는 표준 입출력이 터미널이어야 한다. 파이프 뒤에서는 종료 코드 2로 끝난다.

## 문제를 살펴볼 때

`status`가 알려 주는 로그 파일을 먼저 본다.

```sh
./bin/herdr-git-upstream status
tail -f "$(./bin/herdr-git-upstream status | sed -n 's/.*"log_path": "\(.*\)".*/\1/p')"
```

상세 로그가 필요하면 `HERDR_GIT_UPSTREAM_DEBUG=1`을 준 채로 데몬을 다시 띄운다. `merged`가 붙은
이유(조상 검사인지, 어느 통합 브랜치와의 merge-tree 비교인지)도 이 수준에서 보인다.

`catchup`이 한 번도 뜨지 않으면 `status`의 `git_version`과 `merge_tree_supported`를 본다.
git 2.38 미만에서는 이 토큰이 쉰다.

아무것도 보이지 않는다면 대개 둘 중 하나다. 사이드바 `rows`에 토큰을 넣지 않았거나,
사이드바를 접어 두었거나(접힌 상태에서는 herdr가 번호와 상태 점만 그린다). 앞의 경우는 `status`의
`sidebar_configured`가 false로 나오며, `setup`이 붙여 넣을 것을 만들어 준다.

## 알아둘 점

- 주기적인 원격 갱신은 fetch만 한다. 갓 만든 worktree는 조건이 맞으면 빨리 감기로 앞당기며,
  `fresh_worktrees`를 false로 두면 자동 최신화를 하지 않는다. 별도로 사용자가 생성 팝업이나
  worktree 화면에서 요청하면 worktree를 만들거나 지운다. 강제 삭제는 하지 않고 브랜치는 남긴다.
- Git 2.31 이상이 필요하다. fetch에서 쓰는 `--no-write-fetch-head`는
  [Git 2.29](https://github.com/git/git/blob/v2.29.0/Documentation/RelNotes/2.29.0.txt#L43-L44)에 생겼고,
  worktree 화면이 읽는 porcelain 출력의 `locked`·`prunable` 필드는
  [Git 2.31](https://github.com/git/git/blob/v2.31.0/Documentation/RelNotes/2.31.0.txt#L56-L58)에 추가됐다.
  merge-tree 비교와 `catchup`은 Git 2.38 이상에서 동작한다.
  지원 범위 안에서 그보다 낮은 버전은 `merged`의 조상 검사만 하고 `catchup`은 생략한다.
  실행 검증에는 Git 2.55.0을 썼으며, Git 2.31 자체에서 실행해 보지는 않았다.
- `merge-tree --write-tree`는 결과 트리 객체를 객체 저장소에 남긴다. 다만 같은 두 커밋을 다시
  병합하면 같은 트리가 나와 새로 쓰이지 않고, `catchup`은 (HEAD, 추적 참조 커밋) 쌍이 같으면 아예
  부르지 않으므로 반복 실행으로 저장소가 자라지는 않는다. 남는 객체는 어디에서도 참조되지 않아
  `git gc`가 알아서 거둔다. 자동 정리(`gc.auto`)는 이 명령에서도 꺼 둔다.
- 사이드바에 아무것도 뜨지 않으면 `status`의 `invalid_token_names`부터 본다. herdr는 한 요청의
  토큰 이름을 통째로 검사하므로, 이름 하나가 규칙(`[A-Za-z0-9_-]`, 32자 이하)에 어긋나면
  그 요청 전체가 거절된다. 이 플러그인은 어긋난 이름을 미리 걸러 내고 거기에 적어 둔다.
- 사용자가 페인을 맞바꾼 워크스페이스에서는 herdr가 뿌리 페인을 새로 지정하지 않으므로,
  worktree가 아닌 워크스페이스에 한해 사이드바의 브랜치와 다른 저장소를 셀 수 있다.
  worktree 워크스페이스는 herdr가 기억해 둔 체크아웃 경로를 쓰므로 이 문제가 없다.
- `herdr machine`으로 연결한 원격 호스트의 저장소는 그쪽 서버에서 따로 돌려야 한다.
- `git maintenance`의 자동 prefetch는 대안이 되지 못한다. `refs/prefetch/*`만 갱신하고
  `refs/remotes/*`는 그대로 두어서 herdr가 보는 값이 변하지 않는다.

## 감사

[mariotmc/herdr-source-control](https://github.com/mariotmc/herdr-source-control)이 herdr가 fetch를
하지 않는다는 문제와 스로틀을 둔 갱신이라는 해법을 먼저 보여 주었다. 이 플러그인은 그 아이디어를
가져오되 세 가지를 달리했다. 세 플랫폼을 모두 지원하고, 포커스된 저장소만이 아니라 열려 있는 모든
워크스페이스를 주기적으로 돌며, 내장 토큰이 지워지는 worktree 행에서도 보이도록 값을 직접 보고한다.

## 문서

- [docs/PLAN.md](docs/PLAN.md) — 무엇을 왜 만드는지, 어떤 순서로 갈지
- [docs/HERDR.md](docs/HERDR.md) — 설계의 근거가 되는 herdr 동작들. 전부 소스에서 확인했고 출처를 적어 두었다

## 라이선스

MIT
