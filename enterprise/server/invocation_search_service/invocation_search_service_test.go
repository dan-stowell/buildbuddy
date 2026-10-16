package invocation_search_service_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	inspb "github.com/buildbuddy-io/buildbuddy/proto/invocation_status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/invocation_search_service"
	"github.com/buildbuddy-io/buildbuddy/server/real_environment"
	"github.com/buildbuddy-io/buildbuddy/server/tables"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testauth"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testclickhouse"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/perms"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ctxpb "github.com/buildbuddy-io/buildbuddy/proto/context"
	inpb "github.com/buildbuddy-io/buildbuddy/proto/invocation"
	sfpb "github.com/buildbuddy-io/buildbuddy/proto/stat_filter"
)

func getUUIDBytes(id byte) []byte {
	return []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, id}
}

func getUUIDString(id byte) string {
	return fmt.Sprintf("00000000-0000-0000-0000-0000000000%02x", id)
}

func setUpFakeData(ctx context.Context, env *real_environment.RealEnv, t *testing.T) {
	for _, row := range []*tables.Invocation{
		{
			InvocationID:     getUUIDString(0),
			InvocationUUID:   getUUIDBytes(0),
			User:             "jdhollen",
			Perms:            perms.GROUP_READ,
			InvocationStatus: 1, /* COMPLETE */
			GroupID:          "GR1",
		},
		{
			InvocationID:     getUUIDString(1),
			InvocationUUID:   getUUIDBytes(1),
			User:             "jdhollen",
			Perms:            perms.GROUP_READ,
			InvocationStatus: 3, /* DISCONNECTED */
			GroupID:          "GR1",
		},
		{
			InvocationID:     getUUIDString(2),
			InvocationUUID:   getUUIDBytes(2),
			User:             "jdhollen",
			Perms:            perms.GROUP_READ,
			InvocationStatus: 2, /* PARTIAL */
			GroupID:          "GR1",
		},
		{
			InvocationID:     getUUIDString(3),
			InvocationUUID:   getUUIDBytes(3),
			User:             "sluongng",
			Perms:            perms.GROUP_READ,
			InvocationStatus: 1, /* COMPLETE */
			GroupID:          "GR1",
		},
		{
			InvocationID:     getUUIDString(4),
			InvocationUUID:   getUUIDBytes(4),
			User:             "octocat",
			Perms:            perms.GROUP_READ,
			InvocationStatus: 1, /* COMPLETE */
			GroupID:          "GR2",
		},
		{
			InvocationID:     getUUIDString(5),
			InvocationUUID:   getUUIDBytes(5),
			User:             "jdhollen",
			Perms:            perms.GROUP_READ,
			InvocationStatus: 1, /* COMPLETE */
			GroupID:          "GR1",
		},
		{
			InvocationID:     getUUIDString(6),
			InvocationUUID:   getUUIDBytes(6),
			User:             "jdhollen",
			Perms:            perms.GROUP_READ,
			InvocationStatus: 1, /* COMPLETE */
			GroupID:          "GR1",
		},
		{
			InvocationID:     getUUIDString(7),
			InvocationUUID:   getUUIDBytes(7),
			User:             "jdhollen",
			Perms:            perms.GROUP_READ,
			InvocationStatus: 1, /* COMPLETE */
			GroupID:          "GR1",
		},
	} {
		var asCreated tables.Invocation
		err := env.GetDBHandle().NewQuery(ctx, "test").Create(row)
		require.NoError(t, err)
		err = env.GetDBHandle().GORM(ctx, "test").Where("invocation_id = ?", row.InvocationID).First(&asCreated).Error
		require.NoError(t, err)

		// Don't mirror inv. 6 over to OLAP, for fun.  Otherwise, don't sync partial / disconnected invocations to clickhouse.
		if row.InvocationID == getUUIDString(6) || row.InvocationStatus == 2 || row.InvocationStatus == 3 {
			continue
		}
		if env.GetOLAPDBHandle() != nil {
			err := env.GetOLAPDBHandle().FlushInvocationStats(ctx, &asCreated)
			require.NoError(t, err)
		}
	}
}

func setUpDB(ctx context.Context, env *real_environment.RealEnv, t *testing.T) *testauth.TestAuthenticator {
	// Disable async invocation batch inserts.
	flags.Set(t, "olap_database.invocation_batch_insert_interval", 0)
	flags.Set(t, "olap_database.async_insert", true)

	ta := testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1", "US2", "GR2"))
	env.SetAuthenticator(ta)
	setUpFakeData(ctx, env, t)
	return ta
}

func getInvocationIDSlice(rsp *inpb.SearchInvocationResponse) []string {
	out := make([]string, 0)
	for _, inv := range rsp.Invocation {
		out = append(out, inv.InvocationId)
	}
	return out
}

func TestNonOLAPQuery(t *testing.T) {
	bgCtx := context.Background()
	env := testenv.GetTestEnv(t)
	ta := setUpDB(bgCtx, env, t)

	testCtx, err := ta.WithAuthenticatedUser(bgCtx, "US1")
	require.NoError(t, err)

	service := invocation_search_service.NewInvocationSearchService(env, env.GetDBHandle(), env.GetOLAPDBHandle())

	rsp, err := service.QueryInvocations(testCtx, &inpb.SearchInvocationRequest{
		RequestContext: &ctxpb.RequestContext{GroupId: "GR1"},
		Query: &inpb.InvocationQuery{
			User: "jdhollen",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{getUUIDString(7), getUUIDString(6), getUUIDString(5), getUUIDString(2), getUUIDString(1), getUUIDString(0)}, getInvocationIDSlice(rsp))

	rsp, err = service.QueryInvocations(testCtx, &inpb.SearchInvocationRequest{
		RequestContext: &ctxpb.RequestContext{GroupId: "GR1"},
		Query: &inpb.InvocationQuery{
			User: "sluongng",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{getUUIDString(3)}, getInvocationIDSlice(rsp))
}

func TestGroupIDDefaultsToAuthenticatedGroupID(t *testing.T) {
	env := testenv.GetTestEnv(t)
	ta := setUpDB(t.Context(), env, t)

	service := invocation_search_service.NewInvocationSearchService(env, env.GetDBHandle(), env.GetOLAPDBHandle())

	for _, tc := range []struct {
		name                  string
		user                  string
		query                 *inpb.InvocationQuery
		expectedInvocationIDs []string
		expectedError         func(err error) bool
	}{
		{
			name:  "nil query",
			user:  "US1",
			query: nil,
			expectedInvocationIDs: []string{
				getUUIDString(0),
				getUUIDString(1),
				getUUIDString(2),
				getUUIDString(3),
				// Note: invocation 00..04 is not owned by US1's group
				getUUIDString(5),
				getUUIDString(6),
				getUUIDString(7),
			},
		},
		{
			name:  "empty query",
			user:  "US1",
			query: &inpb.InvocationQuery{},
			expectedInvocationIDs: []string{
				getUUIDString(0),
				getUUIDString(1),
				getUUIDString(2),
				getUUIDString(3),
				getUUIDString(5),
				getUUIDString(6),
				getUUIDString(7),
			},
		},
		{
			name:  "nonempty query",
			user:  "US1",
			query: &inpb.InvocationQuery{User: "jdhollen"},
			expectedInvocationIDs: []string{
				getUUIDString(0),
				getUUIDString(1),
				getUUIDString(2),
				getUUIDString(5),
				getUUIDString(6),
				getUUIDString(7),
			},
		},
		{
			name:          "unauthenticated",
			user:          "",
			query:         nil,
			expectedError: status.IsInternalError,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.user != "" {
				var err error
				ctx, err = ta.WithAuthenticatedUser(t.Context(), tc.user)
				require.NoError(t, err)
			}

			req := &inpb.SearchInvocationRequest{
				RequestContext: &ctxpb.RequestContext{GroupId: "GR1"},
				Query:          tc.query,
			}
			rsp, err := service.QueryInvocations(ctx, req)

			if tc.expectedError != nil {
				require.True(t, tc.expectedError(err))
			} else {
				require.NoError(t, err)
				assert.ElementsMatch(t, tc.expectedInvocationIDs, getInvocationIDSlice(rsp))
			}
		})
	}
}

func TestBlendedOLAPQuery(t *testing.T) {
	flags.Set(t, "app.blended_invocation_search_enabled", true)
	bgCtx := context.Background()
	env := testenv.GetTestEnv(t)
	testclickhouse.Configure(t, env)
	ta := setUpDB(bgCtx, env, t)

	testCtx, err := ta.WithAuthenticatedUser(bgCtx, "US1")
	require.NoError(t, err)

	service := invocation_search_service.NewInvocationSearchService(env, env.GetDBHandle(), env.GetOLAPDBHandle())

	rsp, err := service.QueryInvocations(testCtx, &inpb.SearchInvocationRequest{
		RequestContext: &ctxpb.RequestContext{GroupId: "GR1"},
		Query: &inpb.InvocationQuery{
			User: "jdhollen",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{getUUIDString(7), getUUIDString(5), getUUIDString(2), getUUIDString(1), getUUIDString(0)}, getInvocationIDSlice(rsp))

	rsp, err = service.QueryInvocations(testCtx, &inpb.SearchInvocationRequest{
		RequestContext: &ctxpb.RequestContext{GroupId: "GR1"},
		Query: &inpb.InvocationQuery{
			User: "sluongng",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{getUUIDString(3)}, getInvocationIDSlice(rsp))
}

func TestUnblendedOLAPQuery(t *testing.T) {
	flags.Set(t, "app.olap_invocation_search_enabled", true)
	bgCtx := context.Background()
	env := testenv.GetTestEnv(t)
	testclickhouse.Configure(t, env)
	ta := setUpDB(bgCtx, env, t)

	testCtx, err := ta.WithAuthenticatedUser(bgCtx, "US1")
	require.NoError(t, err)

	service := invocation_search_service.NewInvocationSearchService(env, env.GetDBHandle(), env.GetOLAPDBHandle())

	metric := sfpb.InvocationMetricType_UPDATED_AT_USEC_INVOCATION_METRIC
	bogusMetricMin := int64(0)
	rsp, err := service.QueryInvocations(testCtx, &inpb.SearchInvocationRequest{
		RequestContext: &ctxpb.RequestContext{GroupId: "GR1"},
		Query: &inpb.InvocationQuery{
			User: "jdhollen",
			// Including this filter will cause this request path to hit Clickhouse.
			Filter: []*sfpb.StatFilter{&sfpb.StatFilter{
				Metric: &sfpb.Metric{Invocation: &metric},
				Min:    &bogusMetricMin,
			}},
		},
	})
	require.NoError(t, err)
	// Pending / disconnected builds should be missing.
	assert.Equal(t, []string{getUUIDString(7), getUUIDString(5), getUUIDString(0)}, getInvocationIDSlice(rsp))

	rsp, err = service.QueryInvocations(testCtx, &inpb.SearchInvocationRequest{
		RequestContext: &ctxpb.RequestContext{GroupId: "GR1"},
		Query: &inpb.InvocationQuery{
			User: "sluongng",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{getUUIDString(3)}, getInvocationIDSlice(rsp))
}

// paginationFixture creates exact timestamps after insertion because Model's
// BeforeCreate hook replaces supplied timestamps. No wall clock or randomness
// participates in the generated ordering.
func paginationFixture(t *testing.T, n int, tied bool, olap bool) (context.Context, *real_environment.RealEnv, *invocation_search_service.InvocationSearchService) {
	t.Helper()
	env := testenv.GetTestEnv(t)
	if olap {
		testclickhouse.Configure(t, env)
		flags.Set(t, "olap_database.invocation_batch_insert_interval", 0)
		flags.Set(t, "olap_database.async_insert", true)
	}
	ta := testauth.NewTestAuthenticator(t, testauth.TestUsers("US1", "GR1"))
	env.SetAuthenticator(ta)
	ctx, err := ta.WithAuthenticatedUser(context.Background(), "US1")
	require.NoError(t, err)
	for i := range n {
		row := &tables.Invocation{InvocationID: getUUIDString(byte(i)), InvocationUUID: getUUIDBytes(byte(i)), GroupID: "GR1", Perms: perms.GROUP_READ, User: "pager", RepoURL: "https://github.com/example/repo", BranchName: "main", CommitSHA: "abc", InvocationStatus: 1, Success: true}
		require.NoError(t, env.GetDBHandle().NewQuery(ctx, "pagination_insert").Create(row))
		stamp := int64(1700000000000000 + i*1000000)
		if tied {
			stamp = 1700000000000000 + int64(i/3)*1000000
		}
		require.NoError(t, env.GetDBHandle().GORM(ctx, "pagination_timestamp").Exec("UPDATE Invocations SET created_at_usec = ?, updated_at_usec = ? WHERE invocation_id = ?", stamp, stamp, row.InvocationID).Error)
		row.CreatedAtUsec, row.UpdatedAtUsec = stamp, stamp
		if olap {
			require.NoError(t, env.GetOLAPDBHandle().FlushInvocationStats(ctx, row))
		}
	}
	return ctx, env, invocation_search_service.NewInvocationSearchService(env, env.GetDBHandle(), env.GetOLAPDBHandle())
}

func TestPaginationStatic(t *testing.T) {
	for _, backend := range []string{"sqlite", "olap"} {
		for _, tied := range []bool{false, true} {
			for n := 0; n <= 7; n++ {
				t.Run(fmt.Sprintf("%s/tied=%t/n=%d", backend, tied, n), func(t *testing.T) {
					olap := backend == "olap"
					flags.Set(t, "app.blended_invocation_search_enabled", false)
					flags.Set(t, "app.olap_invocation_search_enabled", olap)
					ctx, env, service := paginationFixture(t, n, tied, olap)
					// Interleave one exclusion for each filter with matching timestamps.
					for excluded := range 8 {
						row := &tables.Invocation{InvocationID: getUUIDString(byte(100 + excluded)), InvocationUUID: getUUIDBytes(byte(100 + excluded)), GroupID: "GR1", Perms: perms.GROUP_READ, User: "pager", RepoURL: "https://github.com/example/repo", BranchName: "main", CommitSHA: "abc", InvocationStatus: 1, Success: true}
						stamp := int64(1700000001000000)
						switch excluded {
						case 0:
							row.User = "other"
						case 1:
							row.RepoURL = "https://github.com/example/other"
						case 2:
							row.BranchName = "other"
						case 3:
							row.CommitSHA = "other"
						case 4:
							row.Success = false
						case 5:
							stamp = 1699999999999999
						case 6:
							stamp = 1700000007000000
						case 7:
							row.GroupID = "GR2"
						}
						require.NoError(t, env.GetDBHandle().NewQuery(ctx, "pagination_excluded").Create(row))
						require.NoError(t, env.GetDBHandle().GORM(ctx, "pagination_timestamp").Exec("UPDATE Invocations SET created_at_usec = ?, updated_at_usec = ? WHERE invocation_id = ?", stamp, stamp, row.InvocationID).Error)
						row.CreatedAtUsec, row.UpdatedAtUsec = stamp, stamp
						if olap {
							require.NoError(t, env.GetOLAPDBHandle().FlushInvocationStats(ctx, row))
						}
					}
					for size := 1; size <= 4; size++ {
						for _, ascending := range []bool{false, true} {
							for _, field := range []inpb.InvocationSort_SortField{inpb.InvocationSort_UPDATED_AT_USEC_SORT_FIELD, inpb.InvocationSort_CREATED_AT_USEC_SORT_FIELD} {
								t.Run(fmt.Sprintf("size=%d/asc=%t/field=%d", size, ascending, field), func(t *testing.T) {
									req := &inpb.SearchInvocationRequest{Count: int32(size), Sort: &inpb.InvocationSort{SortField: field, Ascending: ascending}, Query: &inpb.InvocationQuery{
										User: "pager", RepoUrl: "https://github.com/example/repo", BranchName: "main", CommitSha: "abc", Status: []inspb.OverallStatus{inspb.OverallStatus_SUCCESS},
										UpdatedAfter: timestamppb.New(time.UnixMicro(1700000000000000)), UpdatedBefore: timestamppb.New(time.UnixMicro(1700000007000000)),
									}}
									// A nonempty stat filter selects the unblended OLAP path.
									if olap {
										metric, min := sfpb.InvocationMetricType_UPDATED_AT_USEC_INVOCATION_METRIC, int64(0)
										req.Query.Filter = []*sfpb.StatFilter{{Metric: &sfpb.Metric{Invocation: &metric}, Min: &min}}
									}
									var ids []string
									var timestamps []int64
									for page := 0; ; page++ {
										require.LessOrEqual(t, page, n+1, "pagination must terminate")
										rsp, err := service.QueryInvocations(ctx, req)
										require.NoError(t, err)
										for _, inv := range rsp.Invocation {
											ids = append(ids, inv.InvocationId)
											timestamps = append(timestamps, inv.UpdatedAtUsec)
										}
										if rsp.NextPageToken == "" {
											break
										}
										require.NotEqual(t, req.PageToken, rsp.NextPageToken, "cursor must advance")
										req.PageToken = rsp.NextPageToken
									}
									want := make([]string, n)
									for i := range want {
										want[i] = getUUIDString(byte(i))
									}
									require.ElementsMatch(t, want, ids, "all matching invocations exactly once")
									for i := 1; i < len(timestamps); i++ {
										if ascending {
											require.LessOrEqual(t, timestamps[i-1], timestamps[i])
										} else {
											require.GreaterOrEqual(t, timestamps[i-1], timestamps[i])
										}
									}
								})
							}
						}
					}
				})
			}
		}
	}
}

func TestPaginationBlendedSingleItemPageAdvances(t *testing.T) {
	flags.Set(t, "app.blended_invocation_search_enabled", true)
	ctx, _, service := paginationFixture(t, 2, false, true)
	req := &inpb.SearchInvocationRequest{Count: 1, Query: &inpb.InvocationQuery{User: "pager"}}
	first, err := service.QueryInvocations(ctx, req)
	require.NoError(t, err)
	req.PageToken = first.NextPageToken
	require.NotEmpty(t, req.PageToken)
	second, err := service.QueryInvocations(ctx, req)
	require.NoError(t, err)
	require.NotEqual(t, first.NextPageToken, second.NextPageToken, "a nonterminal page must advance; first=%v second=%v", first, second)
}

func TestPaginationMutationDoesNotRepeatOrSkip(t *testing.T) {
	for _, mutation := range []string{"insert_before_cursor", "update_unreturned_before_cursor"} {
		t.Run(mutation, func(t *testing.T) {
			ctx, env, service := paginationFixture(t, 3, false, false)
			req := &inpb.SearchInvocationRequest{Count: 1, Query: &inpb.InvocationQuery{User: "pager"}}
			first, err := service.QueryInvocations(ctx, req)
			require.NoError(t, err)
			require.Equal(t, []string{getUUIDString(2)}, getInvocationIDSlice(first))
			if mutation == "insert_before_cursor" {
				require.NoError(t, env.GetDBHandle().NewQuery(ctx, "pagination_insert").Create(&tables.Invocation{InvocationID: getUUIDString(3), GroupID: "GR1", Perms: perms.GROUP_READ, User: "pager", InvocationStatus: 1}))
			} else {
				require.NoError(t, env.GetDBHandle().GORM(ctx, "pagination_update").Exec("UPDATE Invocations SET updated_at_usec = ? WHERE invocation_id = ?", int64(1700000010000000), getUUIDString(0)).Error)
			}
			seen := getInvocationIDSlice(first)
			req.PageToken = first.NextPageToken
			for page := 0; req.PageToken != ""; page++ {
				require.Less(t, page, 5)
				rsp, err := service.QueryInvocations(ctx, req)
				require.NoError(t, err)
				seen = append(seen, getInvocationIDSlice(rsp)...)
				req.PageToken = rsp.NextPageToken
			}
			// Newly inserted rows may be outside the initial snapshot. Every row
			// present at the start must still occur exactly once.
			for i := range 3 {
				count := 0
				for _, id := range seen {
					if id == getUUIDString(byte(i)) {
						count++
					}
				}
				assert.Equal(t, 1, count, "original invocation %d; sequence=%v", i, seen)
			}
		})
	}
}

func TestPaginationInvalidTokens(t *testing.T) {
	ctx, _, service := paginationFixture(t, 3, false, false)
	for _, token := range []string{"garbage", "offset_", "offset_no", "offset_9223372036854775808", "offset_-1"} {
		t.Run(token, func(t *testing.T) {
			_, err := service.QueryInvocations(ctx, &inpb.SearchInvocationRequest{Count: 1, PageToken: token, Query: &inpb.InvocationQuery{User: "pager"}})
			require.True(t, status.IsInvalidArgumentError(err), "token %q: expected InvalidArgument, got %v", token, err)
		})
	}
	t.Run("different_query", func(t *testing.T) {
		first, err := service.QueryInvocations(ctx, &inpb.SearchInvocationRequest{Count: 1, Query: &inpb.InvocationQuery{User: "pager"}})
		require.NoError(t, err)
		require.NotEmpty(t, first.NextPageToken)
		_, err = service.QueryInvocations(ctx, &inpb.SearchInvocationRequest{Count: 1, PageToken: first.NextPageToken, Query: &inpb.InvocationQuery{User: "someone_else"}})
		require.True(t, status.IsInvalidArgumentError(err), "token from another query must be rejected; got %v", err)
	})
}
