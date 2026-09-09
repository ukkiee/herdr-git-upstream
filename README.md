# herdr-git-upstream

**pull 받아야 하는 저장소를 herdr 사이드바에서 바로 알아본다.** Linux, macOS, Windows에서 동작한다.

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
herdr plugin link /Users/ukyi/personal/herdr-git-upstream
```

설치할 때 herdr가 `go build`를 한 번 돌린다. Go 1.24 이상이 필요하며, 표준 라이브러리만 쓰므로
내려받을 의존성은 없다.

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
셸에서 직접 부를 때는 없다. 그때 다른 자리를 보면 설정이 통째로 무시되고, 잠금이 갈려 데몬이 둘 뜬다.

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

**`merged`는 놓칠 수 있어도 틀리지는 않는다.** 병합 뒤 통합 브랜치가 같은 파일을 다시 고쳤으면
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
herdr plugin action invoke refresh --plugin git-upstream   # 스로틀을 무시하고 지금 갱신
herdr plugin action invoke start   --plugin git-upstream   # 데몬 시작
herdr plugin action invoke stop    --plugin git-upstream   # 데몬 중지
```

실행 파일을 직접 부를 수도 있다.

```sh
./bin/herdr-git-upstream setup     # config.toml 에 붙여 넣을 설정을 출력 (파일은 고치지 않는다)
./bin/herdr-git-upstream status    # 데몬과 설정 상태를 JSON 으로 출력
./bin/herdr-git-upstream refresh
```

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

- 평소에는 fetch만 한다. 작업 트리를 건드리는 것은 갓 만든 worktree를 앞당길 때 한 번뿐이며,
  그것도 빨리 감기라 잃을 것이 없다. `fresh_worktrees`를 false로 두면 그마저 하지 않는다.
- fetch와 토큰 보고는 git 2.5 이상이면 된다. 오래된 배포판의 git에서도 동작하도록, 최근에 생긴
  옵션은 쓰지 않는다. `merged`의 조상 검사(`for-each-ref --contains`)는 2.7, merge-tree 비교와
  `catchup`은 2.38 이상에서만 돌고 그 아래에서는 조용히 쉰다.
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
