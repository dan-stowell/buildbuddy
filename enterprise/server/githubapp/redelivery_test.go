package githubapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/tables"
	"github.com/google/go-github/v59/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingWorkflowService struct {
	interfaces.WorkflowService

	mu     sync.Mutex
	events []*interfaces.WebhookData
}

func (s *recordingWorkflowService) HandleRepositoryEvent(ctx context.Context, repo *tables.GitRepository, wd *interfaces.WebhookData, accessToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, wd)
	return nil
}

const redeliveryPushPayload = `{
  "ref": "refs/heads/main",
  "before": "0000000000000000000000000000000000000000",
  "after": "c04d68571cb519e095772c865847007ed3e7fea9",
  "deleted": false,
  "head_commit": {"id": "c04d68571cb519e095772c865847007ed3e7fea9"},
  "repository": {
    "name": "test-repo",
    "full_name": "test-org/test-repo",
    "private": true,
    "default_branch": "main",
    "clone_url": "https://github.com/test-org/test-repo.git",
    "owner": {"login": "test-org"}
  },
  "installation": {"id": 1234}
}`

// Sending the same delivery (same X-GitHub-Delivery ID and body) twice, as a
// GitHub "Redeliver" does, should only hand the event to the workflow service
// once.
func TestHandleWebhookRequest_RedeliveredPushHandledOnce(t *testing.T) {
	te, ctx := setupEnv(t)
	insertInstallation(t, te, ctx)
	insertRepo(t, te, ctx)
	wfs := &recordingWorkflowService{}
	te.SetWorkflowService(wfs)

	app := newTestApp(te, &fakeAppClient{t: t, wantInstallationID: testInstallationID})
	app.webhookSecret = "test-webhook-secret"

	payload := []byte(redeliveryPushPayload)
	mac := hmac.New(sha256.New, []byte(app.webhookSecret))
	mac.Write(payload)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	var codes []int
	for i := 0; i < 2; i++ {
		req, err := http.NewRequest("POST", "http://localhost/webhooks/github/app", bytes.NewReader(payload))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(github.SHA256SignatureHeader, sig)
		req.Header.Set("X-GitHub-Event", "push")
		req.Header.Set("X-GitHub-Delivery", "6f1c2a10-0000-4000-8000-000000000002")
		w := httptest.NewRecorder()
		app.handleWebhookRequest(w, req)
		codes = append(codes, w.Code)
	}
	t.Logf("HTTP status codes: %v", codes)
	require.Equal(t, []int{http.StatusOK, http.StatusOK}, codes)

	wfs.mu.Lock()
	defer wfs.mu.Unlock()
	for i, wd := range wfs.events {
		t.Logf("HandleRepositoryEvent call %d: event=%s branch=%s sha=%s", i+1, wd.EventName, wd.PushedBranch, wd.SHA)
	}
	assert.Len(t, wfs.events, 1, "redelivered GitHub App webhook was dispatched to the workflow service again")
}
