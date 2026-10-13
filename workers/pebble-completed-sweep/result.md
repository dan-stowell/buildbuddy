# Wait for a completed Pebble janitor sweep in the threshold test

The higher-threshold retention check previously slept for three seconds before asserting that all 95 entries remained. It now waits for partition size accounting to catch up, then for completion of a janitor sweep started after setup. The sweep-completion signal includes the below-threshold return, so the test proves that the janitor ran while preserving every original retention assertion.

The minimum-age test is restored byte-for-byte to origin/master, including its five fake-clock wake attempts, 100ms wake sleeps, three-second negative-eviction wait, and subsequent positive-eviction phase. A sampling batch finishing does not prove that incorrectly enqueued young samples had an opportunity to be evicted before the assertions. That optimization is reverted to preserve coverage. All sampling-only production observation code, the fake-clock wait helper, and the tests introduced solely for that reverted path are removed from the final diff. The sampler implementation and the internal Pebble test file are unchanged from origin/master.

The remaining minimal LRU observation hook captures a test completion channel at sweep start and closes it after the janitor returns. Registration and capture are synchronized; an already-running sweep cannot satisfy a newly registered observation. Eviction decisions, clocks, intervals, and sampling behavior are unchanged. The cache helper and observation API are for tests only, and the observation channel is allocated only when a test requests it. The wait has a 30-second failure timeout.

`TestSweepWaitIgnoresRunningSweep` retains the deterministic ordering regression: it blocks an actual LRU sampling callback, registers an observation, completes that old sweep, and verifies the signal stays open while the following sweep is blocked. Only releasing the following sweep completes the observation.

## Validation

Both complete targets passed **5/5 runs** on BuildBuddy Remote Bazel:

```sh
bash -lc 'cd ~/repo && bb remote test //enterprise/server/backends/pebble_cache:pebble_cache_test //server/util/approxlru:approxlru_test --test_output=all --runs_per_test=5 --nocache_test_results'
```

[Five-run invocation](https://app.buildbuddy.io/invocation/57de5a80-69b3-4bec-8b3e-3fdc6a2d8e9e): 214 Pebble cases and 3 approxlru cases per run; **1,085 case PASS results**, no failures. Every original case, both threshold scenarios, the restored minimum-age scenario, and the janitor ordering regression passed all five times. Formatting and `git diff --check` passed. A source comparison also confirms the minimum-age function is identical to origin/master.

No original tests or assertions were deleted, skipped, weakened, renamed, or manual-tagged. No dependency, lockfile, BUILD, or CI configuration changes were made. The final source diff contains the two original Pebble files and approxlru's implementation/test files.

## Timings

The original baseline and final standalone check used the same remote command:

```sh
bash -lc 'cd ~/repo && bb remote test //enterprise/server/backends/pebble_cache:pebble_cache_test --test_output=all --nocache_test_results'
```

- [Before invocation](https://app.buildbuddy.io/invocation/7f0f5889-125c-46f8-9ff5-c5c32cb8f573): all 214 cases passed.
- [Final after invocation](https://app.buildbuddy.io/invocation/7e55aeaf-15f2-4b73-a908-21022469c70b): the same 214 cases passed.

Both case timings use Go `--- PASS` durations. The total below sums only top-level durations to avoid counting nested cases twice; neither suite uses t.Parallel.

| Measure | Before | After |
| --- | ---: | ---: |
| Changed higher-threshold negative case | 3.02s | 1.01s |
| Unchanged lower-threshold positive case | 4.04s | 1.12s |
| Restored/unchanged minimum-age case | 12.13s | 4.07s |
| Sum of the same 59 top-level Pebble tests | 51.22s | 38.05s |
| Bazel elapsed time, including analysis/build | 95.626s | 42.547s |
| Bazel critical path | 63.15s | 38.75s |

The final scope saves approximately **2 seconds** in the threshold case, replacing its three-second sleep with the unchanged one-second janitor ticker. The originally modeled six-second saving is not claimed, and the modeled 79-second target time was not reproduced. Whole-target single-run differences include variation in unchanged eviction phases and build/cache effects, so they are not a controlled estimate of this change's speedup. The new approxlru ordering regression takes about two seconds in its separate target.

| Relevant case in five full-target runs | Pass count | Duration range | Median |
| --- | ---: | ---: | ---: |
| TestPartitionMinEvictionAge | 5/5 | 3.77–4.08s | 4.07s |
| TestPartitionJanitorCutoffThreshold/higher_threshold_starts_eviction_later | 5/5 | 1.01–1.02s | 1.01s |
| TestPartitionJanitorCutoffThreshold/lower_threshold_starts_eviction_sooner | 5/5 | 1.11–2.02s | 1.12s |
| TestSweepWaitIgnoresRunningSweep | 5/5 | 2.00–2.00s | 2.00s |

## Before/after executed case lists

Extracted from `=== RUN` and `--- PASS` lines in the original baseline and final standalone Bazel output, including nested cases. Compared as sets because execution order varies: **before = after = 214 Pebble cases**. Every listed case passed both standalone runs and all five final repetitions.

| Before — PASS | After — PASS (also 5/5 repetitions) |
| --- | --- |
| TestAtimeUpdater | TestAtimeUpdater |
| TestAtimeUpdaterSkipRemote | TestAtimeUpdaterSkipRemote |
| TestCacheStaysBelowConfiguredSize | TestCacheStaysBelowConfiguredSize |
| TestCacheStaysBelowConfiguredSize/defaults | TestCacheStaysBelowConfiguredSize/defaults |
| TestCacheStaysBelowConfiguredSize/gcs_and_metadata_enabled | TestCacheStaysBelowConfiguredSize/gcs_and_metadata_enabled |
| TestCacheStaysBelowConfiguredSize/include_metadata | TestCacheStaysBelowConfiguredSize/include_metadata |
| TestCacheStaysBelowConfiguredSize/no_inlining | TestCacheStaysBelowConfiguredSize/no_inlining |
| TestCompression | TestCompression |
| TestCompression/disk_multiple_compression_chunk_write_compressed_read_compressed | TestCompression/disk_multiple_compression_chunk_write_compressed_read_compressed |
| TestCompression/disk_multiple_compression_chunk_write_compressed_read_decompressed | TestCompression/disk_multiple_compression_chunk_write_compressed_read_decompressed |
| TestCompression/disk_multiple_compression_chunk_write_uncompressed_read_compressed | TestCompression/disk_multiple_compression_chunk_write_uncompressed_read_compressed |
| TestCompression/disk_multiple_compression_chunk_write_uncompressed_read_decompressed | TestCompression/disk_multiple_compression_chunk_write_uncompressed_read_decompressed |
| TestCompression/inline_write_compressed_read_compressed | TestCompression/inline_write_compressed_read_compressed |
| TestCompression/inline_write_compressed_read_decompressed | TestCompression/inline_write_compressed_read_decompressed |
| TestCompression/inline_write_uncompressed_read_compressed | TestCompression/inline_write_uncompressed_read_compressed |
| TestCompression/inline_write_uncompressed_read_decompressed | TestCompression/inline_write_uncompressed_read_decompressed |
| TestCompressionOffset | TestCompressionOffset |
| TestCompressionOffset/disk_multiple_compression_chunk | TestCompressionOffset/disk_multiple_compression_chunk |
| TestCompressionOffset/inline | TestCompressionOffset/inline |
| TestCompression_BufferPoolReuse | TestCompression_BufferPoolReuse |
| TestCompression_BufferPoolReuse/inline | TestCompression_BufferPoolReuse/inline |
| TestCompression_BufferPoolReuse/non_inline | TestCompression_BufferPoolReuse/non_inline |
| TestCompression_NoEarlyEviction | TestCompression_NoEarlyEviction |
| TestCompression_ParallelRequests | TestCompression_ParallelRequests |
| TestCompression_ParallelRequests/inline | TestCompression_ParallelRequests/inline |
| TestCompression_ParallelRequests/non_inline | TestCompression_ParallelRequests/non_inline |
| TestCreateReference | TestCreateReference |
| TestDeleteEmptyDirs | TestDeleteEmptyDirs |
| TestDeleteOrphans | TestDeleteOrphans |
| TestDereference | TestDereference |
| TestDereference/dereference | TestDereference/dereference |
| TestDereference/encrypted | TestDereference/encrypted |
| TestDereference/identity_stored,_zstd_requested | TestDereference/identity_stored,_zstd_requested |
| TestDereference/malformed_reference | TestDereference/malformed_reference |
| TestDereference/missing_blob | TestDereference/missing_blob |
| TestDereference/no_shared_storage_backing | TestDereference/no_shared_storage_backing |
| TestDereference/ranged_zstd_passthrough_is_rejected | TestDereference/ranged_zstd_passthrough_is_rejected |
| TestDereference/zstd_stored,_identity_requested | TestDereference/zstd_stored,_identity_requested |
| TestDupeWrites | TestDupeWrites |
| TestDupeWrites/ac_extern | TestDupeWrites/ac_extern |
| TestDupeWrites/ac_inline | TestDupeWrites/ac_inline |
| TestDupeWrites/cas_extern | TestDupeWrites/cas_extern |
| TestDupeWrites/cas_inline | TestDupeWrites/cas_inline |
| TestEncryptedUnencryptedSameDigest | TestEncryptedUnencryptedSameDigest |
| TestEncryptedUnencryptedSameDigest/disk | TestEncryptedUnencryptedSameDigest/disk |
| TestEncryptedUnencryptedSameDigest/inline | TestEncryptedUnencryptedSameDigest/inline |
| TestEncryption | TestEncryption |
| TestEncryption/disk_is_key_available_false | TestEncryption/disk_is_key_available_false |
| TestEncryption/disk_is_key_available_true | TestEncryption/disk_is_key_available_true |
| TestEncryption/inline_is_key_available_false | TestEncryption/inline_is_key_available_false |
| TestEncryption/inline_is_key_available_true | TestEncryption/inline_is_key_available_true |
| TestEncryptionAndCompression | TestEncryptionAndCompression |
| TestEncryptionAndCompression/write_compressed_read_compressed_disk_multiple_compression_chunk | TestEncryptionAndCompression/write_compressed_read_compressed_disk_multiple_compression_chunk |
| TestEncryptionAndCompression/write_compressed_read_compressed_inline | TestEncryptionAndCompression/write_compressed_read_compressed_inline |
| TestEncryptionAndCompression/write_compressed_read_decompressed_disk_multiple_compression_chunk | TestEncryptionAndCompression/write_compressed_read_decompressed_disk_multiple_compression_chunk |
| TestEncryptionAndCompression/write_compressed_read_decompressed_inline | TestEncryptionAndCompression/write_compressed_read_decompressed_inline |
| TestEncryptionAndCompression/write_compressed_read_decompressed_offset_disk_multiple_compression_chunk | TestEncryptionAndCompression/write_compressed_read_decompressed_offset_disk_multiple_compression_chunk |
| TestEncryptionAndCompression/write_compressed_read_decompressed_offset_inline | TestEncryptionAndCompression/write_compressed_read_decompressed_offset_inline |
| TestEncryptionAndCompression/write_uncompressed_read_compressed_disk_multiple_compression_chunk | TestEncryptionAndCompression/write_uncompressed_read_compressed_disk_multiple_compression_chunk |
| TestEncryptionAndCompression/write_uncompressed_read_compressed_inline | TestEncryptionAndCompression/write_uncompressed_read_compressed_inline |
| TestEncryptionAndCompression/write_uncompressed_read_decompressed_disk_multiple_compression_chunk | TestEncryptionAndCompression/write_uncompressed_read_decompressed_disk_multiple_compression_chunk |
| TestEncryptionAndCompression/write_uncompressed_read_decompressed_inline | TestEncryptionAndCompression/write_uncompressed_read_decompressed_inline |
| TestEncryptionAndCompression/write_uncompressed_read_decompressed_offset_disk_multiple_compression_chunk | TestEncryptionAndCompression/write_uncompressed_read_decompressed_offset_disk_multiple_compression_chunk |
| TestEncryptionAndCompression/write_uncompressed_read_decompressed_offset_inline | TestEncryptionAndCompression/write_uncompressed_read_decompressed_offset_inline |
| TestEncryptionUsesSameKeyForFileRecordAndEncryptor | TestEncryptionUsesSameKeyForFileRecordAndEncryptor |
| TestEvictorDeleteFileGCSBlob | TestEvictorDeleteFileGCSBlob |
| TestEvictorDeleteFileGCSBlob/shared=false | TestEvictorDeleteFileGCSBlob/shared=false |
| TestEvictorDeleteFileGCSBlob/shared=true | TestEvictorDeleteFileGCSBlob/shared=true |
| TestFindMissing | TestFindMissing |
| TestFindMissing/test_size_100 | TestFindMissing/test_size_100 |
| TestFindMissing/test_size_1000 | TestFindMissing/test_size_1000 |
| TestFindMissing/test_size_10000 | TestFindMissing/test_size_10000 |
| TestFindMissing/test_size_1500 | TestFindMissing/test_size_1500 |
| TestFindMissing/test_size_50 | TestFindMissing/test_size_50 |
| TestGCSAtimeUpdateThreshold | TestGCSAtimeUpdateThreshold |
| TestGCSBlobStorage | TestGCSBlobStorage |
| TestGCSBlobStorageOverwriteObjects | TestGCSBlobStorageOverwriteObjects |
| TestGCSBlobStorageReadAfterTTL | TestGCSBlobStorageReadAfterTTL |
| TestGetSet | TestGetSet |
| TestGetSet/test_size:_1 | TestGetSet/test_size:_1 |
| TestGetSet/test_size:_10 | TestGetSet/test_size:_10 |
| TestGetSet/test_size:_100 | TestGetSet/test_size:_100 |
| TestGetSet/test_size:_1000 | TestGetSet/test_size:_1000 |
| TestGetSet/test_size:_10000 | TestGetSet/test_size:_10000 |
| TestGetSet/test_size:_1000000 | TestGetSet/test_size:_1000000 |
| TestGetSet/test_size:_1024 | TestGetSet/test_size:_1024 |
| TestGetSet/test_size:_2048 | TestGetSet/test_size:_2048 |
| TestGetSet/test_size:_256 | TestGetSet/test_size:_256 |
| TestGetSet/test_size:_512 | TestGetSet/test_size:_512 |
| TestGetSizeOnLocalDiskFromView | TestGetSizeOnLocalDiskFromView |
| TestGetSizeOnLocalDiskFromView/disk | TestGetSizeOnLocalDiskFromView/disk |
| TestGetSizeOnLocalDiskFromView/empty | TestGetSizeOnLocalDiskFromView/empty |
| TestGetSizeOnLocalDiskFromView/gcs | TestGetSizeOnLocalDiskFromView/gcs |
| TestGetSizeOnLocalDiskFromView/inline | TestGetSizeOnLocalDiskFromView/inline |
| TestGetSizeOnLocalDiskFromView/no-storage-metadata | TestGetSizeOnLocalDiskFromView/no-storage-metadata |
| TestIsolateAnonUsers | TestIsolateAnonUsers |
| TestIsolateByGroupIds | TestIsolateByGroupIds |
| TestIsolation | TestIsolation |
| TestIsolation/(file_is_written_inline) | TestIsolation/(file_is_written_inline) |
| TestIsolation/(file_is_written_inline)#01 | TestIsolation/(file_is_written_inline)#01 |
| TestIsolation/(file_is_written_inline)#02 | TestIsolation/(file_is_written_inline)#02 |
| TestIsolation/(file_is_written_inline)#03 | TestIsolation/(file_is_written_inline)#03 |
| TestIsolation/(file_is_written_inline)#04 | TestIsolation/(file_is_written_inline)#04 |
| TestIsolation/(file_is_written_on_disk) | TestIsolation/(file_is_written_on_disk) |
| TestIsolation/(file_is_written_on_disk)#01 | TestIsolation/(file_is_written_on_disk)#01 |
| TestIsolation/(file_is_written_on_disk)#02 | TestIsolation/(file_is_written_on_disk)#02 |
| TestIsolation/(file_is_written_on_disk)#03 | TestIsolation/(file_is_written_on_disk)#03 |
| TestIsolation/(file_is_written_on_disk)#04 | TestIsolation/(file_is_written_on_disk)#04 |
| TestLRU | TestLRU |
| TestLRU/disk | TestLRU/disk |
| TestLRU/inline | TestLRU/inline |
| TestMetadata | TestMetadata |
| TestMetadata/testSize:_1000000_AC_compressed | TestMetadata/testSize:_1000000_AC_compressed |
| TestMetadata/testSize:_1000000_AC_uncompressed | TestMetadata/testSize:_1000000_AC_uncompressed |
| TestMetadata/testSize:_1000000_CAS_compressed | TestMetadata/testSize:_1000000_CAS_compressed |
| TestMetadata/testSize:_1000000_CAS_uncompressed | TestMetadata/testSize:_1000000_CAS_uncompressed |
| TestMetadata/testSize:_10000_AC_compressed | TestMetadata/testSize:_10000_AC_compressed |
| TestMetadata/testSize:_10000_AC_uncompressed | TestMetadata/testSize:_10000_AC_uncompressed |
| TestMetadata/testSize:_10000_CAS_compressed | TestMetadata/testSize:_10000_CAS_compressed |
| TestMetadata/testSize:_10000_CAS_uncompressed | TestMetadata/testSize:_10000_CAS_uncompressed |
| TestMetadata/testSize:_1000_AC_compressed | TestMetadata/testSize:_1000_AC_compressed |
| TestMetadata/testSize:_1000_AC_uncompressed | TestMetadata/testSize:_1000_AC_uncompressed |
| TestMetadata/testSize:_1000_CAS_compressed | TestMetadata/testSize:_1000_CAS_compressed |
| TestMetadata/testSize:_1000_CAS_uncompressed | TestMetadata/testSize:_1000_CAS_uncompressed |
| TestMetadata/testSize:_100_AC_compressed | TestMetadata/testSize:_100_AC_compressed |
| TestMetadata/testSize:_100_AC_uncompressed | TestMetadata/testSize:_100_AC_uncompressed |
| TestMetadata/testSize:_100_CAS_compressed | TestMetadata/testSize:_100_CAS_compressed |
| TestMetadata/testSize:_100_CAS_uncompressed | TestMetadata/testSize:_100_CAS_uncompressed |
| TestMetadata/testSize:_10_AC_compressed | TestMetadata/testSize:_10_AC_compressed |
| TestMetadata/testSize:_10_AC_uncompressed | TestMetadata/testSize:_10_AC_uncompressed |
| TestMetadata/testSize:_10_CAS_compressed | TestMetadata/testSize:_10_CAS_compressed |
| TestMetadata/testSize:_10_CAS_uncompressed | TestMetadata/testSize:_10_CAS_uncompressed |
| TestMetadata/testSize:_1_AC_compressed | TestMetadata/testSize:_1_AC_compressed |
| TestMetadata/testSize:_1_AC_uncompressed | TestMetadata/testSize:_1_AC_uncompressed |
| TestMetadata/testSize:_1_CAS_compressed | TestMetadata/testSize:_1_CAS_compressed |
| TestMetadata/testSize:_1_CAS_uncompressed | TestMetadata/testSize:_1_CAS_uncompressed |
| TestMigrateVersions | TestMigrateVersions |
| TestMigrateVersions/key_migration_to_v1 | TestMigrateVersions/key_migration_to_v1 |
| TestMigrateVersions/key_migration_to_v2 | TestMigrateVersions/key_migration_to_v2 |
| TestMigrateVersions/key_migration_to_v3 | TestMigrateVersions/key_migration_to_v3 |
| TestMigrateVersions/key_migration_to_v4 | TestMigrateVersions/key_migration_to_v4 |
| TestMigrateVersions/key_migration_to_v5 | TestMigrateVersions/key_migration_to_v5 |
| TestMigrateVersions/key_migration_to_v6 | TestMigrateVersions/key_migration_to_v6 |
| TestMultiGetSet | TestMultiGetSet |
| TestNoEarlyEviction | TestNoEarlyEviction |
| TestNoEarlyEviction/disk | TestNoEarlyEviction/disk |
| TestNoEarlyEviction/inline_file | TestNoEarlyEviction/inline_file |
| TestPartitionJanitorCutoffThreshold | TestPartitionJanitorCutoffThreshold |
| TestPartitionJanitorCutoffThreshold/higher_threshold_starts_eviction_later | TestPartitionJanitorCutoffThreshold/higher_threshold_starts_eviction_later |
| TestPartitionJanitorCutoffThreshold/lower_threshold_starts_eviction_sooner | TestPartitionJanitorCutoffThreshold/lower_threshold_starts_eviction_sooner |
| TestPartitionMinEvictionAge | TestPartitionMinEvictionAge |
| TestPebbleGCSBackingObjectDeleted | TestPebbleGCSBackingObjectDeleted |
| TestPebbleGCSBackingReadUnavailable | TestPebbleGCSBackingReadUnavailable |
| TestPebbleGCSBlobMissingAfterDelete | TestPebbleGCSBlobMissingAfterDelete |
| TestPebbleGCSBlobPresent | TestPebbleGCSBlobPresent |
| TestPebbleGCSBlobPresent/set | TestPebbleGCSBlobPresent/set |
| TestPebbleGCSBlobPresent/set_multi | TestPebbleGCSBlobPresent/set_multi |
| TestPebbleGCSBlobPresent/writer | TestPebbleGCSBlobPresent/writer |
| TestPebbleGCSWriteFaults | TestPebbleGCSWriteFaults |
| TestPebbleGCSWriteFaults/set | TestPebbleGCSWriteFaults/set |
| TestPebbleGCSWriteFaults/set_multi | TestPebbleGCSWriteFaults/set_multi |
| TestPebbleGCSWriteFaults/writer | TestPebbleGCSWriteFaults/writer |
| TestPresenceCache | TestPresenceCache |
| TestPresenceCache_ExperimentConfig | TestPresenceCache_ExperimentConfig |
| TestPresenceCache_Resize | TestPresenceCache_Resize |
| TestPresenceCache_SetConfig | TestPresenceCache_SetConfig |
| TestPresenceCache_TTLChange | TestPresenceCache_TTLChange |
| TestRatchetDB | TestRatchetDB |
| TestReadEncryptedWrongDigestSize | TestReadEncryptedWrongDigestSize |
| TestReadReference | TestReadReference |
| TestReadWrite | TestReadWrite |
| TestReadWrite/test_size:_1 | TestReadWrite/test_size:_1 |
| TestReadWrite/test_size:_10 | TestReadWrite/test_size:_10 |
| TestReadWrite/test_size:_100 | TestReadWrite/test_size:_100 |
| TestReadWrite/test_size:_1000 | TestReadWrite/test_size:_1000 |
| TestReadWrite/test_size:_10000 | TestReadWrite/test_size:_10000 |
| TestReadWrite/test_size:_1000000 | TestReadWrite/test_size:_1000000 |
| TestReadWrite/test_size:_1024 | TestReadWrite/test_size:_1024 |
| TestReadWrite/test_size:_2048 | TestReadWrite/test_size:_2048 |
| TestReadWrite/test_size:_256 | TestReadWrite/test_size:_256 |
| TestReadWrite/test_size:_512 | TestReadWrite/test_size:_512 |
| TestSampling | TestSampling |
| TestSetOptionDefaults | TestSetOptionDefaults |
| TestSetOptionDefaults_AtimeUpdateThreshold | TestSetOptionDefaults_AtimeUpdateThreshold |
| TestSetOptionDefaults_AtimeUpdateThreshold/cache-wide_min_eviction_age_is_ignored_when_every_partition_overrides_it | TestSetOptionDefaults_AtimeUpdateThreshold/cache-wide_min_eviction_age_is_ignored_when_every_partition_overrides_it |
| TestSetOptionDefaults_AtimeUpdateThreshold/derived_from_the_cache-wide_min_eviction_age | TestSetOptionDefaults_AtimeUpdateThreshold/derived_from_the_cache-wide_min_eviction_age |
| TestSetOptionDefaults_AtimeUpdateThreshold/derived_from_the_smallest_partition_override | TestSetOptionDefaults_AtimeUpdateThreshold/derived_from_the_smallest_partition_override |
| TestSetOptionDefaults_AtimeUpdateThreshold/explicit_threshold_above_half_the_min_eviction_age_is_kept | TestSetOptionDefaults_AtimeUpdateThreshold/explicit_threshold_above_half_the_min_eviction_age_is_kept |
| TestSetOptionDefaults_AtimeUpdateThreshold/explicit_zero_threshold_is_kept | TestSetOptionDefaults_AtimeUpdateThreshold/explicit_zero_threshold_is_kept |
| TestSetOptionDefaults_AtimeUpdateThreshold/negative_threshold_is_derived_like_unset | TestSetOptionDefaults_AtimeUpdateThreshold/negative_threshold_is_derived_like_unset |
| TestSetOptionDefaults_AtimeUpdateThreshold/zero-age_partition_does_not_mask_a_positive_partition_age | TestSetOptionDefaults_AtimeUpdateThreshold/zero-age_partition_does_not_mask_a_positive_partition_age |
| TestSetOptionDefaults_AtimeUpdateThreshold/zero_min_eviction_age_falls_back_to_the_fixed_default | TestSetOptionDefaults_AtimeUpdateThreshold/zero_min_eviction_age_falls_back_to_the_fixed_default |
| TestSizeLimit | TestSizeLimit |
| TestSizeLimit/disk | TestSizeLimit/disk |
| TestSizeLimit/inline | TestSizeLimit/inline |
| TestSpecifiedActiveKeyVersion_ExistingDatabase | TestSpecifiedActiveKeyVersion_ExistingDatabase |
| TestSpecifiedActiveKeyVersion_NewDatabase | TestSpecifiedActiveKeyVersion_NewDatabase |
| TestStartupScan | TestStartupScan |
| TestStartupScan/disk | TestStartupScan/disk |
| TestStartupScan/inline | TestStartupScan/inline |
| TestUnspecifiedActiveKeyVersion_ExistingDatabase | TestUnspecifiedActiveKeyVersion_ExistingDatabase |
| TestUnspecifiedActiveKeyVersion_NewDatabase | TestUnspecifiedActiveKeyVersion_NewDatabase |
| TestWriteReference | TestWriteReference |
| TestWriteReference/clone | TestWriteReference/clone |
| TestWriteReference/digest_mismatch | TestWriteReference/digest_mismatch |
| TestWriteReference/encryption_state_mismatch | TestWriteReference/encryption_state_mismatch |
| TestWriteReference/expired_reference | TestWriteReference/expired_reference |
| TestWriteReference/malformed_reference | TestWriteReference/malformed_reference |
| TestWriteReference/no_shared_storage_backing | TestWriteReference/no_shared_storage_backing |
| TestWriteReference/shared_blob_survives_delete | TestWriteReference/shared_blob_survives_delete |
| TestWriteReference/take_ownership | TestWriteReference/take_ownership |
| TestWriteReference/take_ownership_deletes_the_blob | TestWriteReference/take_ownership_deletes_the_blob |
| TestWriteReference/take_ownership_inherits_the_blob_TTL | TestWriteReference/take_ownership_inherits_the_blob_TTL |
| TestWriteReference/zstd_stored_blob | TestWriteReference/zstd_stored_blob |

The original approxlru cases were executed successfully in the earlier complete companion-target run ([invocation](https://app.buildbuddy.io/invocation/ede58c76-0a3f-4063-8801-59a15fea3ea0)). Final verification preserves both and adds the ordering regression:

| Approxlru before — PASS | After — PASS, 5/5 |
| --- | --- |
| TestPartialEviction | TestPartialEviction |
| TestRefresh | TestRefresh |
| — (new regression) | TestSweepWaitIgnoresRunningSweep |

## PR preparation

The final review correction is committed as `0ffd0b550`. No result.md at any directory appears in branch history, and no result.md is tracked. The root write-up remains on disk at ~/repo/result.md, untracked/ignored.

Verified `git diff --name-only origin/master...HEAD` lists only four Go files:

- enterprise/server/backends/pebble_cache/pebble_cache.go
- enterprise/server/backends/pebble_cache/pebble_cache_test.go
- server/util/approxlru/approxlru.go
- server/util/approxlru/approxlru_test.go

Remote tests used the public fork fetch URL and the internal mirror push URL. Origin fetch is restored to the internal mirror after testing for PR tooling. No push or PR was performed.


---
Part of a coordinated effort to simplify the build and speed up slow tests while preserving coverage. Reviewed by a fresh-context reviewer before opening.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01NPAch3p4ukdf3wsrFsi6r3