# Executor file-cache transfers

Action workspace inputs and files admitted from workspaces use independent
inodes by default. An action rewriting an input in place therefore cannot
change the cached digest. Transfers use reflinks where enabled and supported,
otherwise byte copies. Without reflinks, live workspace inputs require their
own disk allocation in addition to the file-cache budget. Allow room for live
workspaces and admission temp files when sizing executor disks.

`FileCache.MaterializeWritable` is for action inputs; `AddFile` admits files
that their owner may subsequently modify. `FastLinkFile` remains a safe
compatibility alias for `MaterializeWritable`.

`LinkReadOnly` and `AddReadOnly` permit inode sharing. Their callers must never
modify the shared inode. These methods serve immutable ext4 images and VM
snapshot files/chunks; reads also use this path. Write and Writer.Commit move
private temporary files into the cache without copying their bytes. Duplicate
digest admissions through AddFile, AddReadOnly, and Writer retain the existing
entry and skip the transfer. Write retains replacement semantics for mutable
snapshot manifest keys, publishing a new inode atomically. Callers
must use DeleteFile before replacing a known bad digest entry.

`executor.local_cache_trusted_workspace_hardlinks` defaults to false. Setting
it to true opts trusted workloads out of workspace mutation protection, for
both workspace admission and materialization. Inputs and admitted outputs must
never be modified in place when this option is enabled. Violating this contract
can corrupt cached inputs and subsequent action results.

`executor.local_cache_always_clone` forces independent inodes even for read-only
transfers and overrides the trusted-workload opt-out. Private temp commits still
rename. `executor.local_cache_use_hardlinks` is a macOS fastcopy option: it
controls read-only transfers and trusted-workload transfers, but does not bypass
default workspace isolation. On Linux, read-only transfers use hardlinks.
`executor.enable_fastcopy_reflinking` enables Linux reflink attempts; unsupported
filesystems fall back to copying for writable transfers.
