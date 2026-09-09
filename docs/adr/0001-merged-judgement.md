# merged 판정은 조상 검사와 merge-tree 트리 비교를 함께 쓴다

`git merge-base --is-ancestor`만으로는 squash 병합과 rebase 병합을 잡지 못한다. 실제 저장소(mfe)에서
upstream이 사라진 worktree 22개를 조상 관계로 검사하면 병합된 것이 하나도 나오지 않았다. 그래서
`git merge-tree --write-tree <통합 브랜치> HEAD`의 결과 트리가 통합 브랜치의 트리와 같으면(병합해도
아무것도 바뀌지 않으면) 이미 들어간 것으로 본다. 이 방법도 병합 뒤 통합 브랜치가 같은 파일을 다시
고쳤으면 놓치므로, `merged`는 "놓칠 수는 있어도 틀리지는 않는" 신호이고 주된 신호는 `gone`이다.

검사 대상은 둘로 나눈다. 값이 싼 조상 검사는 모든 원격 추적 참조를 상대로 하고(`for-each-ref
--contains HEAD refs/remotes/`), 값이 드는 merge-tree 비교는 원격 기본 브랜치와 저장소에 지정된
통합 브랜치(`git config git-upstream.mergeTarget`, 여러 값 가능)만 상대로 한다. mfe처럼 작업이
`origin/main`이 아니라 `origin/widget-studio/dev`로 병합되는 저장소가 있기 때문이다.

조상 검사에서 자기 참조(원격마다 있는 그 브랜치의 사본과 upstream 추적 참조)와 브랜치가 아닌
참조(`refs/pull/*/head` 같은 것)는 근거로 삼지 않고, 자기 사본이 통합 브랜치이면 아예 판정하지
않는다. 그래도 조상 검사는 "HEAD에서 갈라져 나간 브랜치"와 "HEAD가 병합된 브랜치"를 구별하지
못하므로, 남이 내 브랜치에서 갈라져 나가 push하면 내 브랜치에 `merged`가 붙는다. 이것도 "틀리지는
않는" 범위 안이지만 이른 신호다.

## Considered Options

- 조상 검사만: 단순하지만 squash 병합 흐름에서는 사실상 아무것도 잡지 못한다.
- `git cherry`나 patch-id 비교: squash 병합은 여러 커밋이 하나로 합쳐져 patch-id가 맞지 않는다.
- 통합 브랜치를 설정 파일(config.json)에 저장소 경로별로 두기: 경로 이동과 심볼릭 링크에 약하고,
  git config는 연결된 worktree 전부가 자동으로 공유한다.
