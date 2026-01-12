package filecache

import (
	"context"
	"time"

	"github.com/buildbuddy-io/buildbuddy/server/interfaces"
	"github.com/buildbuddy-io/buildbuddy/server/metrics"
	"github.com/buildbuddy-io/buildbuddy/server/util/log"
	"github.com/prometheus/client_golang/prometheus"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

// MarkerEvictionPolicy is an alias for interfaces.MarkerEvictionPolicy.
// It's exported here for convenience.
type MarkerEvictionPolicy = interfaces.MarkerEvictionPolicy

// markerFileSuffix is appended to marker file keys to distinguish them from
// regular file entries with the same digest.
const markerFileSuffix = ".marker"

func markerKey(ctx context.Context, node *repb.FileNode) string {
	return key(ctx, node) + markerFileSuffix
}

// AddMarkerFile adds a marker file to the cache that represents an external
// resource at resourcePath. The sizeBytes parameter should reflect the actual
// disk usage of the external resource, which is used for LRU capacity accounting.
//
// The node's digest hash (combined with the group ID from ctx) is used as the
// cache key. When the filecache needs to evict this entry, it will consult the
// provided policy.
//
// If a marker file with the same key already exists, it is replaced.
func (c *fileCache) AddMarkerFile(
	ctx context.Context,
	node *repb.FileNode,
	resourcePath string,
	sizeBytes int64,
	policy MarkerEvictionPolicy,
) error {
	k := markerKey(ctx, node)

	c.lock.Lock()
	defer c.lock.Unlock()

	// Remove any existing entry with this key
	c.l.Remove(k)

	e := &entry{
		addedAtUsec:  time.Now().UnixMicro(),
		sizeBytes:    sizeBytes,
		isMarker:     true,
		resourcePath: resourcePath,
		evictPolicy:  policy,
	}

	groupID := groupIDStringFromContext(ctx)
	metrics.FileCacheAddedFileSizeBytes.Observe(float64(e.sizeBytes))
	metrics.FileCacheAddedFileBytesCount.With(prometheus.Labels{
		metrics.GroupID: groupID,
	}).Add(float64(e.sizeBytes))

	if !c.l.Add(k, e) {
		log.CtxWarningf(ctx, "Failed to add marker file %q to filecache LRU (cache full of non-evictable entries?)", k)
		// Note: we don't return an error here because the resource still exists
		// on disk, it just won't be tracked by the LRU. The caller can still
		// use the resource.
	}
	return nil
}

// GetMarkerFile returns the resource path for a marker file if it exists in
// the cache. This also updates the entry's position in the LRU (marks it as
// recently used).
//
// Returns the resource path and true if found, or empty string and false if
// the marker file is not in the cache.
func (c *fileCache) GetMarkerFile(
	ctx context.Context,
	node *repb.FileNode,
) (resourcePath string, ok bool) {
	k := markerKey(ctx, node)

	c.lock.Lock()
	defer c.lock.Unlock()

	e, ok := c.l.Get(k)
	if !ok {
		return "", false
	}
	if !e.isMarker {
		// Shouldn't happen, but be defensive
		log.CtxWarningf(ctx, "GetMarkerFile called but entry %q is not a marker file", k)
		return "", false
	}
	return e.resourcePath, true
}
