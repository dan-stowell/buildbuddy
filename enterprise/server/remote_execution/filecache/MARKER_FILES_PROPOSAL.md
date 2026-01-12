# Marker Files Proposal for FileCache

## Overview

This document proposes adding "marker file" support to the filecache package.
Marker files are entries in the filecache LRU that represent external on-disk
resources (like OCI image layers or ext4 disk images) rather than actual file
content.

## Motivation

Currently, two packages manage their own on-disk caches independently:

1. **ociruntime**: Stores extracted OCI image layers as directories under
   `{cacheRoot}/images/oci/v2/{algorithm}/{hash}`

2. **ociconv** (used by firecracker): Stores ext4 disk images under
   `{cacheRoot}/images/ext4/{hashedImageName}/{imageHash}/containerfs.ext4`

These caches have no size limits and no eviction policy. As executors process
many different container images, disk usage grows unbounded.

By integrating with the filecache, these packages can:
- Benefit from the existing LRU eviction mechanism
- Have their disk usage counted against the executor's configured cache size
- Avoid implementing their own cache management logic

## Key Challenges

### 1. Resources May Be In Use

Unlike regular filecache entries (which use hardlinks), OCI layers and ext4
images may be actively mounted or in use by running containers. The filecache
cannot simply delete them during eviction.

### 2. External Resource Location

The actual data lives outside the filecache directory structure. The filecache
needs to track these resources without moving them.

### 3. Size Accounting

The filecache needs to know the size of external resources to properly manage
LRU capacity.

## Proposed Design

### Core Concept: Marker Files with Eviction Callbacks

A marker file is a small metadata file stored in the filecache that:
1. Points to an external resource path
2. Records the size of that resource (for LRU accounting)
3. Associates an eviction callback that handles cleanup

### New Types

```go
// MarkerEvictionPolicy controls how marker files are evicted.
type MarkerEvictionPolicy interface {
    // CanEvict is called when the filecache wants to evict a marker file.
    // It returns true if the resource can be safely deleted, false if it's
    // currently in use. The resourcePath is the path to the external resource.
    //
    // If CanEvict returns false, the filecache will skip this entry and try
    // evicting other entries first. The entry remains in the LRU but may be
    // retried later.
    CanEvict(ctx context.Context, resourcePath string) bool

    // Evict is called after CanEvict returns true. It should delete the
    // external resource. The marker file in the filecache will be deleted
    // after Evict returns (regardless of error).
    Evict(ctx context.Context, resourcePath string) error
}
```

### New FileCache Methods

```go
// AddMarkerFile adds a marker file to the cache that represents an external
// resource. The resourcePath is the path to the external resource on disk.
// The sizeBytes should reflect the actual disk usage of the resource.
// The policy handles eviction decisions and cleanup.
//
// The node's digest hash is used as the cache key (combined with group ID).
// The node's size field is used for display purposes but sizeBytes is used
// for actual LRU capacity accounting.
func (c *fileCache) AddMarkerFile(
    ctx context.Context,
    node *repb.FileNode,
    resourcePath string,
    sizeBytes int64,
    policy MarkerEvictionPolicy,
) error

// GetMarkerFile returns the resource path for a marker file if it exists
// in the cache. Returns empty string if not found.
func (c *fileCache) GetMarkerFile(
    ctx context.Context,
    node *repb.FileNode,
) (resourcePath string, ok bool)

```

### Implementation Details

#### Entry Structure Changes

```go
type entry struct {
    addedAtUsec  int64
    sizeBytes    int64
    
    // New fields for marker files:
    isMarker     bool
    resourcePath string                // Path to external resource
    evictPolicy  MarkerEvictionPolicy  // Handles eviction
}
```

#### Eviction Logic Changes

The eviction function needs to handle marker files specially:

```go
func evictFn(rootDir string) func(string, *entry, lru.EvictionReason) bool {
    return func(key string, v *entry, reason lru.EvictionReason) bool {
        if v.isMarker {
            ctx := context.Background() // or pass context through
            
            // Check if we can evict
            if reason == lru.SizeEviction && !v.evictPolicy.CanEvict(ctx, v.resourcePath) {
                return false // Signal to LRU to skip this entry
            }
            
            // Perform eviction
            if err := v.evictPolicy.Evict(ctx, v.resourcePath); err != nil {
                log.Warningf("Failed to evict marker file resource %q: %s", v.resourcePath, err)
            }
        } else {
            // Existing behavior for regular files
            fp := filecachePath(rootDir, key)
            if err := syscall.Unlink(fp); err != nil {
                log.Errorf("Failed to unlink filecache entry %q: %s", fp, err)
            }
        }
        
        // Update metrics...
        return true
    }
}
```

**Note:** This requires modifying the LRU package to support eviction callbacks
that can return `false` to indicate "skip this entry". Currently, the LRU
package's `OnEvict` callback is void-returning.

#### LRU Package Changes

Option A: Modify `EvictedCallback` to return bool:
```go
type EvictedCallback[V any] func(key string, value V, reason EvictionReason) bool
```

Option B: Add a separate `CanEvict` callback:
```go
type Config[V any] struct {
    // ... existing fields ...
    CanEvict func(key string, value V) bool  // New
}
```

Option B is more backward-compatible but adds complexity.

### Example Usage: ociruntime

```go
type layerEvictionPolicy struct {
    // Tracks which layers are currently mounted
    activeLayers sync.Map // map[string]int (refcount)
}

func (p *layerEvictionPolicy) CanEvict(ctx context.Context, resourcePath string) bool {
    _, inUse := p.activeLayers.Load(resourcePath)
    return !inUse
}

func (p *layerEvictionPolicy) Evict(ctx context.Context, resourcePath string) error {
    return os.RemoveAll(resourcePath)
}

// When pulling a layer:
func (s *ImageStore) cacheLayer(ctx context.Context, layerPath string, diffID Hash, size int64) error {
    node := &repb.FileNode{
        Digest: &repb.Digest{
            Hash:      diffID.Hex,
            SizeBytes: size,
        },
    }
    return s.fileCache.AddMarkerFile(ctx, node, layerPath, size, s.evictionPolicy)
}

// When mounting a layer:
func (s *ImageStore) useLayer(layerPath string) {
    // Increment refcount
    s.evictionPolicy.activeLayers.Store(layerPath, 1)
}

// When unmounting:
func (s *ImageStore) releaseLayer(layerPath string) {
    s.evictionPolicy.activeLayers.Delete(layerPath)
}
```

### Example Usage: firecracker/ociconv

```go
type ext4ImageEvictionPolicy struct {
    // Tracks which images are currently in use by VMs
    activeImages sync.Map
}

func (p *ext4ImageEvictionPolicy) CanEvict(ctx context.Context, resourcePath string) bool {
    _, inUse := p.activeImages.Load(resourcePath)
    return !inUse
}

func (p *ext4ImageEvictionPolicy) Evict(ctx context.Context, resourcePath string) error {
    // Delete the ext4 file and its parent directory
    if err := os.Remove(resourcePath); err != nil {
        return err
    }
    return os.Remove(filepath.Dir(resourcePath))
}
```

## Alternative Designs Considered

### Alternative 1: Store Empty Files with Extended Attributes

Store empty marker files in the filecache directory, using xattrs to store
the resource path and using the file's apparent size for accounting.

**Pros:**
- Marker files persist across restarts
- Uses existing filecache directory structure

**Cons:**
- Xattr support varies across filesystems
- Sparse files for size representation are hacky
- Still need callback mechanism for eviction

### Alternative 2: Reference Counting Instead of Callbacks

Have consumers acquire/release references to marker files, and only allow
eviction when refcount is 0.

**Pros:**
- Simpler mental model
- No callback complexity

**Cons:**
- Risk of leaked references preventing eviction forever
- Doesn't allow for async "please release soon" signaling

### Alternative 3: Separate LRU for Marker Files

Maintain a completely separate LRU for marker files with its own eviction logic.

**Pros:**
- No changes to existing filecache code
- Can have different eviction strategies

**Cons:**
- Doesn't share capacity with regular filecache
- Duplicates LRU infrastructure
- Two caches to configure and monitor

## Open Questions

1. **Restart recovery**: Should marker files persist across executor restarts?
   - If yes, we need to store metadata on disk
   - If no, external resources become orphaned after restart

2. **What happens when CanEvict keeps returning false?**
   - Should we track "skip count" and force eviction after threshold?
   - Should we have a separate background goroutine that retries?

3. **Should marker files be per-group or global?**
   - OCI layers could potentially be shared across groups
   - But authentication/access control gets complicated

4. **Metrics**: What additional metrics do we need?
   - Marker file eviction attempts vs successes?
   - Time spent waiting for eviction permission?

## Proposed Implementation Plan

1. **Phase 1**: Modify LRU package to support skippable evictions
2. **Phase 2**: Add marker file support to filecache 
3. **Phase 3**: Integrate ociruntime with marker files
4. **Phase 4**: Integrate firecracker/ociconv with marker files
5. **Phase 5**: Add restart recovery (if needed)

## Feedback Requested

- Is the callback-based eviction approach acceptable?
- Should we pursue restart recovery in the initial implementation?
- Are there other use cases for marker files we should consider?
- Should marker files have their own subdirectory/prefix in the cache?
