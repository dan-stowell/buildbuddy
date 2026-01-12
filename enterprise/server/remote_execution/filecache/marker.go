package filecache

import (
	"context"

	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

// MarkerEvictionPolicy controls how marker files are evicted from the cache.
//
// Marker files represent external on-disk resources (like OCI image layers or
// ext4 disk images). When the filecache needs to evict a marker file to free
// space, it consults the policy to determine if eviction is allowed and to
// perform the actual cleanup.
type MarkerEvictionPolicy interface {
	// CanEvict is called when the filecache wants to evict a marker file.
	// It returns true if the resource can be safely deleted, false if it's
	// currently in use (e.g., mounted by a running container).
	//
	// If CanEvict returns false, the filecache will skip this entry and try
	// evicting other entries first. The entry remains in the LRU and may be
	// retried later.
	CanEvict(ctx context.Context, resourcePath string) bool

	// Evict is called after CanEvict returns true. It should delete the
	// external resource at resourcePath. The marker file entry in the
	// filecache LRU will be removed after Evict returns, regardless of
	// whether Evict returns an error.
	Evict(ctx context.Context, resourcePath string) error
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
	// TODO: implement
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
	// TODO: implement
	return "", false
}
