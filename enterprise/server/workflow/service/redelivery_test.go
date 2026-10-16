package service_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/endpoint_urls/build_buddy_url"
	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/tables"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testgit"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testhttp"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/google/go-github/v59/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const redeliveryTestDeliveryID = "6f1c2a10-0000-4000-8000-000000000001"

// collectExecuteRequests waits for the first execute request, then gathers any
// further requests that arrive within the grace period.
func collectExecuteRequests(t *testing.T, c *fakeExecutionClient, grace time.Duration) []*executeRequest {
	reqs := []*executeRequest{c.NextExecuteRequest()}
	deadline := time.After(grace)
	for {
		select {
		case r := <-c.executeRequests:
			reqs = append(reqs, r)
		case <-deadline:
			return reqs
		}
	}
}

func drainQueuedStatuses(p *testgit.FakeProvider, grace time.Duration) []string {
	var urls []string
	deadline := time.After(grace)
	for {
		select {
		case s := <-p.Statuses:
			rs, ok := s.Payload.(*github.RepoStatus)
			if ok && rs.GetState() == "pending" {
				urls = append(urls, fmt.Sprintf("%s -> %s", rs.GetContext(), rs.GetTargetURL()))
			}
		case <-deadline:
			return urls
		}
	}
}

func redeliveryWebhookData() *interfaces.WebhookData {
	return &interfaces.WebhookData{
		EventName:               "push",
		TargetRepoURL:           "https://github.com/acme-inc/acme",
		TargetBranch:            "main",
		TargetRepoDefaultBranch: "main",
		PushedRepoURL:           "https://github.com/acme-inc/acme",
		PushedBranch:            "main",
		SHA:                     "c04d68571cb519e095772c865847007ed3e7fea9",
		IsTargetRepoPublic:      true,
	}
}

// A webhook delivery that is sent again with the same X-GitHub-Delivery ID
// (e.g. a "Redeliver" from the GitHub webhook settings page) should not start
// the workflow action a second time.
func TestRedelivery_LegacyWebhook_SameDeliveryIDStartsOnce(t *testing.T) {
	ctx := context.Background()
	u, lis := testhttp.NewServer(t)
	flags.Set(t, "app.build_buddy_url", *u)
	flags.Set(t, "remote_execution.enable_remote_exec", true)
	te := newTestEnv(t)
	ctx, _, gid := authenticate(t, ctx, te)
	execClient := te.GetRemoteExecutionClient().(*fakeExecutionClient)
	go http.Serve(lis, te.GetWorkflowService())
	provider := setupFakeGitProvider(t, te)
	repoURL := makeTempRepo(t)
	runBBServer(ctx, t, te)

	webhookID := "webhook-redelivery"
	legacyWF := &tables.Workflow{
		WorkflowID:           "WF-redelivery",
		WebhookID:            webhookID,
		UserID:               gid,
		GroupID:              gid,
		Perms:                48,
		RepoURL:              repoURL,
		GitProviderWebhookID: testgit.FakeWebhookID,
	}
	require.NoError(t, te.GetDBHandle().NewQuery(context.Background(), "create_legacy_wf_for_test").Create(legacyWF))
	webhookURL := fmt.Sprintf("%s/webhooks/workflow/%s", build_buddy_url.WithPath("").String(), webhookID)

	provider.WebhookData = redeliveryWebhookData()
	provider.FileContents = map[string]string{"buildbuddy.yaml": configWithLinuxWorkflow}
	provider.RegisteredWebhookURL = webhookURL

	codes := []int{}
	for range 2 {
		req, err := http.NewRequest("POST", webhookURL, nil)
		require.NoError(t, err)
		req.Header.Set("X-GitHub-Event", "push")
		req.Header.Set("X-GitHub-Delivery", redeliveryTestDeliveryID)
		rw := httptest.NewRecorder()
		te.GetWorkflowService().ServeHTTP(rw, req)
		codes = append(codes, rw.Code)
	}
	t.Logf("HTTP status codes for the two deliveries: %v", codes)

	reqs := collectExecuteRequests(t, execClient, 10*time.Second)
	statuses := drainQueuedStatuses(provider, 2*time.Second)
	t.Logf("execute requests started: %d", len(reqs))
	for _, s := range statuses {
		t.Logf("pending commit status: %s", s)
	}
	assert.Len(t, reqs, 1, "redelivered webhook (same X-GitHub-Delivery) started the workflow action again")
}

// Same check for the GitHub App path: githubapp.handleWebhookRequest calls
// HandleRepositoryEvent once per delivery with identical WebhookData.
func TestRedelivery_RepositoryEvent_SameEventStartsOnce(t *testing.T) {
	ctx := context.Background()
	u, lis := testhttp.NewServer(t)
	flags.Set(t, "app.build_buddy_url", *u)
	flags.Set(t, "remote_execution.enable_remote_exec", true)
	te := newTestEnv(t)
	ctx, _, gid := authenticate(t, ctx, te)
	execClient := te.GetRemoteExecutionClient().(*fakeExecutionClient)
	go http.Serve(lis, te.GetWorkflowService())
	provider := setupFakeGitProvider(t, te)
	repoURL := makeTempRepo(t)
	runBBServer(ctx, t, te)
	repo := createWorkflow(t, te, repoURL, gid, false)
	provider.FileContents = map[string]string{"buildbuddy.yaml": configWithLinuxWorkflow}

	for range 2 {
		wd := redeliveryWebhookData()
		require.NoError(t, te.GetWorkflowService().HandleRepositoryEvent(ctx, repo, wd, "faketoken"))
	}

	reqs := collectExecuteRequests(t, execClient, 10*time.Second)
	statuses := drainQueuedStatuses(provider, 2*time.Second)
	t.Logf("execute requests started: %d", len(reqs))
	for _, s := range statuses {
		t.Logf("pending commit status: %s", s)
	}
	assert.Len(t, reqs, 1, "repeated repository event for the same commit started the workflow action again")
}

// A config with a misspelled top-level key parses without error, so no
// "Invalid buildbuddy.yaml" status is posted and nothing runs.
func TestConfigTypo_PublishesConfigErrorStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
	}{
		{"top_level_actons", `
actons:
  - name: "Test"
    triggers: { push: { branches: [ "*" ] } }
    steps: [ { run: "bazel test //..." } ]
`},
		{"trigger_pussh", `
actions:
  - name: "Test"
    triggers: { pussh: { branches: [ "*" ] } }
    steps: [ { run: "bazel test //..." } ]
`},
		// Control: a syntax error does produce an error status.
		{"control_syntax_error", "actions: [\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			u, lis := testhttp.NewServer(t)
			flags.Set(t, "app.build_buddy_url", *u)
			flags.Set(t, "remote_execution.enable_remote_exec", true)
			te := newTestEnv(t)
			ctx, _, gid := authenticate(t, ctx, te)
			go http.Serve(lis, te.GetWorkflowService())
			provider := setupFakeGitProvider(t, te)
			repoURL := makeTempRepo(t)
			runBBServer(ctx, t, te)
			repo := createWorkflow(t, te, repoURL, gid, false)
			provider.FileContents = map[string]string{"buildbuddy.yaml": tc.yaml}

			require.NoError(t, te.GetWorkflowService().HandleRepositoryEvent(ctx, repo, redeliveryWebhookData(), "faketoken"))

			var got []string
			deadline := time.After(5 * time.Second)
		loop:
			for {
				select {
				case s := <-provider.Statuses:
					rs := s.Payload.(*github.RepoStatus)
					got = append(got, fmt.Sprintf("%s [%s] %s", rs.GetContext(), rs.GetState(), rs.GetDescription()))
				case <-deadline:
					break loop
				}
			}
			t.Logf("statuses posted: %q", got)
			assert.NotEmpty(t, got, "no commit status was posted for a buildbuddy.yaml that cannot run any action")
		})
	}
}
