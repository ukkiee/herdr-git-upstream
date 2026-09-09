# 구현 계획

## 이 플러그인은 무엇인가

**작업이 최신 바닥 위에 서 있게 한다.**

herdr는 로컬 git 상태를 잘 보여 주지만 원격에는 한 번도 말을 걸지 않는다. 그래서 herdr가 그리는
숫자는 마지막으로 누군가 fetch 한 시점의 이야기이고, 새로 만든 worktree는 손에 쥔 낡은 커밋에서
시작한다. 이 플러그인은 원격 쪽 일을 대신 맡아 그 두 가지를 고친다.

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

worktree 삭제는 경계에 걸쳐 있다. herdr-shear가 이미 잘하지만 판정 기준이 **로컬** 기본 브랜치라,
로컬 main이 뒤처져 있으면 이미 병합된 브랜치를 살아 있는 것으로 잘못 분류한다. 우리는 원격 기준으로
판정할 수 있으므로 그 차이만큼만 들어간다. 자세한 것은 아래 3번.

## 지금까지 만든 것

| 기능 | 상태 |
| --- | --- |
| 주기적 fetch (기본 60초, 저장소당 120초 스로틀) | 완료 |
| `$behind` / `$ahead` / `$sync_stale` 토큰 보고 | 완료 |
| 새 worktree 를 기준 브랜치 최신으로 빨리 감기 | 완료 |

시험 53개가 통과하고 여섯 플랫폼에서 교차 컴파일된다. 아직 herdr 에 붙이지 않았다.

구조는 이렇다.

```
cmd/herdr-git-upstream/   명령 진입점
internal/config/          설정 읽기, 토큰 이름 검사
internal/daemon/          갱신 루프, 잠금, 쪽지, 워크스페이스 훑기
internal/freshen/         새 worktree 최신화
internal/gitrepo/         저장소 찾기, upstream 해석, fetch, 빨리 감기
internal/herdrcli/        herdr CLI 감싸기
internal/herdrpaths/      herdr 와 같은 규칙으로 디렉터리 찾기
internal/state/           fetch 기록, 데몬 잠금
```

## 앞으로 넣을 것

### 0. `setup` 명령

**왜.** 이 플러그인은 설정을 두 군데 손봐야 제 몫을 한다. 사이드바 행에 토큰을 넣어야 숫자가 보이고,
키를 묶어야 화면이 열린다. 둘 다 하지 않으면 fetch 만 돌아 herdr 내장 `git_status` 가 정확해지는
데까지다. 그것만으로도 값어치가 있지만, 나머지 절반이 조용히 잠들어 있는 셈이다.

조사한 플러그인들이 하나같이 "설정을 넣기 전에는 아무것도 보이지 않는다"를 경고문으로 달고 있었다.
같은 자리에서 넘어지지 않으려면 붙여 넣을 것을 우리가 만들어 주어야 한다.

**무엇을.**

```
$ herdr-git-upstream setup

~/.config/herdr/config.toml 에 아래를 더하세요.

[ui.sidebar.spaces]
rows = [
  ["state_icon", "workspace"],
  ["branch", { token = "$behind", fg = "#f38ba8" }, { token = "$ahead", fg = "#a6e3a1" }],
]

[[keys.command]]
key = "prefix+shift+w"
type = "plugin_action"
command = "git-upstream.worktrees"
description = "worktree board"

그다음: herdr config check && herdr server reload-config
```

**어떻게.** 사용자의 `config.toml` 을 읽어 이미 `[ui.sidebar.spaces]` 를 쓰고 있으면 그 행에 우리
토큰만 끼워 넣은 결과를 보여 준다. 이미 우리 토큰이 있으면 그 부분은 빼고 남은 것만 알려 준다.

**파일을 말없이 고치지 않는다.** 남의 설정 파일을 손대는 플러그인은 신뢰를 잃는다. 출력만 하고
붙여 넣는 것은 사람이 한다.

**작다.** 언제 넣어도 되지만 먼저 넣으면 나머지를 시험하기 편해진다.

### 1. 병합·삭제 판정 (`$gone`, `$merged`)

**왜.** worktree 를 열 개 넘게 열어 두면 어느 것이 끝난 작업인지 사람이 기억하지 못한다.
원격을 봐야만 답이 나오는 질문이다.

**무엇을.** 워크스페이스마다 토큰 하나를 더 보고한다.

| 토큰 | 뜻 |
| --- | --- |
| `$gone` | 원격에서 그 브랜치가 사라졌다. 대개 병합 후 삭제된 것이다 |
| `$merged` | 원격 기본 브랜치에 이미 병합되었다 |

**어떻게.**

- `gone`: fetch 뒤에도 추적 참조가 없으면. prune 없이 알아내려면 `git ls-remote --heads <원격> <참조>`가
  빈 답을 주는지 본다. 지금 fetch 는 `--no-prune` 이라 추적 참조만으로는 판단할 수 없다.
- `merged`: `git merge-base --is-ancestor HEAD <원격 기본 브랜치>` 가 참이면 병합된 것이다.
  원격 기본 브랜치는 `refs/remotes/<원격>/HEAD` 에서 읽고, 없으면 판정하지 않는다.

**열린 질문.** `ls-remote` 를 저장소마다 매번 부르면 fetch 한 번이 두 번이 된다. 스로틀을 따로 두거나,
기본 브랜치 하나만 넓게 fetch 해서 prune 을 켜는 방법을 견줘 봐야 한다.

### 2. 따라잡을 때 충돌하는지 미리 보기

**왜.** `↓12` 만으로는 결정을 못 한다. 열두 개 뒤처졌지만 깨끗하게 따라잡히는 것과, 세 개인데 손이
가는 것은 사람이 할 일이 다르다. 조사한 1,045개 플러그인 중 아무도 하지 않는다.

**무엇을.** `$catchup` 토큰. 값은 `clean` 또는 `conflict`.

**어떻게.** `git merge-tree --write-tree <추적 참조> HEAD` 의 종료 코드로 판단한다. 0 이 아니면
충돌이다. **작업 트리를 전혀 건드리지 않는다.** 실측으로 확인했다.

git 2.38 이상이 필요하다. 그보다 낮으면 이 토큰만 조용히 쉰다. 나머지 기능은 git 2.5 이상에서 돈다.

**열린 질문.** 뒤처진 것이 없으면 계산할 필요가 없다. 뒤처짐이 생겼을 때만 계산하고 결과를 커밋 쌍으로
캐시하면 비용이 거의 없다.

### 3. worktree 화면

**왜.** 1번과 2번의 판정이 모이면 "어느 것부터 손봐야 하나"에 답하는 표가 된다.
사이드바 토큰은 한 줄에 조금씩만 보여 줄 수 있어서, 열세 개를 한눈에 견주려면 화면이 필요하다.

**무엇을.** worktree 판을 보여 주는 화면 하나. 세 갈래로 연다.

| 통로 | 쓰임새 |
| --- | --- |
| 키 설정 (`type = "plugin_action"`) | 평소 사용. 이것이 정상적인 통로다 |
| `herdr plugin action invoke worktrees --plugin git-upstream` | 스크립트나 시험 |
| `herdr-git-upstream worktrees` | 터미널에서 직접. 데몬 없이도 돈다 |

앞의 둘은 팝업으로 열리고, 마지막은 지금 있는 페인에 그대로 그린다.

herdr 에는 액션을 골라 실행하는 화면이 없고, 액션의 `contexts` 는 아직 쓰이지 않는다
(자세한 것은 [HERDR.md](HERDR.md)). 그래서 **키에 묶기 전에는 없는 것과 같다.**
아래 0번의 `setup` 명령이 그 설정을 내놓는 이유가 이것이다.

```
 mfe · 13 worktrees                            ↑↓ move  ⏎ open

 safe    add-shopping-widget-api   gone, merged        8 kB
 safe    fix-design-detail         merged upstream    12 kB
 review  design-qa-3               ↓12, conflicts     34 MB
 review  packages                  dirty               8 kB
 keep    agent-admin               up to date        156 kB
 keep    fix-modal                 ↓3, clean catch-up  8 kB
 blocked widget-studio/dev         main checkout     456 MB

 3 safe · d remove selected · D remove all safe
```

`design-qa-3` 과 `fix-modal` 을 견줘 보면 이 화면이 왜 필요한지 드러난다. 하나는 열두 개 뒤처졌지만
따라잡을 때 손이 가고, 다른 하나는 세 개 뒤처졌지만 깨끗하게 따라잡힌다. 숫자만으로는 어느 쪽을
먼저 손볼지 정할 수 없다.

| 판정 | 조건 |
| --- | --- |
| `safe` | 깨끗하고, 원격에서 사라졌거나 원격 기본 브랜치에 병합됨 |
| `review` | 깨끗하지 않거나, 뒤처졌거나, 판단이 필요함 |
| `keep` | 아직 일이 남음 |
| `blocked` | 본 체크아웃이거나 잠겨 있음. 지울 수 없음 |

**shear 와 무엇이 다른가.** 화면 모양은 닮았지만 답이 다르다. shear 의 `merged` 는 **로컬** 기본
브랜치 기준이라, 내 로컬 main 이 사흘 전 것이면 동료가 어제 병합한 브랜치를 아직 살아 있는 것으로
분류한다. 우리는 원격 참조를 계속 갱신하고 있으므로 `origin/main` 기준으로 판정한다.
뒤처진 정도와 충돌 예상은 shear 에 아예 없는 열이다.

**어떻게.** herdr 플러그인 pane 을 `popup` 으로 열고 그 안에서 대화형 화면을 그린다.
삭제는 `safe` 인 것만 허용한다.

- herdr 에 열려 있으면 `herdr worktree remove --workspace <id>`
- 디스크에만 있으면 `git worktree remove <경로>`

**강제 삭제는 넣지 않는다.** 깨끗한 worktree 를 지우면 체크아웃만 사라지고 브랜치와 커밋은 남는다.
잃을 것이 없는 것만 지우므로 되돌리기 기록도 필요 없다. 손댄 것이 있으면 지우지 않고 이유를 보여 준다.
shear 가 되돌리기 기록에 들인 공은 강제 삭제를 지원하기 때문이며, 우리는 그 문을 열지 않는다.

**뒤로 미룰 것.** 디스크 사용량은 worktree 마다 디렉터리를 훑어야 해서 화면이 느려진다.
먼저 만들고 반응을 본 뒤 넣는다. `--json` 출력도 나중에 쉽게 붙는다.

**넣지 않을 것.** CI 용 리포트 출력. 이 플러그인은 사람이 보는 화면을 위한 것이고, CI 에서 낡은
worktree 를 세는 일은 herdr 와 무관한 자리에서 하는 편이 맞다.

### 4. 기준 브랜치를 고르는 worktree 생성 팝업

**왜.** herdr 기본 팝업은 브랜치 이름만 받고 기준은 언제나 `HEAD` 다. 어디서 갈라져 나오는지
사람이 고를 수 없다.

**무엇을.**

```
┌ New worktree ─────────────────────────────────── mfe ─┐
│                                                        │
│  Branch  [widget-studio/dev-2]                         │
│  Path    ~/.herdr/worktrees/mfe/widget-studio-dev-2    │
│                                                        │
│  Base                                                  │
│  ▸ widget-studio/dev              current · ↓3 behind  │
│    origin/widget-studio/dev       remote · up to date  │
│    origin/main                    remote · up to date  │
│                                                        │
│  Tab switch · Enter create · Esc cancel                │
└────────────────────────────────────────────────────────┘
```

정해 둔 규칙.

- 커서는 브랜치 칸에서 시작하고, 자동으로 채운 이름이 통째로 선택되어 있다. 타이핑하면 덮어써진다
- 자동 이름은 기준 브랜치 이름에서 따오되 **언제나 비어 있는 이름**이다. 겹치면 `-2`, `-3` 으로 넘어간다.
  로컬 브랜치, 원격 브랜치, 열려 있는 worktree 셋을 모두 보고 빈 번호를 찾는다
- 기준의 기본 선택은 현재 브랜치. 뒤처져 있어도 생성 직후 자동 최신화가 앞당긴다
- 기준을 바꾸면 이름과 경로가 따라오되, 이름을 한 글자라도 직접 고친 뒤에는 덮어쓰지 않는다
- 경로는 herdr 의 `branch_to_path_slug` 규칙을 그대로 옮겨 만든다
- 새 브랜치인지 기존 브랜치인지는 표시하지 않는다. herdr 가 이름을 보고 알아서 가른다
- UI 문구는 영문

**어떻게.** `herdr worktree create --branch <입력> --base <선택>` 을 부른다. `--path` 는 비워
herdr 의 자리 규칙을 그대로 쓴다.

**옵트인이다.** 액션으로만 노출하고 기본 키를 잡지 않는다. 키를 묶기 전에는 없는 것과 같다.

## 순서와 이유

```
0. setup 명령            → 작다. 먼저 넣으면 나머지를 시험하기 편하다
1. 병합·삭제 판정        → 3번의 재료
2. 충돌 미리 보기        → 3번의 재료이자 그 자체로 값어치가 큼
3. worktree 화면         → 1,2 의 결과를 표로 그림. 터미널 화면 기반을 여기서 만듦
4. 생성 팝업             → 3 에서 만든 화면 기반을 재사용
```

3번과 4번이 같은 터미널 화면 코드를 쓴다. 한 번 만들어 두면 두 번째는 값이 싸다.

## 터미널 화면 기반 (3번에서 만든다)

의존성 없이 간다. raw 모드는 플랫폼마다 다르지만 표준 라이브러리로 닿는다.

- 유닉스: `syscall.Termios` 와 `TCGETS`/`TIOCGETA`
- 윈도우: `GetConsoleMode`/`SetConsoleMode`

`internal/tui` 에 두고, 화면은 `internal/worktreeui` 에 둔다. 나중에 팝업만 떼어 내고 싶어지면
그 두 디렉터리와 `internal/gitrepo` 만 옮기면 되도록 경계를 그어 둔다.

## 열린 질문

**README 를 영문으로 옮길 것인가.** 지금은 한국어다. 마켓플레이스에 올려 발견되기를 바란다면
영문이 유리하다. 코드 주석은 한국어로 두어도 무방하다.

**설정을 넣지 않은 사용자에게 알릴 것인가.** `setup` 명령(0번)은 물어본 사람에게만 답한다.
데몬이 시작할 때 사용자의 `config.toml` 을 읽어 우리 토큰이 없으면 herdr 알림으로 한 번 알리는
방법도 있다. 친절하지만, 부르지 않은 알림은 소음이 되기 쉬워 아직 정하지 않았다.

**`ls-remote` 비용.** 1번의 `gone` 판정 방식에 달렸다. prune 을 켜는 대안과 견주어야 한다.
