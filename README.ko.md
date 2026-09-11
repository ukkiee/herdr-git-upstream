# herdr-git-upstream

[English](README.md) | **한국어**

## 어떤 플러그인인가요?

**원격 Git 변경을 확인하고 worktree를 관리하는 [herdr](https://herdr.dev) 플러그인입니다.**
배경에서 원격 추적 참조를 갱신하고, 사이드바에 작업 상태를 표시하며, worktree 확인과 생성을 위한
팝업을 제공합니다.

herdr는 로컬에 저장된 Git 정보를 읽습니다. 이 플러그인이 fetch를 맡으므로, 동료가 push한 변경을
직접 fetch하지 않아도 사이드바에서 확인할 수 있습니다.

```text
● app                main                ↓3
  · fix-checkout     fix/checkout        ↓3 ↑1 conflict
  · add-search       feat/search         gone merged
```

## 어떤 역할을 하나요?

| 기능 | 할 수 있는 일 |
| --- | --- |
| 배경 갱신 | herdr에 열린 저장소의 원격 추적 참조를 주기적으로 fetch |
| 사이드바 표시 | 뒤처짐·앞섬, 사라진 upstream, 원격에 이미 포함된 내용, 따라잡기 충돌 확인 |
| worktree 화면 | 저장소의 모든 worktree를 판정과 함께 확인하고 삭제 가능한 항목 정리 |
| 생성 팝업 | 드롭다운에서 기준 브랜치를 선택하고 이름을 입력해 새 worktree 생성 |
| 자동 최신화 | 새 worktree의 기준이 원격보다 뒤처졌으면 조건을 확인하고 빨리 감기 |

주기적인 갱신은 참조만 fetch합니다. 자동 최신화는 조건을 통과했을 때 `--ff-only`로 실행하며 끌 수
있습니다. worktree 삭제는 사용자가 실행하는 동작이며, 강제 삭제하지 않고 브랜치를 남깁니다.

## 어떻게 설치하나요?

**herdr 0.7.5 이상**, **Go 1.24 이상**, **Git 2.31 이상**이 필요합니다. 충돌 검사와 squash/rebase 병합
판정에는 Git 2.38 이상을 사용하세요. 설치할 때 Go 표준 라이브러리만으로 실행 파일을 빌드합니다.
Linux·macOS·Windows를 대상으로 하며, Windows는 교차 컴파일만 확인했고 실제 실행은 검증하지 않았습니다.

### 1. 플러그인 설치

```sh
herdr plugin install ukkiee/herdr-git-upstream
```

로컬 저장소에서 개발할 때는 [로컬 개발](docs/REFERENCE.ko.md#로컬-개발)을 참고하세요.

### 2. 사이드바 토큰 추가

herdr의 `config.toml`을 여세요. `herdr --help`에 경로가 나오며, Linux·macOS 기본 경로는
`~/.config/herdr/config.toml`입니다. 아래 내용을 추가하거나 기존 `rows`에 토큰 항목을 합치세요.
`[ui.sidebar.spaces]`가 이미 있으면 같은 테이블을 다시 추가하지 말고 기존 것을 수정하세요.

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

커스텀 토큰은 herdr 내장 Git 토큰이 생략되는 들여쓴 worktree 행에도 표시됩니다.
이 항목이 없어도 fetch는 동작하지만 플러그인 자체 표시는 보이지 않습니다.

### 3. 설정 적용

```sh
herdr config check && herdr server reload-config
herdr plugin action invoke start --plugin git-upstream
```

두 번째 명령은 실행 중인 herdr 세션에서 데몬을 시작합니다. 이후에는 시작·포커스 이벤트가 데몬을
유지합니다. Git 워크스페이스를 열면 해당 토큰이 나타납니다. 값이 0이거나 해당 사항이 없으면 비어 있습니다.

## 어떻게 사용하나요?

### 사이드바 읽기

| 표시 | 뜻 |
| --- | --- |
| `↓3` / `↑1` | upstream과 비교해 받을 커밋 3개 / push할 커밋 1개 |
| `gone` | upstream 브랜치가 원격에서 사라짐 |
| `merged` | Git 검사에서 HEAD의 내용이 다른 원격 브랜치에 이미 포함된 것으로 판정 |
| `conflict` | upstream을 따라잡으면 충돌 |
| `stale` | fetch 실패가 오래 이어져 숫자가 낡았을 수 있음 |

`merged`는 판정이며 PR 병합 완료를 보증하지 않습니다. 자체 커밋이 없는 새 브랜치에도 표시될 수
있습니다. [토큰 상세](docs/REFERENCE.ko.md#토큰)와 [판정 한계](docs/REFERENCE.ko.md#동작-방식)를 참고하세요.

지금 바로 갱신하려면 실행하세요.

```sh
herdr plugin action invoke refresh --plugin git-upstream
```

### worktree 화면 열기

herdr에서 해당 저장소의 워크스페이스를 연 상태로 실행하세요.

```sh
herdr plugin action invoke worktrees --plugin git-upstream
```

herdr에 열지 않은 항목까지 **그 저장소의 모든 worktree**를 보여 줍니다. 이름을 먼저 `pending`으로
표시하고 배경에서 판정합니다. `safe`는 삭제 가능한 항목, `review`는 확인이 필요한 항목,
`keep`은 진행 중인 작업, `blocked`는 보호된 항목입니다.

| 키 | 동작 |
| --- | --- |
| `↑` / `↓` 또는 `j` / `k` | 선택 이동 |
| `Enter` | 선택한 worktree를 열거나 포커스 |
| `d` / `D` | 선택한 `safe` 항목 즉시 삭제 / 모든 `safe` 항목을 확인 후 삭제 |
| `r` | 갱신 |
| `q` / `Esc` | 닫기 |

최초 판정 중에는 삭제할 수 없습니다. 삭제 직전에도 대상과 안전 판정을 다시 확인합니다.
자세한 기준은 [판정·삭제 규칙](docs/REFERENCE.ko.md#worktree-화면)에 있습니다.

### worktree 생성하기

```sh
herdr plugin action invoke new-worktree --plugin git-upstream
```

1. 제안된 브랜치 이름을 수정하세요. 커서는 끝에 있으며, 입력하면 덧붙고 `Backspace`는 한 글자씩 지웁니다.
2. `Tab`으로 `Base`에 이동하고 `Enter`로 드롭다운을 여세요.
3. `↑` / `↓`로 브랜치를 고르고 `Enter`로 확정하세요.
4. `Tab`으로 이름 칸에 돌아온 뒤 `Enter`로 생성하세요.

현재 브랜치·upstream·원격 기본·설정된 통합 브랜치를 먼저 보여 주고, 이어서 로컬 브랜치와 알려진
원격 추적 브랜치 전체를 보여 줍니다. 현재 위치와 전체 개수도 표시합니다.
원격 브랜치 목록은 로컬에 알려진 참조 기준이며 서버의 모든 브랜치를 실시간으로 조회한 목록은 아닙니다.

선택한 원격 기준은 생성 전에 fetch합니다. `Esc`는 드롭다운을 먼저 닫고, 다시 누르면 팝업을
취소합니다. 생성에는 실행 중인 herdr 서버가 필요합니다. 경로, 기존 브랜치 이름, upstream 처리는
[생성 상세](docs/REFERENCE.ko.md#생성-팝업)를 참고하세요.

### 단축키

원하면 다음 **설정 예제**를 `config.toml`에 추가하세요. 기존 설정에서 비어 있는 키를 선택하면 됩니다.
추가 후 `herdr config check && herdr server reload-config`를 실행하면 적용됩니다.

**W는 Worktrees**, **C는 Create**로 기억하세요. herdr는 기본적으로 `prefix+shift+w`를 워크스페이스
이름 변경에 사용합니다. 기본값을 쓰고 있다면 먼저 `rename_workspace`를 다른 빈 키로 옮기세요. 예를 들면:

```toml
[keys]
rename_workspace = "prefix+shift+comma"
```

`[keys]` 테이블을 중복 추가하지 말고 기존 테이블을 수정하세요. 이름 변경을 이미 다른 키로 옮겼다면
그 설정을 유지하세요. 이어서 플러그인 명령을 추가합니다.

```toml
[[keys.command]]
key = "prefix+shift+w"
type = "plugin_action"
command = "git-upstream.worktrees"
description = "git upstream: worktrees"

[[keys.command]]
key = "prefix+shift+c"
type = "plugin_action"
command = "git-upstream.new-worktree"
description = "git upstream: new worktree"
```

설정한 prefix를 누른 뒤 `Shift+W`로 worktree 화면, `Shift+C`로 생성 팝업을 엽니다.
예를 들어 prefix가 `Ctrl+A`라면 생성 팝업은 `Ctrl+A` → `Shift+C`입니다.
이 키는 사용자 설정이며 플러그인이 자동으로 지정하는 단축키가 아닙니다.

### 설정과 도움말

플러그인 설정 파일 없이도 기본값으로 동작합니다. 갱신 주기나 자동 최신화를 바꾸려면
`herdr plugin config-dir git-upstream`이 알려 주는 디렉터리에 `config.json`을 두세요.
[상세 설정](docs/REFERENCE.ko.md#설정)과 [config.example.json](config.example.json)에 항목이 있습니다.
통합 브랜치 추가는 저장소별 선택 설정이며 특정 프로젝트의 브랜치 이름을 넣을 필요는 없습니다.

토큰이나 팝업이 보이지 않으면 [문제 해결](docs/REFERENCE.ko.md#문제를-살펴볼-때)부터 확인하세요.
개발할 때는 [구현 계획](docs/PLAN.md), [herdr 연동 근거](docs/HERDR.md), [에이전트 지침](AGENTS.md)을 참고하세요.

[mariotmc/herdr-source-control](https://github.com/mariotmc/herdr-source-control)이 제시한 fetch 누락 문제와
스로틀을 둔 갱신 방식에서 출발했습니다. 라이선스는 [MIT](LICENSE)입니다.
