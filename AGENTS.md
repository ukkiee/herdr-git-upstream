# Repository guidance

herdr-git-upstream은 원격 Git 상태를 사이드바 토큰과 worktree 화면으로 전달하는 Go 플러그인이다.
에이전트 지침은 이 파일에서 관리한다. `CLAUDE.md`는 이 파일을 import한다.

## 작업 시작

1. `git status --short --branch`와 작업 대상 diff를 확인하고 기존 변경의 소유 범위를 구분한다.
2. 기능 범위를 바꾸기 전에는 [docs/PLAN.md](docs/PLAN.md)의 경계와 해당 명세를 읽는다.
   용어를 쓰거나 고칠 때는 [CONTEXT.md](CONTEXT.md)를 참고하되 실제 동작은 코드와 회귀 시험으로 확인한다.
3. herdr 명령·이벤트·팝업 문맥을 바꿀 때는 [docs/HERDR.md](docs/HERDR.md)의 연동 근거와 현재 CLI를 확인한다.

## 구현 경계

- 포커스 훅은 쪽지를 남기고 빨리 반환한다. 주기적인 fetch는 데몬이 맡으며 작업 트리를 변경하지 않는다.
  `worktree.created` 훅은 사용자가 작업을 시작한 뒤 기준이 움직이지 않도록 그 자리에서 최신화한다.
- Git 명령은 `internal/gitrepo`, herdr 호출은 `internal/herdrcli` 경계를 사용한다.
  원격·추적 참조는 Git 설정과 refspec으로 해석한다. 특정 저장소의 원격명·브랜치명·개인 경로를 기본값에 넣지 않는다.
- 팝업 저장소 선택은 `--cwd` → `HERDR_PLUGIN_CONTEXT_JSON`의 워크스페이스 →
  `HERDR_WORKSPACE_ID` → 현재 디렉터리 순이다. 팝업의 cwd는 플러그인 저장소일 수 있다.
  주어진 호출 문맥의 해석·조회가 실패하면 오류를 반환하고 다른 저장소로 넘어가지 않는다.
- 최신화는 조건을 확인한 빨리 감기만 허용한다. 현재 브랜치 외에 명시적으로 고른 생성 기준과 기존 브랜치를 보호한다.
  원격 기준에서 새 브랜치를 만들 때의 upstream 해제는 기존 브랜치에 적용하지 않는다.
- 삭제를 바꿀 때는 [ADR 0002](docs/adr/0002-removal-boundary.md)를 읽는다. `safe` 대상만 강제 옵션 없이
  제거하고 브랜치는 남긴다. 삭제 직전에 대상·HEAD·안전 판정을 재확인하며, 일괄 확인 대상은 고정한다.
  `merged` 판정을 바꿀 때는 [ADR 0001](docs/adr/0001-merged-judgement.md)의 근거와 한계를 함께 확인한다.

## 검증

동작 변경에는 사용자가 관찰할 결과를 검증하는 회귀 시험을 추가한다. Git 동작은 임시 저장소와
로컬 bare 원격에서 확인하고 herdr 호출은 기존 시험 경계로 대체한다. 실제 사용자 worktree를
시험용으로 생성·삭제하거나 사용자 설정을 변경하는 작업은 요청 범위에 포함될 때만 한다.

Go 변경을 마치면 저장소 뿌리에서 다음을 실행한다.

```sh
go test -race ./...
go vet ./...
git diff --check
```

수정한 Go 파일에 `gofmt`를 적용한다. 터미널·프로세스·경로 등 플랫폼 동작을 바꾸면
linux/darwin/windows × amd64/arm64 교차 빌드도 확인한다. 빌드 통과와 실제 OS 실행 검증은 구분한다.
화면 변경은 모델·렌더링 시험과 실제 크기의 PTY로 검증한다. `herdr plugin link`는 빌드하지 않으므로
연결 실행 전에는 `sh scripts/build.sh` 또는 Windows의 `scripts/build.ps1`로 실행 파일을 갱신한다.

문서만 바꿨으면 전체 Go 시험 대신 링크·설정 예제·명령과 실제 구현의 일치를 확인한다.
기존 검증 결과를 참고할 때는 [검증 기록](docs/reviews/0.2.0-verification.md)에서 실행 환경과 미검증 범위를 읽는다.

## 문서와 전달

- 사용자 동작이 바뀌면 영문 기본 [README.md](README.md)와 [README.ko.md](README.ko.md)를 함께 고친다.
  설치·일상 사용은 README에, 상세 설정·판정 원리는 [영문 참고](docs/REFERENCE.md)와
  [한국어 참고](docs/REFERENCE.ko.md)에 둔다.
- 커밋 제목과 PR 본문은 한국어로 작성하고 변경 이유와 검증 결과를 적는다.
- GitHub 작업은 로컬 `gh`를 사용한다. 쓰기 전에 `gh auth status`로 계정을 확인하고 요청된 계정으로 실행한다.
- 완료 보고에는 지금 가능한 동작, 실행한 검증, 남은 한계를 짧게 적는다. PR 생성·push·배포는 실제 결과를 확인한 뒤 보고한다.
