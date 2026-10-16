package invocation_stat_service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testclickhouse"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/timestamppb"

	ctxpb "github.com/buildbuddy-io/buildbuddy/proto/context"
	inpb "github.com/buildbuddy-io/buildbuddy/proto/invocation"
	inspb "github.com/buildbuddy-io/buildbuddy/proto/invocation_status"
	sfpb "github.com/buildbuddy-io/buildbuddy/proto/stat_filter"
	statspb "github.com/buildbuddy-io/buildbuddy/proto/stats"
	olaptables "github.com/buildbuddy-io/buildbuddy/server/util/clickhouse/schema"
)

func TestGetInvocationStat(t *testing.T) {
	ctx := context.Background()
	te := testenv.GetTestEnv(t)
	testclickhouse.Configure(t, te)
	ta := testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1"))
	te.SetAuthenticator(ta)
	err := te.GetOLAPDBHandle().GORM(ctx, "test_create_invocations").Create([]olaptables.Invocation{
		{
			UpdatedAtUsec:    1000,
			GroupID:          "GR1",
			InvocationUUID:   "b0f0e1d284d742cd8d4ab3e128744f5d",
			RepoURL:          "https://github.com/buildbuddy-io/buildbuddy",
			Success:          true,
			InvocationStatus: int64(inspb.InvocationStatus_COMPLETE_INVOCATION_STATUS),
		},
		{
			UpdatedAtUsec:    2000,
			GroupID:          "GR1",
			InvocationUUID:   "6f64b34bbb17470987de56ee4f3f4813",
			RepoURL:          "https://github.com/buildbuddy-io/protoc-gen-protobufjs",
			Success:          false,
			InvocationStatus: int64(inspb.InvocationStatus_COMPLETE_INVOCATION_STATUS),
		},
	}).Error
	require.NoError(t, err)
	iss := NewInvocationStatService(te, te.GetDBHandle(), te.GetOLAPDBHandle())
	for _, tc := range []struct {
		name      string
		userID    string
		groupID   string
		repoURL   string
		wantStats []*inpb.InvocationStat
		wantErr   error
	}{
		{
			name:    "all builds for group",
			userID:  "US1",
			groupID: "GR1",
			wantStats: []*inpb.InvocationStat{
				{
					Name:                    "GR1",
					LatestBuildTimeUsec:     2000,
					LastGreenBuildUsec:      1000,
					LastRedBuildUsec:        2000,
					TotalNumBuilds:          2,
					TotalNumSucessfulBuilds: 1,
					TotalNumFailingBuilds:   1,
				},
			},
		},
		{
			name:    "filter by normalized repo URL",
			userID:  "US1",
			groupID: "GR1",
			repoURL: " buildbuddy-io/buildbuddy ",
			wantStats: []*inpb.InvocationStat{
				{
					Name:                    "GR1",
					LatestBuildTimeUsec:     1000,
					LastGreenBuildUsec:      1000,
					LastRedBuildUsec:        0,
					TotalNumBuilds:          1,
					TotalNumSucessfulBuilds: 1,
				},
			},
			wantErr: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var requestContext *ctxpb.RequestContext
			if tc.userID != "" {
				authCtx, err := ta.WithAuthenticatedUser(ctx, tc.userID)
				require.NoError(t, err)
				ctx = authCtx
				requestContext = testauth.RequestContext(tc.userID, tc.groupID)
			}
			rsp, err := iss.GetInvocationStat(ctx, &inpb.GetInvocationStatRequest{
				RequestContext:  requestContext,
				AggregationType: inpb.AggType_GROUP_ID_AGGREGATION_TYPE,
				Query: &inpb.InvocationStatQuery{
					RepoUrl: tc.repoURL,
				},
			})
			if tc.wantErr != nil {
				require.Error(t, err)
				require.Equal(t, tc.wantErr, err)
			} else {
				require.NoError(t, err)
				require.Empty(t, cmp.Diff(tc.wantStats, rsp.GetInvocationStat(), protocmp.Transform()))
			}
		})
	}
}

func TestGetStatDrilldown(t *testing.T) {
	flags.Set(t, "app.trends_heatmap_enabled", true)
	te := testenv.GetTestEnv(t)
	testclickhouse.Configure(t, te)

	ta := testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1"))
	te.SetAuthenticator(ta)

	ctx, err := ta.WithAuthenticatedUser(context.Background(), "US1")
	require.NoError(t, err)

	reqCtx := testauth.RequestContext("US1", "GR1")

	iss := NewInvocationStatService(te, te.GetDBHandle(), te.GetOLAPDBHandle())
	_, err = iss.GetStatDrilldown(ctx, &statspb.GetStatDrilldownRequest{
		RequestContext: reqCtx,
	})
	require.True(t, status.IsInvalidArgumentError(err))
}

func TestGetStatHeatmap_LogScale(t *testing.T) {
	flags.Set(t, "app.trends_heatmap_enabled", true)
	ctx := context.Background()
	te := testenv.GetTestEnv(t)
	testclickhouse.Configure(t, te)
	ta := testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1"))
	te.SetAuthenticator(ta)

	// Insert invocations whose durations span four orders of magnitude, so a
	// log-scale heatmap subdivides each decade into 1-2-4-6-8-10 steps.
	windowStart := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	updatedAt := windowStart.Add(time.Hour).UnixMicro()
	durations := []int64{5, 50, 500, 5000}
	invocations := make([]olaptables.Invocation, 0, len(durations))
	uuids := []string{
		"00000000000000000000000000000001",
		"00000000000000000000000000000002",
		"00000000000000000000000000000003",
		"00000000000000000000000000000004",
	}
	for i, d := range durations {
		invocations = append(invocations, olaptables.Invocation{
			UpdatedAtUsec:    updatedAt,
			GroupID:          "GR1",
			InvocationUUID:   uuids[i],
			DurationUsec:     d,
			Success:          true,
			InvocationStatus: int64(inspb.InvocationStatus_COMPLETE_INVOCATION_STATUS),
		})
	}
	err := te.GetOLAPDBHandle().GORM(ctx, "test_create_invocations").Create(invocations).Error
	require.NoError(t, err)

	authCtx, err := ta.WithAuthenticatedUser(ctx, "US1")
	require.NoError(t, err)
	reqCtx := testauth.RequestContext("US1", "GR1")

	durationMetric := sfpb.InvocationMetricType_DURATION_USEC_INVOCATION_METRIC
	iss := NewInvocationStatService(te, te.GetDBHandle(), te.GetOLAPDBHandle())
	rsp, err := iss.GetStatHeatmap(authCtx, &statspb.GetStatHeatmapRequest{
		RequestContext: reqCtx,
		Metric:         &sfpb.Metric{Invocation: &durationMetric},
		LogScale:       true,
		Query: &statspb.TrendQuery{
			UpdatedAfter:  timestamppb.New(windowStart),
			UpdatedBefore: timestamppb.New(windowStart.Add(2 * time.Hour)),
		},
	})
	require.NoError(t, err)

	// The range spans 4 powers of 10, so each decade is subdivided into
	// 1-2-4-6-8-10 steps and we trim down to only buckets that enclose
	// the metric range of [5, 5000].
	require.Equal(t, []int64{4, 6, 8, 10, 20, 40, 60, 80, 100, 200, 400, 600, 800, 1000, 2000, 4000, 6000}, rsp.GetBucketBracket())
	require.False(t, rsp.GetMetricHadNegativeValues())

	// Every invocation should be counted exactly once across all buckets.
	var valueCount int64
	for _, col := range rsp.GetColumn() {
		for _, v := range col.GetValue() {
			valueCount += v
		}
	}
	var totalCount int64
	for _, col := range rsp.GetColumn() {
		for _, t := range col.GetTotal() {
			totalCount += t
		}
	}
	require.Equal(t, int64(len(durations)), valueCount)
	require.Equal(t, int64(5555), totalCount)
}

// TestGeneratedDailyTrends compares the production SQL aggregation with an
// independent local-calendar oracle. The fixture is deterministic (no RNG).
func TestGeneratedDailyTrends(t *testing.T) {
	t.Log("deterministic fixture; seed=none")
	te := testenv.GetTestEnv(t)
	testclickhouse.Configure(t, te)
	flags.Set(t, "app.finer_time_buckets", true)
	ta := testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1"))
	te.SetAuthenticator(ta)
	ctx, err := ta.WithAuthenticatedUser(context.Background(), "US1")
	require.NoError(t, err)
	iss := NewInvocationStatService(te, te.GetDBHandle(), te.GetOLAPDBHandle())
	for _, window := range []string{"2024-03-01T03:17:00Z", "2024-10-27T03:17:00Z", "2024-12-23T03:17:00Z"} {
		start, err := time.Parse(time.RFC3339, window)
		require.NoError(t, err)
		end := start.Add(20*24*time.Hour + 37*time.Minute)
		rows := make([]olaptables.Invocation, 0)
		// Include both boundaries and rows outside the requested half-open range.
		for n := -1; n <= 481; n++ {
			rows = append(rows, olaptables.Invocation{GroupID: "GR1", InvocationUUID: fmt.Sprintf("%032x", n+2), UpdatedAtUsec: start.Add(time.Duration(n) * time.Hour).UnixMicro(), DurationUsec: int64(n+2) * 100, ActionCacheHits: int64(n + 2), ActionCacheMisses: 2, ActionCacheUploads: 3, CasCacheHits: 4, CasCacheMisses: 5, CasCacheUploads: 6, TotalDownloadSizeBytes: int64(n+2) * 17, TotalUploadSizeBytes: int64(n+2) * 19, User: fmt.Sprintf("user%d", (n+2)%2), Host: fmt.Sprintf("host%d", (n+2)%3), Success: true, InvocationStatus: 1})
		}
		require.NoError(t, te.GetOLAPDBHandle().GORM(ctx, "generated_rows").Create(rows).Error)
		executions := make([]olaptables.Execution, 0, len(rows))
		for n, r := range rows {
			executions = append(executions, olaptables.Execution{GroupID: r.GroupID, InvocationUUID: r.InvocationUUID, ExecutionUUID: fmt.Sprintf("00000000-0000-0000-0000-%012x", n+1), UpdatedAtUsec: r.UpdatedAtUsec, User: r.User, Host: r.Host, QueuedTimestampUsec: 1000000, WorkerStartTimestampUsec: 1000000 + r.DurationUsec, WorkerCompletedTimestampUsec: 1000000 + 2*r.DurationUsec})
		}
		require.NoError(t, te.GetOLAPDBHandle().GORM(ctx, "generated_executions").Create(executions).Error)

		for _, zone := range []string{"UTC", "America/New_York", "Australia/Lord_Howe", "Asia/Kathmandu"} {
			for _, filters := range []struct{ user, host string }{{"", ""}, {"user0", ""}, {"", "host1"}, {"user0", "host1"}, {"absent", ""}} {
				t.Run(window+"/"+zone+"/"+filters.user+"/"+filters.host, func(t *testing.T) {
					loc, err := time.LoadLocation(zone)
					require.NoError(t, err)
					q := &statspb.TrendQuery{UpdatedAfter: timestamppb.New(start), UpdatedBefore: timestamppb.New(end), User: filters.user, Host: filters.host}
					rc := testauth.RequestContext("US1", "GR1")
					rc.Timezone = zone
					got, err := iss.getInvocationTrend(ctx, &statspb.GetTrendRequest{RequestContext: rc, Query: q}, iss.getTrendTimeSettings(q, zone))
					require.NoError(t, err)
					want := map[int64]*statspb.TrendStat{}
					durations := map[int64][]int64{}
					for _, r := range rows {
						if r.UpdatedAtUsec < start.UnixMicro() || r.UpdatedAtUsec >= end.UnixMicro() || (filters.user != "" && r.User != filters.user) || (filters.host != "" && r.Host != filters.host) {
							continue
						}
						local := time.UnixMicro(r.UpdatedAtUsec).In(loc)
						key := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).UnixMicro()
						if want[key] == nil {
							want[key] = &statspb.TrendStat{}
						}
						w := want[key]
						e := executionByBucket[key]
						require.NotNil(t, e)
						require.Equal(t, g.TotalBuildTimeUsec, e.GetTotalBuildTimeUsec())
						require.Equal(t, []float64{g.BuildTimeUsecP50, g.BuildTimeUsecP75, g.BuildTimeUsecP90, g.BuildTimeUsecP95, g.BuildTimeUsecP99}, []float64{e.GetQueueDurationUsecP50(), e.GetQueueDurationUsecP75(), e.GetQueueDurationUsecP90(), e.GetQueueDurationUsecP95(), e.GetQueueDurationUsecP99()})

						w.TotalNumBuilds++
						w.TotalBuildTimeUsec += r.DurationUsec
						w.ActionCacheHits += r.ActionCacheHits
						w.ActionCacheMisses += r.ActionCacheMisses
						w.ActionCacheUploads += r.ActionCacheUploads
						w.CasCacheHits += r.CasCacheHits
						w.CasCacheMisses += r.CasCacheMisses
						w.CasCacheUploads += r.CasCacheUploads
						w.TotalDownloadSizeBytes += r.TotalDownloadSizeBytes
						w.TotalUploadSizeBytes += r.TotalUploadSizeBytes
						durations[key] = append(durations[key], r.DurationUsec)
					}
					require.Len(t, got, len(want))
					executionGot, err := iss.getExecutionTrend(ctx, &statspb.GetTrendRequest{RequestContext: rc, Query: q}, iss.getTrendTimeSettings(q, zone))
					require.NoError(t, err)
					require.Len(t, executionGot, len(got))
					executionByBucket := map[int64]*statspb.ExecutionStat{}
					for _, e := range executionGot {
						executionByBucket[e.GetBucketStartTimeMicros()] = e
					}

					for _, g := range got {
						key := g.GetBucketStartTimeMicros()
						w := want[key]
						e := executionByBucket[key]
						require.NotNil(t, e)
						require.Equal(t, g.TotalBuildTimeUsec, e.GetTotalBuildTimeUsec())
						require.Equal(t, []float64{g.BuildTimeUsecP50, g.BuildTimeUsecP75, g.BuildTimeUsecP90, g.BuildTimeUsecP95, g.BuildTimeUsecP99}, []float64{e.GetQueueDurationUsecP50(), e.GetQueueDurationUsecP75(), e.GetQueueDurationUsecP90(), e.GetQueueDurationUsecP95(), e.GetQueueDurationUsecP99()})

						require.NotNil(t, w, "unexpected bucket %v", time.UnixMicro(key))
						require.Equal(t, []int64{w.TotalNumBuilds, w.TotalBuildTimeUsec, w.ActionCacheHits, w.ActionCacheMisses, w.ActionCacheUploads, w.CasCacheHits, w.CasCacheMisses, w.CasCacheUploads, w.TotalDownloadSizeBytes, w.TotalUploadSizeBytes}, []int64{g.TotalNumBuilds, g.TotalBuildTimeUsec, g.ActionCacheHits, g.ActionCacheMisses, g.ActionCacheUploads, g.CasCacheHits, g.CasCacheMisses, g.CasCacheUploads, g.TotalDownloadSizeBytes, g.TotalUploadSizeBytes})
						ds := durations[key]
						slices.Sort(ds)
						for n, p := range []float64{.5, .75, .9, .95, .99} {
							// Exclusive quantiles use rank p*(N+1), clamped to endpoints.
							rank := p*float64(len(ds)+1) - 1
							var expected float64
							if rank <= 0 {
								expected = float64(ds[0])
							} else if rank >= float64(len(ds)-1) {
								expected = float64(ds[len(ds)-1])
							} else {
								lo := int(rank)
								expected = float64(ds[lo]) + (rank-float64(lo))*float64(ds[lo+1]-ds[lo])
							}
							actual := []float64{g.BuildTimeUsecP50, g.BuildTimeUsecP75, g.BuildTimeUsecP90, g.BuildTimeUsecP95, g.BuildTimeUsecP99}[n]
							require.InDelta(t, expected, actual, 1e-6, "p=%v", p)
						}
					}
				})
			}
		}
	}
}

// A heatmap must enclose its data in the timestamp brackets it returns, even
// when the caller specifies a partial second at the edge of the window.
func TestHeatmapPartialSecondBounds(t *testing.T) {
	t.Log("deterministic fixture; seed=none")
	flags.Set(t, "app.trends_heatmap_enabled", true)
	te := testenv.GetTestEnv(t)
	testclickhouse.Configure(t, te)
	ta := testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1"))
	te.SetAuthenticator(ta)
	ctx, err := ta.WithAuthenticatedUser(context.Background(), "US1")
	require.NoError(t, err)
	start := time.Date(2024, 12, 31, 23, 59, 59, 100000000, time.UTC)
	end := start.Add(800 * time.Millisecond)
	updated := start.Add(400 * time.Millisecond)
	require.NoError(t, te.GetOLAPDBHandle().GORM(ctx, "partial_second_row").Create(&olaptables.Invocation{
		GroupID: "GR1", InvocationUUID: "00000000000000000000000000000001", UpdatedAtUsec: updated.UnixMicro(), DurationUsec: 100, Success: true, InvocationStatus: 1,
	}).Error)
	metric := sfpb.InvocationMetricType_DURATION_USEC_INVOCATION_METRIC
	iss := NewInvocationStatService(te, te.GetDBHandle(), te.GetOLAPDBHandle())
	rsp, err := iss.GetStatHeatmap(ctx, &statspb.GetStatHeatmapRequest{RequestContext: testauth.RequestContext("US1", "GR1"), Metric: &sfpb.Metric{Invocation: &metric}, Query: &statspb.TrendQuery{UpdatedAfter: timestamppb.New(start), UpdatedBefore: timestamppb.New(end)}})
	require.NoError(t, err)
	var count int64
	for _, col := range rsp.GetColumn() {
		for _, value := range col.GetValue() {
			count += value
		}
	}
	require.Equal(t, int64(1), count)
	brackets := rsp.GetTimestampBracket()
	require.GreaterOrEqual(t, len(brackets), 2)
	require.Equal(t, start.UnixMicro(), brackets[0], "first bracket must preserve inclusive request start")
	require.Equal(t, end.UnixMicro(), brackets[len(brackets)-1], "last bracket must preserve exclusive request end")
	require.Less(t, updated.UnixMicro(), brackets[len(brackets)-1], "returned data must precede final bracket")
}

// Each bit independently varies a dimension, so combined filters cannot pass
// accidentally because the fixture correlated all its dimensions.
func TestGeneratedTrendFilters(t *testing.T) {
	t.Log("deterministic exhaustive bit fixture; seed=none")
	te := testenv.GetTestEnv(t)
	testclickhouse.Configure(t, te)
	flags.Set(t, "app.finer_time_buckets", false)
	ta := testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1"))
	te.SetAuthenticator(ta)
	ctx, err := ta.WithAuthenticatedUser(context.Background(), "US1")
	require.NoError(t, err)
	start := time.Date(2024, 12, 31, 12, 0, 0, 0, time.UTC)
	rows := make([]olaptables.Invocation, 0, 1024)
	for n := range 1024 {
		val := func(bit int) string { return fmt.Sprint((n >> bit) & 1) }
		rows = append(rows, olaptables.Invocation{GroupID: "GR1", InvocationUUID: fmt.Sprintf("%032x", n+1), UpdatedAtUsec: start.Add(time.Duration(n) * time.Microsecond).UnixMicro(), DurationUsec: int64(n + 1), User: "user" + val(0), Host: "host" + val(1), RepoURL: "https://example.com/repo" + val(2), BranchName: "branch" + val(3), CommitSHA: "commit" + val(4), Command: "command" + val(5), Role: "role" + val(6), Pattern: "//pattern" + val(7), Tags: []string{"tag" + val(8), "common"}, Success: (n & 512) == 0, InvocationStatus: 1})
	}
	require.NoError(t, te.GetOLAPDBHandle().GORM(ctx, "filter_rows").Create(rows).Error)
	for _, r := range rows {
		require.NoError(t, te.GetDBHandle().NewQuery(ctx, "sql_filter_rows").Raw(`INSERT INTO "Invocations" (invocation_id,group_id,updated_at_usec,duration_usec,"user",host,repo_url,branch_name,commit_sha,command,role,pattern,success,invocation_status) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, r.InvocationUUID, r.GroupID, r.UpdatedAtUsec, r.DurationUsec, r.User, r.Host, r.RepoURL, r.BranchName, r.CommitSHA, r.Command, r.Role, r.Pattern, r.Success, r.InvocationStatus).Exec().Error)
	}

	iss := NewInvocationStatService(te, te.GetDBHandle(), te.GetOLAPDBHandle())
	dimensions := []string{"user", "host", "repo", "branch", "commit", "command", "role", "pattern", "tags", "status"}
	cases := append([]string{"all", "combined", "empty", "roles_or", "tags_and", "status_or"}, dimensions...)
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			q := &statspb.TrendQuery{UpdatedAfter: timestamppb.New(start), UpdatedBefore: timestamppb.New(start.Add(time.Hour))}
			selected := func(dim string) bool { return name == dim || name == "combined" }
			if selected("user") {
				q.User = "user1"
			}
			if selected("host") {
				q.Host = "host1"
			}
			if selected("repo") {
				q.RepoUrl = "https://example.com/repo1"
			}
			if selected("branch") {
				q.BranchName = "branch1"
			}
			if selected("commit") {
				q.CommitSha = "commit1"
			}
			if selected("command") {
				q.Command = "command1"
			}
			if selected("role") {
				q.Role = []string{"role1"}
			}
			if selected("pattern") {
				q.Pattern = "//pattern1"
			}
			if selected("tags") {
				q.Tags = []string{"tag1"}
			}
			if selected("status") {
				q.Status = []inspb.OverallStatus{inspb.OverallStatus_FAILURE}
			}
			if name == "empty" {
				q.User = "absent"
			}
			if name == "roles_or" {
				q.Role = []string{"role0", "role1"}
			}
			if name == "tags_and" {
				q.Tags = []string{"tag1", "common"}
			}
			if name == "status_or" {
				q.Status = []inspb.OverallStatus{inspb.OverallStatus_SUCCESS, inspb.OverallStatus_FAILURE}
			}
			var wantCount, wantDuration int64
			for _, r := range rows {
				matches := true
				for _, pair := range [][2]string{{q.User, r.User}, {q.Host, r.Host}, {q.RepoUrl, r.RepoURL}, {q.BranchName, r.BranchName}, {q.CommitSha, r.CommitSHA}, {q.Command, r.Command}, {q.Pattern, r.Pattern}} {
					if pair[0] != "" && pair[0] != pair[1] {
						matches = false
					}
				}
				if len(q.Role) > 0 && !slices.Contains(q.Role, r.Role) {
					matches = false
				}
				for _, tag := range q.Tags {
					if !slices.Contains(r.Tags, tag) {
						matches = false
					}
				}
				status := inspb.OverallStatus_FAILURE
				if r.Success {
					status = inspb.OverallStatus_SUCCESS
				}
				if len(q.Status) > 0 && !slices.Contains(q.Status, status) {
					matches = false
				}
				if matches {
					wantCount++
					wantDuration += r.DurationUsec
				}
			}
			rc := testauth.RequestContext("US1", "GR1")
			got, err := iss.getInvocationTrend(ctx, &statspb.GetTrendRequest{RequestContext: rc, Query: q}, iss.getTrendTimeSettings(q, "UTC"))
			require.NoError(t, err)
			var count, duration int64
			for _, g := range got {
				count += g.TotalNumBuilds
				duration += g.TotalBuildTimeUsec
			}
			require.Equal(t, wantCount, count, strings.Join([]string{name, "count"}, " "))
			require.Equal(t, wantDuration, duration)
			// The SQL fallback supports these dimensions except tags. It also has
			// no percentile support, so compare the common count/duration fields.
			if len(q.Tags) == 0 {
				flags.Set(t, "app.enable_read_from_olap_db", false)
				sqlGot, err := iss.getInvocationTrend(ctx, &statspb.GetTrendRequest{RequestContext: rc, Query: q}, iss.getTrendTimeSettings(q, "UTC"))
				require.NoError(t, err)
				var sqlCount, sqlDuration int64
				for _, g := range sqlGot {
					sqlCount += g.TotalNumBuilds
					sqlDuration += g.TotalBuildTimeUsec
				}
				require.Equal(t, wantCount, sqlCount, "SQL count")
				require.Equal(t, wantDuration, sqlDuration, "SQL duration")
			}

		})
	}
}
