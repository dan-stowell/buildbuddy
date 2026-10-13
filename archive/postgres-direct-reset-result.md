# Postgres reset via SQL

Changed only `server/testutil/testpostgres/testpostgres.go`. The readiness/reset connection now uses the `postgres` control database. Each reused-server reset executes separate, quoted `DROP DATABASE IF EXISTS "buildbuddy-test" WITH (FORCE)` and `CREATE DATABASE "buildbuddy-test"` statements. FORCE is supported by the pinned `postgres:15.3` image. The returned test DSN and real GORM migrations are preserved. Timing logs measure the complete drop/create pair.

Isolation is preserved: each existing reset still destroys and recreates the entire test database, force-disconnecting old sessions. No new sharing or parallelism was introduced. Existing test scoping, container reuse, cleanup, migrations, cases, assertions, and sharding remain unchanged. No other source/build files changed.

## Measurement method

All test measurements below are remote: BuildBuddy Remote Bazel orchestrates the invocation, and `--config=remote` dispatches Docker-enabled test actions to their declared Firecracker workers. No local Bazel fallback was needed. Local Docker was available and its PostgreSQL image pull succeeded, but no local test timings are mixed into these results.

Baseline used the original reset with only the same `time.Now()` / `time.Since()` log instrumentation added around the paired commands. After uses identical instrumentation around the paired SQL statements. Readiness, container discovery/startup, and migrations are excluded from paired reset timings. Each log event is one completed reset. Before/after used exactly this command:

```sh
bb remote test \
  //server/backends/invocationdb:invocationdb_test_postgres \
  //enterprise/server/backends/authdb:authdb_test_postgres \
  //enterprise/server/backends/userdb:userdb_test_postgres \
  //enterprise/server/usage:usage_test_postgres \
  --config=remote --runs_per_test=5 --nocache_test_results --test_output=all
```

The premise was verified in source and runtime: four Postgres targets enable `--testenv.reuse_server`, each baseline reset executes two docker-exec processes, and the number of completed resets is unchanged.

## Main comparison

Times are per complete drop/create pair, aggregated over five repetitions. Resets/run counts the whole target across its existing shards.

| Target | Resets/run before = after | Resets in 5 runs | Before mean / median (ms) | After mean / median (ms) | Mean change |
|---|---:|---:|---:|---:|---:|
| `//server/backends/invocationdb:invocationdb_test_postgres` | 5 | 25 | 477.22 / 453.88 | 241.87 / 128.29 | -49.3% |
| `//enterprise/server/backends/authdb:authdb_test_postgres` | 17 | 85 | 357.93 / 326.02 | 102.47 / 57.51 | -71.4% |
| `//enterprise/server/usage:usage_test_postgres` | 10 | 50 | 1435.33 / 1232.75 | 4020.93 / 5200.68 | +180.1% |
| `//enterprise/server/backends/userdb:userdb_test_postgres` | 102 | 510 | 671.46 / 616.20 | 320.71 / 175.19 | -52.2% |

There are **134 resets per complete run**, hence **268 docker-exec subprocesses removed per run** (670 resets and 1,340 subprocesses over five repetitions). This is an observed reset count, not a projected count. Main aggregate reset duration: before 456.56 s, after 379.37 s, summed across concurrent actions; this is not wall time.

Bazel elapsed time was 343.559 s before and 109.077 s after. These include build/cache/scheduling effects and cannot be attributed entirely to the reset change.

Three targets improved in the main paired-reset comparison; usage regressed. Rather than hide this, usage was repeated alone with the same command/flags and five repetitions:

| Usage-only rerun | Resets | Mean pair (ms) | Median pair (ms) |
|---|---:|---:|---:|
| Before | 50 | 10425.98 | 13609.95 |
| After | 50 | 3723.70 | 5013.08 |

Usage-only mean improved 64.3%, but baseline mean varied from 1.44 s to 10.43 s across invocations with unchanged code. Usage's main regression and rerun improvement show substantial remote execution variance; no reliable usage-specific saving or precise overall wall-time saving is claimed. Measurements support real savings in the other three targets and remove the two subprocesses everywhere. No further tuning was done based on uncertain usage attribution.

## Repeated runs and coverage

All four targets passed 5/5 repetitions before and after, without retries configured or observed failures. Existing sharding means invocationdb/authdb each execute 5 test actions, userdb executes 25 (5 shards × 5), and usage executes 35 (7 shards × 5). Usage-only reruns also passed 5/5 before and after. Main runtime output contains 730 passing case occurrences in each phase (146 distinct passing top-level/nested names, each passing 5/5).

One pre-existing ClickHouse-only case, `TestClickHouseUsageView_DedupesDuplicateRawUsageRows`, skips with “ClickHouse is not enabled” in the Postgres target: 5/5 skips in both phases and in both usage-only reruns. No skip was added or changed. Therefore every applicable Postgres case passed; this ClickHouse case cannot be claimed to pass in these targets. The separate ClickHouse target was not changed or run.

The lists below come from actual `--- PASS:` and `--- SKIP:` lines in Bazel test output, including nested cases. Parsed before/after name sets **and occurrence counts are exactly equal**. Each row explicitly lists the before and after outcome (5 repetitions); no case was removed.

### `//server/backends/invocationdb:invocationdb_test_postgres`

| Test case (same name before and after) | Before | After |
|---|---|---|
| `TestAttemptLogic` | PASS 5/5 | PASS 5/5 |
| `TestAuthenticatedInvocationReplacesMissingAPIKeyPlaceholder` | PASS 5/5 | PASS 5/5 |
| `TestCreateReadUpdateDelete` | PASS 5/5 | PASS 5/5 |
| `TestDeleteInvocations` | PASS 5/5 | PASS 5/5 |
| `TestDeleteInvocationsRollsBackOnFailure` | PASS 5/5 | PASS 5/5 |
### `//enterprise/server/backends/authdb:authdb_test_postgres`

| Test case (same name before and after) | Before | After |
|---|---|---|
| `TestAPIKeyAuditLogs` | PASS 5/5 | PASS 5/5 |
| `TestAPIKeyCreationMetadata` | PASS 5/5 | PASS 5/5 |
| `TestAPIKeyValueReadbackDisabled` | PASS 5/5 | PASS 5/5 |
| `TestAPIKeyVisibility` | PASS 5/5 | PASS 5/5 |
| `TestBackfillUnencryptedKeys` | PASS 5/5 | PASS 5/5 |
| `TestGetAPIKeyGroupFromAPIKey` | PASS 5/5 | PASS 5/5 |
| `TestGetAPIKeyGroupFromAPIKey/encrypt_false` | PASS 5/5 | PASS 5/5 |
| `TestGetAPIKeyGroupFromAPIKey/encrypt_true` | PASS 5/5 | PASS 5/5 |
| `TestGetAPIKeyGroupFromAPIKeyID` | PASS 5/5 | PASS 5/5 |
| `TestGetAPIKeyGroup_UserOwnedKeys` | PASS 5/5 | PASS 5/5 |
| `TestGetAPIKeys` | PASS 5/5 | PASS 5/5 |
| `TestGetAPIKeys/encrypt_false` | PASS 5/5 | PASS 5/5 |
| `TestGetAPIKeys/encrypt_true` | PASS 5/5 | PASS 5/5 |
| `TestGroupKeyExpiration` | PASS 5/5 | PASS 5/5 |
| `TestImpersonationAPIKeys` | PASS 5/5 | PASS 5/5 |
| `TestImpersonationKeys` | PASS 5/5 | PASS 5/5 |
| `TestSessionInsertUpdateDeleteRead` | PASS 5/5 | PASS 5/5 |
| `TestSubdomainRestrictions` | PASS 5/5 | PASS 5/5 |
| `TestUserKeyExpiration` | PASS 5/5 | PASS 5/5 |
### `//enterprise/server/usage:usage_test_postgres`

| Test case (same name before and after) | Before | After |
|---|---|---|
| `TestUsageTracker_AllFieldsAreMapped` | PASS 5/5 | PASS 5/5 |
| `TestUsageTracker_Flush_ConcurrentAccessAcrossApps` | PASS 5/5 | PASS 5/5 |
| `TestUsageTracker_Flush_CrossRegion` | PASS 5/5 | PASS 5/5 |
| `TestUsageTracker_Flush_DoesNotFlushUnsettledCollectionPeriods` | PASS 5/5 | PASS 5/5 |
| `TestUsageTracker_Flush_OnlyWritesToDBIfNecessary` | PASS 5/5 | PASS 5/5 |
| `TestUsageTracker_Increment_ImpersonationSuppressesUsage` | PASS 5/5 | PASS 5/5 |
| `TestUsageTracker_Increment_MultipleGroupsInSameCollectionPeriod` | PASS 5/5 | PASS 5/5 |
| `TestUsageTracker_UsageLabels_AllFieldsAreMapped` | PASS 5/5 | PASS 5/5 |
| `TestUsageTracker_UsageLabels_Basic` | PASS 5/5 | PASS 5/5 |
| `TestUsageTracker_UsageLabels_UnrecognizedLabelsAreNotFlushed` | PASS 5/5 | PASS 5/5 |
| `TestClickHouseUsageView_DedupesDuplicateRawUsageRows` | SKIP 5/5 (existing) | SKIP 5/5 (existing) |
### `//enterprise/server/backends/userdb:userdb_test_postgres`

| Test case (same name before and after) | Before | After |
|---|---|---|
| `TestAddUserToGroup_GroupHasOnlyUserListMembers` | PASS 5/5 | PASS 5/5 |
| `TestCapabilitiesForUserRole` | PASS 5/5 | PASS 5/5 |
| `TestCapabilitiesForUserRole/Admin` | PASS 5/5 | PASS 5/5 |
| `TestCapabilitiesForUserRole/Developer` | PASS 5/5 | PASS 5/5 |
| `TestCapabilitiesForUserRole/Reader` | PASS 5/5 | PASS 5/5 |
| `TestCapabilitiesForUserRole/Writer` | PASS 5/5 | PASS 5/5 |
| `TestChildGroupAuth` | PASS 5/5 | PASS 5/5 |
| `TestCreateAndGetAPIKey` | PASS 5/5 | PASS 5/5 |
| `TestCreateGroup` | PASS 5/5 | PASS 5/5 |
| `TestCreateGroup_StatusInheritance` | PASS 5/5 | PASS 5/5 |
| `TestCreateGroup_StatusInheritance/BLOCKED_->_BLOCKED_(inherit)` | PASS 5/5 | PASS 5/5 |
| `TestCreateGroup_StatusInheritance/ENTERPRISE_TRIAL_admin_API_key_->_ENTERPRISE_TRIAL` | PASS 5/5 | PASS 5/5 |
| `TestCreateGroup_StatusInheritance/ENTERPRISE_TRIAL_browser_user_->_FREE_TIER` | PASS 5/5 | PASS 5/5 |
| `TestCreateGroup_StatusInheritance/ENTERPRISE_TRIAL_non-admin_API_key_->_FREE_TIER` | PASS 5/5 | PASS 5/5 |
| `TestCreateGroup_StatusInheritance/ENTERPRISE_admin_API_key_->_ENTERPRISE` | PASS 5/5 | PASS 5/5 |
| `TestCreateGroup_StatusInheritance/ENTERPRISE_browser_user_->_FREE_TIER` | PASS 5/5 | PASS 5/5 |
| `TestCreateGroup_StatusInheritance/ENTERPRISE_non-admin_API_key_->_FREE_TIER` | PASS 5/5 | PASS 5/5 |
| `TestCreateGroup_StatusInheritance/FREE_TIER_->_FREE_TIER` | PASS 5/5 | PASS 5/5 |
| `TestCreateGroup_StatusInheritance/UNKNOWN_->_FREE_TIER_(default)` | PASS 5/5 | PASS 5/5 |
| `TestCreateUser_Cloud_CreatesSelfOwnedGroup` | PASS 5/5 | PASS 5/5 |
| `TestCreateUser_Cloud_JoinsOnlyDomainGroup` | PASS 5/5 | PASS 5/5 |
| `TestCreateUser_GroupHasOnlyUserListMembers` | PASS 5/5 | PASS 5/5 |
| `TestCreateUser_OnPrem_OnlyFirstUserCreatedShouldBeMadeAdminOfDefaultGroup` | PASS 5/5 | PASS 5/5 |
| `TestDeleteAPIKey` | PASS 5/5 | PASS 5/5 |
| `TestDeleteUserGitHubToken` | PASS 5/5 | PASS 5/5 |
| `TestDeleteUserList_InvalidatesUserAPIKeysForIndirectMembers` | PASS 5/5 | PASS 5/5 |
| `TestDeleteUserList_UserAPIKeyStillWorksForDirectMember` | PASS 5/5 | PASS 5/5 |
| `TestGetAPIKeyForInternalUseOnly` | PASS 5/5 | PASS 5/5 |
| `TestGetAPIKeyForInternalUseOnly_ManyUsers` | PASS 5/5 | PASS 5/5 |
| `TestGetAPIKeyForInternalUseOnly_PrefersCacheWritePermissions` | PASS 5/5 | PASS 5/5 |
| `TestGetImpersonatedUser_UserWithoutImpersonationPerms_PermissionDenied` | PASS 5/5 | PASS 5/5 |
| `TestGetUserByID_DirectMembershipsOnly` | PASS 5/5 | PASS 5/5 |
| `TestGetUserBySubID` | PASS 5/5 | PASS 5/5 |
| `TestGetUserBySubIDNoGroup` | PASS 5/5 | PASS 5/5 |
| `TestGetUserWithOwnedGroups` | PASS 5/5 | PASS 5/5 |
| `TestGetUser_OrgAPIKeyReturnsNotFound` | PASS 5/5 | PASS 5/5 |
| `TestGroupAuditLogs` | PASS 5/5 | PASS 5/5 |
| `TestGroupMembershipAuditLogs` | PASS 5/5 | PASS 5/5 |
| `TestInsertUser` | PASS 5/5 | PASS 5/5 |
| `TestInsertUser/DuplicateSubID` | PASS 5/5 | PASS 5/5 |
| `TestInsertUser/DuplicateUserID` | PASS 5/5 | PASS 5/5 |
| `TestInsertUser/MissingSubID` | PASS 5/5 | PASS 5/5 |
| `TestInsertUser/MissingUserID` | PASS 5/5 | PASS 5/5 |
| `TestOwnedDomainBlocklist` | PASS 5/5 | PASS 5/5 |
| `TestRemoveUserFromGroup_NoIndirectAccess_UserAPIKeyNotValid` | PASS 5/5 | PASS 5/5 |
| `TestRemoveUserFromGroup_UserAPIKeyStillWorksThroughIndirectMembership` | PASS 5/5 | PASS 5/5 |
| `TestRemoveUserFromUserList_UserAPIKeyStillWorksForDirectMember` | PASS 5/5 | PASS 5/5 |
| `TestRemoveUserListFromGroup_InvalidatesKeysForIndirectOnlyUser` | PASS 5/5 | PASS 5/5 |
| `TestRemoveUserListFromGroup_UserAPIKeyStillWorksForDirectMember` | PASS 5/5 | PASS 5/5 |
| `TestRequestToJoinGroup_AlreadyInGroup_GetAlreadyExists` | PASS 5/5 | PASS 5/5 |
| `TestRequestToJoinGroup_Disabled` | PASS 5/5 | PASS 5/5 |
| `TestRequestToJoinGroup_DomainMember_AlreadyInGroup_GetAlreadyExistsError` | PASS 5/5 | PASS 5/5 |
| `TestRequestToJoinGroup_DomainMember_AlreadyRequested_EmptyGroup_GetAdminRole` | PASS 5/5 | PASS 5/5 |
| `TestRequestToJoinGroup_DomainMember_AlreadyRequested_GetDeveloperRole` | PASS 5/5 | PASS 5/5 |
| `TestRequestToJoinGroup_DomainMember_EmptyGroup_GetsAdminRole` | PASS 5/5 | PASS 5/5 |
| `TestRequestToJoinGroup_DomainMember_GetsDeveloperRole` | PASS 5/5 | PASS 5/5 |
| `TestRequestToJoinGroup_DomainNonMember_CreatesRequest` | PASS 5/5 | PASS 5/5 |
| `TestRequestToJoinGroup_NoAutoJoinForSSOUser` | PASS 5/5 | PASS 5/5 |
| `TestUpdateAPIKey` | PASS 5/5 | PASS 5/5 |
| `TestUpdateGroup` | PASS 5/5 | PASS 5/5 |
| `TestUpdateGroupSamlIdpMetadataUrl_CascadesForParent` | PASS 5/5 | PASS 5/5 |
| `TestUpdateGroupSamlIdpMetadataUrl_NoCascadeForNonParent` | PASS 5/5 | PASS 5/5 |
| `TestUpdateGroupSamlIdpMetadataUrl_ParentCannotClearURL` | PASS 5/5 | PASS 5/5 |
| `TestUpdateGroupSamlIdpMetadataUrl_ParentEmptyURLDoesNotCascade` | PASS 5/5 | PASS 5/5 |
| `TestUpdateGroupUsers_AddNonExistentUser` | PASS 5/5 | PASS 5/5 |
| `TestUpdateGroupUsers_Role` | PASS 5/5 | PASS 5/5 |
| `TestUpdateGroupUsers_RoleAuth` | PASS 5/5 | PASS 5/5 |
| `TestUpdateGroupUsers_RoleAuth/AdminCanAdd` | PASS 5/5 | PASS 5/5 |
| `TestUpdateGroupUsers_RoleAuth/AdminCanRemoveAdmin` | PASS 5/5 | PASS 5/5 |
| `TestUpdateGroupUsers_RoleAuth/AdminCanRemoveDeveloper` | PASS 5/5 | PASS 5/5 |
| `TestUpdateGroupUsers_RoleAuth/AnonymousCannotAdd` | PASS 5/5 | PASS 5/5 |
| `TestUpdateGroupUsers_RoleAuth/DeveloperCannotAdd` | PASS 5/5 | PASS 5/5 |
| `TestUpdateGroupUsers_RoleAuth/DeveloperCannotRemove` | PASS 5/5 | PASS 5/5 |
| `TestUpdateGroupUsers_RoleAuth/NonMemberCannotAdd` | PASS 5/5 | PASS 5/5 |
| `TestUpdateGroupUsers_RoleAuth/NonMemberCannotRemove` | PASS 5/5 | PASS 5/5 |
| `TestUpdateGroupUsers_RoleAuth/UserIdIsRequired` | PASS 5/5 | PASS 5/5 |
| `TestUpdateGroupUsers_UserLists` | PASS 5/5 | PASS 5/5 |
| `TestUpdateUserListRole_DowngradeReflectedInUserAPIKeyCapabilities` | PASS 5/5 | PASS 5/5 |
| `TestUserListAuditLogs` | PASS 5/5 | PASS 5/5 |
| `TestUserListMembership` | PASS 5/5 | PASS 5/5 |
| `TestUserListMembershipAuditLogs` | PASS 5/5 | PASS 5/5 |
| `TestUserListOps` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_ChangeRole_UpdatesCapabilities` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_CreateAndUpdateCapabilities` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_CreateAndUpdateCapabilities/Admin_ACWrite_OK` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_CreateAndUpdateCapabilities/Admin_CASWrite_OK` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_CreateAndUpdateCapabilities/Admin_Executor_Fail` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_CreateAndUpdateCapabilities/Admin_OrgAdmin_Fail` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_CreateAndUpdateCapabilities/Developer_ACWrite_Fail` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_CreateAndUpdateCapabilities/Developer_CASWrite_OK` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_CreateAndUpdateCapabilities/Developer_Executor_Fail` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_CreateAndUpdateCapabilities/Reader_ACWrite_Fail` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_CreateAndUpdateCapabilities/Reader_CASWrite_Fail` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_CreateAndUpdateCapabilities/Reader_NoCapabilities_OK` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_CreateAndUpdateCapabilities/Writer_ACWrite_OK` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_CreateAndUpdateCapabilities/Writer_CASWrite_OK` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_CreateAndUpdateCapabilities/Writer_NoCapabilities_OK` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_CreateForOtherUser` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_CreateForOtherUser/AuthAsOrgAdminAPIKey_CreateForUserInAuthenticatedGroup_OK` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_CreateForOtherUser/AuthAsOrgAdminAPIKey_CreateForUserInOtherGroup_PermissionDenied` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_CreateForOtherUser/AuthAsOrgAdminUser_CreateForUserInAuthenticatedGroup_OK` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_CreateForOtherUser/AuthAsOrgDeveloper_CreateForUserInAuthenticatedGroup_PermissionDenied` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_CreateForOtherUser/AuthAsRegularAPIKey_CreateForUserInAuthenticatedGroup_PermissionDenied` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_GetUpdateDeletePermissions` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_GetUpdateDeletePermissions/AnonymousUser` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_GetUpdateDeletePermissions/DifferentOrg` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_GetUpdateDeletePermissions/KeyOwner` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_GetUpdateDeletePermissions/SameOrgAdmin` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_GetUpdateDeletePermissions/SameOrgDeveloper` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_NotReturnedByGroupLevelAPIs` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_RemoveUserFromGroup_KeyNoLongerWorks` | PASS 5/5 | PASS 5/5 |
| `TestUserOwnedKeys_RespectsEnabledSetting` | PASS 5/5 | PASS 5/5 |

## Evidence and limitations

- before: local raw log `/tmp/testpostgres-before.log`; remote invocation https://app.buildbuddy.io/invocation/97ebfc20-35f5-4c1f-8626-60aeca9cb0dd, https://app.buildbuddy.io/invocation/96060dd9-ad86-4caa-abe6-e7fbda0a207c.
- after: local raw log `/tmp/testpostgres-after.log`; remote invocation https://app.buildbuddy.io/invocation/2fdc05e8-39eb-45da-ab96-9bd80c0a3081, https://app.buildbuddy.io/invocation/5b325b5e-8dbb-4cd1-a632-0174c46b2f7c.
- usage-before: local raw log `/tmp/testpostgres-usage-before.log`; remote invocation https://app.buildbuddy.io/invocation/1a317270-037d-4f75-ab9b-c8ef3330b500, https://app.buildbuddy.io/invocation/b8fdcee9-c419-40e6-9874-81019dbb673b.
- usage-after: local raw log `/tmp/testpostgres-usage-after.log`; remote invocation https://app.buildbuddy.io/invocation/44cd479e-09ce-4ecc-b24e-199ac50e209b, https://app.buildbuddy.io/invocation/c4a131eb-2fd2-4dbd-af5e-dac7880fc5e3.

Initial remote attempts were invalid for measurement: defaults excluded Docker-tagged targets (“No test targets were found”), then overriding tag filters without `--config=remote` ran tests on an orchestration runner without dockerd and failed. These were corrected before the baseline above; their failures are not counted as passing runs. No auth/repository-access failures or 10-minute runner wait occurred.

No coverage weakening, fixture redesign, migration bypass, timeout change, dependency/CI change, or extra parallelism was attempted. `gofmt` and `git diff --check` passed. `result.md` is intentionally not committed. The origin fetch URL is restored to the internal mirror; nothing was pushed and no PR was opened.

---
Committed as `d1f118db5`: resets now use quoted SQL on the control database, with forced disconnection and real migrations preserved.

All four targets passed 5/5 runs; case sets match. Three targets showed faster resets; usage results were mixed. Its existing ClickHouse-only skip is documented.

Measurements, case lists, and limitations are in [result.md](/home/exedev/repo/result.md), left uncommitted. Origin restored; nothing pushed.
