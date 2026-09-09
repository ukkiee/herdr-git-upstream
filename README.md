# herdr-pull-status

**pull 받아야 하는 저장소를 herdr 사이드바에서 바로 알아본다.** Linux, macOS, Windows에서 동작한다.

```
● mfe                 widget-studio/dev   ↓3
  · design-qa-3       fix/modal           ↓3 ↑1
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
2. 워크스페이스마다 `$behind`와 `$ahead` 토큰을 보고한다.

두 번째가 필요한 이유가 있다. herdr는 같은 저장소를 공유하는 워크스페이스들을 묶어서 들여쓰는데,
**들여쓴 행에서는 내장 `branch`와 `git_status` 토큰을 지운다.** worktree를 여러 개 열어 두고 쓰는
사람에게는 오히려 그 행이 더 중요하다. 커스텀 토큰은 그 행에서도 그려지므로, 토큰을 쓰면 어느
행에서나 같은 정보가 보인다.

## 설치

```sh
herdr plugin install <owner>/herdr-pull-status
```

로컬에서 개발 중이라면 이렇게 붙인다.

```sh
herdr plugin link /Users/ukyi/personal/herdr-pull-status
```

설치할 때 herdr가 `go build`를 한 번 돌린다. Go 1.24 이상이 필요하며, 표준 라이브러리만 쓰므로
내려받을 의존성은 없다.

## 사이드바 설정

**이 설정을 넣기 전에는 아무것도 보이지 않는다.** herdr는 사이드바 설정이 요청한 토큰만 그린다.
`~/.config/herdr/config.toml`에 다음을 넣는다.

```toml
[ui.sidebar.spaces]
rows = [
  ["state_icon", "workspace"],
  [
    "branch",
    { token = "$behind", fg = "#f38ba8", bold = true },
    { token = "$ahead",  fg = "#a6e3a1" },
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
| `$sync_stale` | `stale` | fetch가 오래 실패하고 있어 위 숫자를 믿을 수 없다 |

값이 0이거나 해당 사항이 없으면 토큰이 비고, herdr가 그 자리를 지운다. 저장소가 아니거나,
HEAD가 분리되어 있거나, upstream이 설정되지 않은 워크스페이스에서는 아무것도 표시하지 않는다.

## 설정

`herdr plugin config-dir pull-status`가 알려 주는 디렉터리에 `config.json`을 둔다. 파일이 없으면
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
  "enabled": true
}
```

| 항목 | 기본값 | 설명 |
|---|---|---|
| `interval_seconds` | 60 | 전체 워크스페이스를 한 바퀴 도는 간격 (5초 ~ 24시간) |
| `throttle_seconds` | 120 | 같은 저장소를 다시 가져오기까지의 최소 간격 |
| `fetch_timeout_seconds` | 20 | 저장소 하나당 fetch 제한 시간 |
| `stale_after_seconds` | 900 | 실패가 이만큼 이어지면 `stale`을 띄운다 |
| `enabled` | true | false로 두면 이 플러그인이 올린 토큰을 지우고 쉰다 |

토큰 이름을 빈 문자열로 두면 그 토큰은 보고하지 않는다. 설정은 매 회차마다 다시 읽으므로
herdr를 재시작하지 않아도 주기를 바꿀 수 있다.

## 동작 방식

```
herdr startup 훅 ─┐
                  ├─► 데몬 (herdr 바깥에서 계속 돎)
포커스 이벤트 ────┘        │
  쪽지만 남기고 즉시 끝남   ├─ 저장소마다 좁은 fetch
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

**fetch는 좁게 하고, 사용자의 작업을 건드리지 않는다.** 현재 브랜치의 참조 하나만 가져오고,
태그와 prune과 하위 모듈은 손대지 않으며 자동 정리(`gc.auto`)도 꺼 둔다. `--no-write-fetch-head`로
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

**첫 실패로는 경고하지 않는다.** 잠시 끊긴 네트워크나 아직 연결하지 않은 VPN 때문에 곧바로
`stale`이 뜨면, 정작 사람이 손봐야 하는 상황과 구별되지 않는다. 실패가 이어진 시간을 기준으로 삼는다.

## 명령

```sh
herdr plugin action invoke refresh --plugin pull-status   # 스로틀을 무시하고 지금 갱신
herdr plugin action invoke start   --plugin pull-status   # 데몬 시작
herdr plugin action invoke stop    --plugin pull-status   # 데몬 중지
```

실행 파일을 직접 부를 수도 있다.

```sh
./bin/herdr-pull-status status    # 데몬과 설정 상태를 JSON 으로 출력
./bin/herdr-pull-status refresh
```

## 문제를 살펴볼 때

`status`가 알려 주는 로그 파일을 먼저 본다.

```sh
./bin/herdr-pull-status status
tail -f "$(./bin/herdr-pull-status status | sed -n 's/.*"log_path": "\(.*\)".*/\1/p')"
```

상세 로그가 필요하면 `HERDR_PULL_STATUS_DEBUG=1`을 준 채로 데몬을 다시 띄운다.

아무것도 보이지 않는다면 대개 둘 중 하나다. 사이드바 `rows`에 토큰을 넣지 않았거나,
사이드바를 접어 두었거나(접힌 상태에서는 herdr가 번호와 상태 점만 그린다).

## 알아둘 점

- 이 플러그인은 fetch만 한다. `pull`도 `merge`도 하지 않으므로 작업 트리를 건드리지 않는다.
- `herdr machine`으로 연결한 원격 호스트의 저장소는 그쪽 서버에서 따로 돌려야 한다.
- `git maintenance`의 자동 prefetch는 대안이 되지 못한다. `refs/prefetch/*`만 갱신하고
  `refs/remotes/*`는 그대로 두어서 herdr가 보는 값이 변하지 않는다.

## 감사

[mariotmc/herdr-source-control](https://github.com/mariotmc/herdr-source-control)이 herdr가 fetch를
하지 않는다는 문제와 스로틀을 둔 갱신이라는 해법을 먼저 보여 주었다. 이 플러그인은 그 아이디어를
가져오되 세 가지를 달리했다. 세 플랫폼을 모두 지원하고, 포커스된 저장소만이 아니라 열려 있는 모든
워크스페이스를 주기적으로 돌며, 내장 토큰이 지워지는 worktree 행에서도 보이도록 값을 직접 보고한다.

## 라이선스

MIT
