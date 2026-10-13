package schema

import (
	"reflect"
	"slices"
	"testing"

	"github.com/buildbuddy-io/buildbuddy/server/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSchemaInSync(t *testing.T) {
	tests := []struct {
		clickhouseTable Table
		primaryDBTable  any
	}{
		{
			clickhouseTable: &Invocation{},
			primaryDBTable:  tables.Invocation{},
		},
		{
			clickhouseTable: &Execution{},
			primaryDBTable:  tables.Execution{},
		},
		{
			clickhouseTable: &TestTargetStatus{},
			// don't testing schema in sync
			primaryDBTable: nil,
		},
		{
			clickhouseTable: &AuditLog{},
			// Not in primary DB.
			primaryDBTable: nil,
		},
		{
			clickhouseTable: &RawUsage{},
			// Not in primary DB. There is a Usage table but it has an
			// incompatible schema.
			primaryDBTable: nil,
		},
	}

	assert.Equal(t, len(getAllTables()), len(tests), "All clickhouse tables should be present in the tests")

	for _, tc := range tests {
		if tc.primaryDBTable == nil {
			continue
		}

		chType := reflect.Indirect(reflect.ValueOf(tc.clickhouseTable)).Type()

		t.Run(chType.Name(), func(t *testing.T) {
			primaryDBType := reflect.TypeOf(tc.primaryDBTable)

			chFields := reflect.VisibleFields(chType)
			primaryFields := reflect.VisibleFields(primaryDBType)

			// Check AdditionalFields().
			additionalFields := tc.clickhouseTable.AdditionalFields()
			for _, f := range additionalFields {
				_, found := chType.FieldByName(f)
				assert.True(t, found, "Field %q is not found in %s, but it's in AdditionalFields()", f, chType)
			}

			// Check ExcludedFields().
			excludedFields := tc.clickhouseTable.ExcludedFields()
			for _, f := range excludedFields {
				_, found := primaryDBType.FieldByName(f)
				assert.True(t, found, "Field %q is not found in %s, but it's in ExcludedFields()", f, primaryDBType)
			}
			for _, chField := range chFields {
				_, found := primaryDBType.FieldByName(chField.Name)
				isIncluded := slices.Contains(additionalFields, chField.Name)
				if isIncluded {
					assert.False(t, found, "Field %q is found in %s, but it's marked as additional", chField.Name, primaryDBType)
				} else {
					assert.True(t, found, "Field %q is not found in %s", chField.Name, primaryDBType)
				}
			}

			for _, primaryField := range primaryFields {
				if primaryField.Anonymous {
					continue
				}

				_, found := chType.FieldByName(primaryField.Name)
				isExcluded := slices.Contains(excludedFields, primaryField.Name)
				if isExcluded {
					assert.False(t, found, "Field %q is found in %s, but it's marked as excluded", primaryField.Name, chType)
				} else {
					assert.True(t, found, "Field %q is not found in %s", primaryField.Name, chType)
				}
				assert.NotEqual(t, found, isExcluded, "Field %q is not found in %s", primaryField.Name, chType)
			}
		})
	}
}

var NonStandardInvocationCopyFields = []string{
	"InvocationUUID",
	"Tags",
}

func TestToInvocationFromPrimaryDB(t *testing.T) {
	src := &tables.Invocation{
		CreatedAtUsec:                     101,
		UpdatedAtUsec:                     102,
		InvocationUUID:                    []byte{0x00, 0x01, 0xab, 0xff},
		Tags:                              "linux,ci",
		GroupID:                           "GroupID-value",
		Role:                              "Role-value",
		User:                              "User-value",
		UserID:                            "UserID-value",
		Host:                              "Host-value",
		CommitSHA:                         "CommitSHA-value",
		BranchName:                        "BranchName-value",
		Command:                           "Command-value",
		BazelExitCode:                     "BazelExitCode-value",
		Pattern:                           "Pattern-value",
		Attempt:                           213,
		ActionCount:                       214,
		InvocationStatus:                  215,
		RepoURL:                           "RepoURL-value",
		DurationUsec:                      217,
		Success:                           true,
		ActionCacheHits:                   219,
		ActionCacheMisses:                 220,
		ActionCacheUploads:                221,
		CasCacheHits:                      222,
		CasCacheMisses:                    223,
		CasCacheUploads:                   224,
		TotalDownloadSizeBytes:            225,
		TotalUploadSizeBytes:              226,
		TotalDownloadUsec:                 227,
		TotalUploadUsec:                   228,
		TotalDownloadTransferredSizeBytes: 229,
		TotalUploadTransferredSizeBytes:   230,
		TotalCachedActionExecUsec:         231,
		TotalUncachedActionExecUsec:       232,
		DownloadThroughputBytesPerSecond:  233,
		UploadThroughputBytesPerSecond:    234,
		DownloadOutputsOption:             235,
		UploadLocalResultsEnabled:         true,
		RemoteExecutionEnabled:            true,
		RunID:                             "RunID-value",
		ParentRunID:                       "ParentRunID-value",
		RunStatus:                         240,
		GitFetchTotalBytes:                241,
		GitFetchDurationUsec:              242,
		GitFetchRetryCount:                243,
	}
	dest := ToInvocationFromPrimaryDB(src)

	primaryInvType := reflect.TypeOf(*src)
	primaryInvFields := reflect.VisibleFields(primaryInvType)
	srcValue := reflect.ValueOf(*src)
	destValue := reflect.ValueOf(*dest)
	excludedFields := (&Invocation{}).ExcludedFields()

	expectedUUID := "0001abff"
	assert.Equal(t, expectedUUID, dest.InvocationUUID, "src and dest have different values for field 'InvocationUUID'. src = %v, dest = %v", src.InvocationUUID, expectedUUID)
	// Tags are stored as a comma-separated string in the primary DB.
	assert.Equal(t, []string{"linux", "ci"}, dest.Tags)

	for _, primaryField := range primaryInvFields {
		if slices.Contains(excludedFields, primaryField.Name) || primaryField.Anonymous || slices.Contains(NonStandardInvocationCopyFields, primaryField.Name) {
			// already checked fields that don't do direct copies seperately above.
			continue
		}
		srcFieldValue := srcValue.FieldByName(primaryField.Name)
		destFieldValue := destValue.FieldByName(primaryField.Name)
		require.True(t, srcFieldValue.IsValid())
		// A newly added field must receive a nonzero fixture value to catch omitted copies.
		require.False(t, srcFieldValue.IsZero(), "fixture omits %s", primaryField.Name)
		if destFieldValue.IsValid() {
			assert.Equal(t, srcFieldValue.Interface(), destFieldValue.Interface(), "src and dest have different values for field %q. src = %v, dest = %v", primaryField.Name, srcFieldValue, destFieldValue)
		} else {
			assert.Failf(t, "dest has invalid field %q", primaryField.Name)
		}
	}
}

func TestExtractProjectionNames(t *testing.T) {
	createStmt := `
CREATE TABLE bb_test_target.TestTargetStatuses
(
` +
		"`group_id` String," +
		"`repo_url` String," +
		`
    PROJECTION projection_commits
    (
        SELECT
            group_id,
            role,
            repo_url,
            commit_sha,
            max(created_at_usec) AS latest_created_at_usec
        GROUP BY
            group_id,
            role,
            repo_url,
            commit_sha
    )
	PROJECTION projection_targets
	( 
	    SELECT 
		    group_id,
			role,
			repo_url,
			label
	)
)
ENGINE = ReplacingMergeTree
ORDER BY (group_id, repo_url)
SETTINGS index_granularity = 8192
`
	projectionNames := extractProjectionNamesFromCreateStmt(createStmt)
	assert.Contains(t, projectionNames, "projection_commits")
	assert.Contains(t, projectionNames, "projection_targets")

}

func TestUsageViewQuery(t *testing.T) {
	const expectedQuery = "SELECT group_id, period_start, sku, labels, SUM(count) AS count " +
		"FROM RawUsage FINAL GROUP BY group_id, period_start, sku, labels"

	// RawUsage may contain duplicate flushes from Redis buffers, so the view
	// deduplicates with FINAL before aggregating counts by Usage dimensions.
	assert.Equal(t, expectedQuery, (&Usage{}).ViewQuery())
}

func TestSameViewQuery(t *testing.T) {
	// ClickHouse may store the saved SELECT with different formatting and
	// function casing, so avoid replacing the view when the query is
	// semantically the same for this generated view.
	assert.True(t, sameViewQuery(
		"SELECT `group_id`, `period_start`, `sku`, `labels`, sum(`count`) AS `count` FROM `db_name`.`RawUsage` FINAL GROUP BY `group_id`, `period_start`, `sku`, `labels`",
		"SELECT group_id, period_start, sku, labels, SUM(count) AS count FROM RawUsage FINAL GROUP BY group_id, period_start, sku, labels",
		"db_name",
	))

	// A different grouping or aggregate expression should trigger replacement.
	assert.False(t, sameViewQuery(
		"SELECT group_id, period_start, sku, SUM(count) AS count FROM RawUsage FINAL GROUP BY group_id, period_start, sku",
		"SELECT group_id, period_start, sku, labels, SUM(count) AS count FROM RawUsage FINAL GROUP BY group_id, period_start, sku, labels",
		"",
	))
}
