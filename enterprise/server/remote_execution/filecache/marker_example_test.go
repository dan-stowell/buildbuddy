package filecache_test

import (
	"context"
	"os"
	"sync"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/filecache"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
)

// Example: Using marker files to cache OCI image layers
//
// This example shows how ociruntime could use marker files to integrate
// its layer cache with the filecache's LRU eviction.

// LayerCache manages OCI image layers with filecache-backed eviction.
type LayerCache struct {
	fc     *filecache.FileCache
	policy *layerEvictionPolicy
}

// layerEvictionPolicy tracks which layers are in use and handles cleanup.
type layerEvictionPolicy struct {
	// activeLayers tracks layers currently mounted by containers.
	// Key is the layer path, value is a reference count.
	mu           sync.Mutex
	activeLayers map[string]int
}

func newLayerEvictionPolicy() *layerEvictionPolicy {
	return &layerEvictionPolicy{
		activeLayers: make(map[string]int),
	}
}

// CanEvict returns false if the layer is currently mounted.
func (p *layerEvictionPolicy) CanEvict(ctx context.Context, resourcePath string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.activeLayers[resourcePath] == 0
}

// Evict removes the layer directory from disk.
func (p *layerEvictionPolicy) Evict(ctx context.Context, resourcePath string) error {
	return os.RemoveAll(resourcePath)
}

// Acquire marks a layer as in-use (e.g., when mounting for a container).
func (p *layerEvictionPolicy) Acquire(layerPath string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.activeLayers[layerPath]++
}

// Release marks a layer as no longer in-use (e.g., when container exits).
func (p *layerEvictionPolicy) Release(layerPath string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.activeLayers[layerPath] > 0 {
		p.activeLayers[layerPath]--
	}
	if p.activeLayers[layerPath] == 0 {
		delete(p.activeLayers, layerPath)
	}
}

// NewLayerCache creates a new layer cache backed by the given filecache.
func NewLayerCache(fc *filecache.FileCache) *LayerCache {
	return &LayerCache{
		fc:     fc,
		policy: newLayerEvictionPolicy(),
	}
}

// AddLayer registers a downloaded layer with the cache.
// layerPath is where the extracted layer lives on disk.
// diffID is the layer's content hash (e.g., "sha256:abc123...").
// sizeBytes is the disk usage of the extracted layer.
func (lc *LayerCache) AddLayer(ctx context.Context, layerPath, diffID string, sizeBytes int64) error {
	node := &repb.FileNode{
		Digest: &repb.Digest{
			Hash:      diffID,
			SizeBytes: sizeBytes,
		},
	}
	return lc.fc.AddMarkerFile(ctx, node, layerPath, sizeBytes, lc.policy)
}

// GetLayer returns the path to a cached layer, or empty string if not cached.
// If found, this also marks the layer as recently used in the LRU.
func (lc *LayerCache) GetLayer(ctx context.Context, diffID string, sizeBytes int64) (string, bool) {
	node := &repb.FileNode{
		Digest: &repb.Digest{
			Hash:      diffID,
			SizeBytes: sizeBytes,
		},
	}
	return lc.fc.GetMarkerFile(ctx, node)
}

// UseLayer marks a layer as in-use and returns a release function.
// Call the release function when the layer is no longer needed.
func (lc *LayerCache) UseLayer(layerPath string) (release func()) {
	lc.policy.Acquire(layerPath)
	return func() {
		lc.policy.Release(layerPath)
	}
}

// Example usage in ociruntime's Pull method:
//
//   func (s *ImageStore) Pull(ctx context.Context, imageName string, creds Credentials) (*Image, error) {
//       img, err := s.resolver.Resolve(ctx, imageName, creds)
//       if err != nil {
//           return nil, err
//       }
//
//       layers, _ := img.Layers()
//       for _, layer := range layers {
//           diffID, _ := layer.DiffID()
//           size, _ := layer.Size()
//           layerPath := filepath.Join(s.layersDir, diffID.Algorithm, diffID.Hex)
//
//           // Check if layer is already cached
//           if cachedPath, ok := s.layerCache.GetLayer(ctx, diffID.Hex, size); ok {
//               // Layer exists and is now marked as recently used
//               continue
//           }
//
//           // Download and extract layer to layerPath
//           if err := downloadLayer(ctx, layer, layerPath); err != nil {
//               return nil, err
//           }
//
//           // Register with filecache for LRU management
//           if err := s.layerCache.AddLayer(ctx, layerPath, diffID.Hex, size); err != nil {
//               return nil, err
//           }
//       }
//       // ...
//   }
//
// Example usage when running a container:
//
//   func (c *Container) Run(ctx context.Context) error {
//       // Mark all layers as in-use before mounting
//       var releases []func()
//       for _, layer := range c.image.Layers {
//           release := c.layerCache.UseLayer(layer.Path)
//           releases = append(releases, release)
//       }
//       defer func() {
//           for _, release := range releases {
//               release()
//           }
//       }()
//
//       // Mount overlayfs and run container...
//       return c.runWithLayers()
//   }
