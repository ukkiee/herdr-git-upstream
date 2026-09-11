package herdrcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// fakeHerdrDelayEnv 가 있으면 시험 바이너리 자신이 herdr 노릇을 한다. 값은 답하기 전에 기다릴 시간(밀리초)이다.
//
// 셸 스크립트 대신 시험 바이너리를 다시 띄우는 이유는 윈도우에서도 같은 시험이 돌아야 하기 때문이다. 이 가짜는
// 인자를 보지 않고 기다렸다가 빈 응답을 낼 뿐이라, 실행 중인 herdr 서버에는 아무것도 보내지 않는다.
const fakeHerdrDelayEnv = "HERDR_GIT_UPSTREAM_TEST_FAKE_HERDR_DELAY_MS"
const fakeHerdrErrorEnv = "HERDR_GIT_UPSTREAM_TEST_FAKE_HERDR_ERROR"
const fakeHerdrArgsEnv = "HERDR_GIT_UPSTREAM_TEST_FAKE_HERDR_ARGS"
const fakeHerdrResponseEnv = "HERDR_GIT_UPSTREAM_TEST_FAKE_HERDR_RESPONSE"

func TestMain(m *testing.M) {
	if raw := os.Getenv(fakeHerdrArgsEnv); raw != "" {
		var want []string
		if err := json.Unmarshal([]byte(raw), &want); err != nil || !reflect.DeepEqual(os.Args[1:], want) {
			fmt.Fprintf(os.Stderr, "args %q, want %s", os.Args[1:], raw)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if raw := os.Getenv(fakeHerdrErrorEnv); raw != "" {
		fmt.Fprintln(os.Stderr, raw)
		os.Exit(1)
	}
	if v := os.Getenv(fakeHerdrDelayEnv); v != "" {
		ms, _ := strconv.Atoi(v)
		time.Sleep(time.Duration(ms) * time.Millisecond)
		if raw := os.Getenv(fakeHerdrResponseEnv); raw != "" {
			fmt.Print(raw)
		} else {
			fmt.Print(`{"id":"cli:fake","result":{}}`)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// WorktreeRemove 는 Timeout 이 아니라 removeTimeout 을 쓴다. 삭제는 파일 수에 비례해 오래 걸리므로(gitrepo 의
// RemoveWorktree 와 같은 이유) 조회와 같은 제한 시간에 묶이면 안 된다. 같은 클라이언트로 조회는 Timeout 에
// 걸려 실패하고, 그보다 오래 걸리는 삭제는 성공해야 한다.
func TestWorktreeRemoveUsesRemoveTimeout(t *testing.T) {
	t.Setenv(fakeHerdrDelayEnv, "300")
	c := &Client{Binary: os.Args[0], Timeout: 50 * time.Millisecond}
	if _, err := c.WorkspaceList(context.Background()); err == nil {
		t.Fatal("Timeout 을 넘긴 조회는 실패해야 한다")
	}
	if err := c.WorktreeRemove(context.Background(), "w1"); err != nil {
		t.Fatalf("삭제는 Timeout 보다 오래 걸려도 removeTimeout 안이면 성공해야 한다: %v", err)
	}
}

// herdr 0.9.0 은 거절을 종료 코드 1 과 표준 오류의 JSON 봉투로 알린다.
func TestRejectedCommandPreservesServerError(t *testing.T) {
	t.Setenv(fakeHerdrErrorEnv, `{"error":{"code":"workspace_not_found","message":"workspace missing not found"},"id":"cli:worktree:list"}`)
	c := &Client{Binary: os.Args[0]}
	_, err := c.WorktreeList(context.Background(), "missing", "")
	var response *ResponseError
	if !errors.As(err, &response) || response.Code != "workspace_not_found" {
		t.Fatalf("표준 오류의 서버 오류 이름이 보존되어야 한다: %v", err)
	}
}

// 아래 두 본문은 herdr 0.9.0 의 표준 오류에서 받은 것이다.
func TestResponseError(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{"저장소가 아닌 자리", `{"error":{"code":"not_git_worktree","message":"Herdr worktree actions require a path inside a Git work tree"},"id":"cli:worktree:list"}`, "not_git_worktree: Herdr worktree actions require a path inside a Git work tree"},
		{"없는 워크스페이스", `{"error":{"code":"workspace_not_found","message":"workspace no-such-id not found"},"id":"cli:worktree:list"}`, "workspace_not_found: workspace no-such-id not found"},
		{"문구 없는 오류", `{"error":{"code":"denied"}}`, "denied"},
		{"성공 응답", `{"id":"cli:worktree:list","result":{"worktrees":[]}}`, ""},
		{"error 가 null", `{"error":null,"result":{}}`, ""},
		{"빈 error 객체는 오류가 아님", `{"error":{}}`, ""},
		{"JSON 이 아님", "ok", ""},
		{"빈 출력", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := responseError([]byte(tc.out))
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != tc.want {
				t.Fatalf("%q, 기대값 %q", got, tc.want)
			}
			if tc.want != "" {
				var typed *ResponseError
				if !errors.As(err, &typed) || typed.Code == "" {
					t.Fatalf("오류 이름을 꺼낼 수 있어야 한다: %v", err)
				}
			}
		})
	}
}

// 아래 JSON 은 herdr 0.9.0 에 `herdr worktree list --cwd <저장소>` 를 실제로 물어 받은 응답의 모양이다.
// 필드 이름이 바뀌면 화면이 조용히 빈 값을 보게 되므로 고정된 본문으로 잡아 둔다.
func TestParseWorktreeList(t *testing.T) {
	raw := `{"id":"cli:worktree:list","result":{"source":{"repo_key":"/home/u/mfe/.git","repo_name":"mfe","repo_root":"/home/u/mfe","source_checkout_path":"/home/u/mfe","source_workspace_id":"wC5"},"type":"worktree_list","worktrees":[{"branch":"main","is_bare":false,"is_detached":false,"is_linked_worktree":false,"is_prunable":false,"label":"mfe","open_workspace_id":"wC5","path":"/home/u/mfe"},{"branch":"fix-agent","is_bare":false,"is_detached":false,"is_linked_worktree":true,"is_prunable":false,"label":"mfe-fix-agent","open_workspace_id":"wEP","path":"/home/u/.herdr/worktrees/mfe/fix-agent"},{"branch":"","is_bare":false,"is_detached":true,"is_linked_worktree":true,"is_prunable":true,"label":"","open_workspace_id":null,"path":"/home/u/.herdr/worktrees/mfe/old"}]}}`
	got, err := parseWorktreeList([]byte(raw))
	if err != nil {
		t.Fatalf("해석하지 못했다: %v", err)
	}
	want := WorktreeListResult{
		Source: WorktreeSource{RepoName: "mfe", RepoRoot: "/home/u/mfe", SourceCheckoutPath: "/home/u/mfe", SourceWorkspaceID: "wC5"},
		Worktrees: []WorktreeItem{
			{Path: "/home/u/mfe", Branch: "main", Label: "mfe", OpenWorkspaceID: "wC5"},
			{Path: "/home/u/.herdr/worktrees/mfe/fix-agent", Branch: "fix-agent", IsLinkedWorktree: true, Label: "mfe-fix-agent", OpenWorkspaceID: "wEP"},
			// 열려 있지 않은 worktree 의 open_workspace_id 는 null 이다. 빈 문자열로 읽혀야 "herdr 에 없음"이 된다.
			{Path: "/home/u/.herdr/worktrees/mfe/old", IsDetached: true, IsLinkedWorktree: true, IsPrunable: true},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("해석 결과가 다르다:\n%+v\n기대값\n%+v", got, want)
	}
	if _, err := parseWorktreeList([]byte("not json")); err == nil {
		t.Fatal("JSON 이 아니면 오류여야 한다")
	}
}

// agent_status 는 실측으로 "working", "idle", "unknown" 이 보였다. working 만 일하는 것으로 읽는다.
func TestParseWorkspaceListReadsAgentStatus(t *testing.T) {
	raw := `{"id":"cli:workspace:list","result":{"workspaces":[{"workspace_id":"w1","label":"a","agent_status":"working","worktree":{"checkout_path":"/home/u/a","repo_root":"/home/u/a","repo_key":"/home/u/a/.git","is_linked_worktree":false}},{"workspace_id":"w2","label":"b","agent_status":"idle","worktree":null},{"workspace_id":"w3","label":"c","agent_status":"unknown"},{"workspace_id":"w4","label":"d"}]}}`
	got, err := parseWorkspaceList([]byte(raw))
	if err != nil {
		t.Fatalf("해석하지 못했다: %v", err)
	}
	cases := []struct {
		id      string
		status  string
		working bool
	}{
		{"w1", "working", true},
		{"w2", "idle", false},
		{"w3", "unknown", false},
		{"w4", "", false},
	}
	if len(got) != len(cases) {
		t.Fatalf("워크스페이스 수가 다르다: %d", len(got))
	}
	for i, tc := range cases {
		if got[i].WorkspaceID != tc.id || got[i].AgentStatus != tc.status || got[i].AgentWorking() != tc.working {
			t.Fatalf("%d 번째가 다르다: %+v, 기대값 %+v", i, got[i], tc)
		}
	}
	if got[0].Worktree == nil || got[0].Worktree.CheckoutPath != "/home/u/a" {
		t.Fatalf("worktree 필드는 그대로 읽혀야 한다: %+v", got[0].Worktree)
	}
	if got[1].Worktree != nil {
		t.Fatal("null 인 worktree 는 nil 이어야 한다")
	}
}

// 인자 조립은 CLI 를 감싸는 일의 전부다. 옵션이 빠지거나 자리가 바뀌면 herdr 가 거절하므로 표로 잡아 둔다.
func TestCommandArguments(t *testing.T) {
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"worktree create: 워크스페이스", worktreeCreateArgs("w1", "/ignored", "new/feature", "origin/main"), []string{"worktree", "create", "--branch", "new/feature", "--base", "origin/main", "--focus", "--workspace", "w1"}},
		{"worktree create: 경로", worktreeCreateArgs("", "/repo", "feature", "main"), []string{"worktree", "create", "--branch", "feature", "--base", "main", "--focus", "--cwd", "/repo"}},
		{"worktree list: 워크스페이스가 있으면 그것", worktreeListArgs("w1", "/x"), []string{"worktree", "list", "--workspace", "w1"}},
		{"worktree list: 없으면 cwd", worktreeListArgs("", "/x"), []string{"worktree", "list", "--cwd", "/x"}},
		{"worktree list: 둘 다 없으면 옵션 없음", worktreeListArgs("", ""), []string{"worktree", "list"}},
		{"plugin pane open: placement 없음", pluginPaneOpenArgs("git-upstream", "worktrees", ""), []string{"plugin", "pane", "open", "--plugin", "git-upstream", "--entrypoint", "worktrees", "--focus"}},
		{"plugin pane open: placement 있음", pluginPaneOpenArgs("git-upstream", "worktrees", "overlay"), []string{"plugin", "pane", "open", "--plugin", "git-upstream", "--entrypoint", "worktrees", "--placement", "overlay", "--focus"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !reflect.DeepEqual(tc.got, tc.want) {
				t.Fatalf("%v, 기대값 %v", tc.got, tc.want)
			}
		})
	}
}

func TestParseWorktreeCreated(t *testing.T) {
	const valid = `{"id":"cli:worktree:create","result":{"type":"worktree_created","workspace":{"workspace_id":"w1"},"worktree":{"path":"/worktrees/repo/feature","branch":"feature","is_linked_worktree":true}}}`
	path, err := parseWorktreeCreated([]byte(valid))
	if err != nil || path != "/worktrees/repo/feature" {
		t.Fatalf("스키마의 생성 경로: %q, %v", path, err)
	}
	for _, raw := range []string{`{}`, `{"result":{"type":"worktree_created","worktree":{}}}`, `{"result":{"type":"worktree_opened","worktree":{"path":"/x"}}}`, `not json`} {
		if _, err := parseWorktreeCreated([]byte(raw)); err == nil {
			t.Fatalf("생성 결과가 아닌 응답을 거절해야 한다: %s", raw)
		}
	}
}

func TestWorktreeCreateUsesMutationTimeout(t *testing.T) {
	t.Setenv(fakeHerdrDelayEnv, "300")
	t.Setenv(fakeHerdrResponseEnv, `{"result":{"type":"worktree_created","worktree":{"path":"/new"}}}`)
	c := &Client{Binary: os.Args[0], Timeout: 50 * time.Millisecond}
	path, err := c.WorktreeCreate(context.Background(), "w1", "", "feature", "origin/main")
	if err != nil || path != "/new" {
		t.Fatalf("체크아웃은 조회보다 오래 걸려도 완료를 기다린다: %q, %v", path, err)
	}
}

func TestWorktreeOpenCarriesMainCheckoutSource(t *testing.T) {
	args, err := json.Marshal([]string{"worktree", "open", "--cwd", "/repo/main checkout", "--path", "/repo/sibling", "--focus"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeHerdrArgsEnv, string(args))
	c := &Client{Binary: os.Args[0]}
	if err := c.WorktreeOpen(context.Background(), "/repo/main checkout", "/repo/sibling"); err != nil {
		t.Fatal(err)
	}
	if err := c.WorktreeOpen(context.Background(), "", "/repo/sibling"); err == nil {
		t.Fatal("source-less open must be rejected")
	}
}
