package server_notification_test

import (
	"context"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/server_notification"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/testutil/testredis"
	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/testing/protocmp"

	snpb "github.com/buildbuddy-io/buildbuddy/proto/server_notification"
)

func TestPubSub(t *testing.T) {
	h := testredis.Start(t)
	app1 := server_notification.New("app", h.Client())
	app2 := server_notification.New("app", h.Client())
	ch1 := app1.Subscribe(&snpb.InvalidateIPRulesCache{})
	ch2 := app2.Subscribe(&snpb.InvalidateIPRulesCache{})

	ctx := context.Background()
	// New subscribes asynchronously. Wait until Redis has registered both
	// subscribers so the published message can reach both services.
	rdb := h.Client()
	require.Eventually(t, func() bool {
		counts, err := rdb.PubSubNumSub(ctx, "serverNotifications/app").Result()
		return err == nil && counts["serverNotifications/app"] == 2
	}, 10*time.Second, 5*time.Millisecond, "notification channel should have two subscribers")

	// Publish a message.
	msg := &snpb.InvalidateIPRulesCache{GroupId: "123"}
	err := app1.Publish(ctx, msg)
	require.NoError(t, err)

	// Make sure it's received by both subscribers.
	require.Empty(t, cmp.Diff(msg, <-ch1, protocmp.Transform()))
	require.Empty(t, cmp.Diff(msg, <-ch2, protocmp.Transform()))
}
