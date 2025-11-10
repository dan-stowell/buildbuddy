# Container Image Fetching Simplification Plan

## Executive Summary

The current container image fetching architecture spans 6+ packages with multiple cache layers, complex authentication chains, and dual image formats. This document proposes a simplified interface and data flow that maintains performance while reducing complexity.

## Current Architecture Analysis

### Pain Points

1. **Multiple Cache Layers**: In-memory → on-disk → AC+CAS → registry, each with different TTLs and invalidation strategies
2. **Credential Complexity**: 3-level fallback (platform properties → executor config → default keychain) evaluated at multiple points
3. **Dual Image Formats**: OCI overlayfs layers and Firecracker ext4 images don't share cache or code paths
4. **Scattered Concerns**: Authentication, resolution, caching, and format conversion spread across packages
5. **Concurrent Deduplication**: Multiple singleflight groups for images, layers, and conversions
6. **Anonymous User Handling**: Different code paths based on authentication status

### Current Data Flow

```
User Request
    ↓
[container] PullImageIfNecessary() + ImageCacheAuthenticator
    ↓
[ociruntime/firecracker] Implementation-specific pull
    ↓
[oci] Resolve() + authentication chain
    ↓
[ocicache] Manifest/layer lookup in AC+CAS
    ↓
[Registry] Remote fetch if cache miss
    ↓
[ociruntime] Extract to overlayfs layers on disk
 OR
[ociconv] Convert to ext4 disk image
```

## Proposed Simplified Architecture

### Core Principles

1. **Single Source of Truth**: One package owns image fetching, delegates to specialists
2. **Layered Caching**: Clear hierarchy with consistent invalidation
3. **Explicit Materialization**: Separate image fetch from format conversion
4. **Pluggable Auth**: Authentication as a separate concern passed to fetcher
5. **Observability**: Built-in metrics and tracing at layer boundaries

### Package Responsibilities

```
┌─────────────────────────────────────────────────────────────┐
│                    imagefetch (NEW)                          │
│  Orchestrates all image fetching with unified interface     │
│  - Single entry point for all image operations              │
│  - Manages cache hierarchy                                   │
│  - Handles deduplication                                     │
│  - Returns abstract Image representation                     │
└─────────────────────────────────────────────────────────────┘
                            ↓
        ┌──────────────────┼──────────────────┐
        ↓                  ↓                   ↓
┌──────────────┐  ┌──────────────┐  ┌──────────────┐
│  imageauth   │  │  imagecache  │  │ imageregistry│
│   (NEW)      │  │   (NEW)      │  │    (NEW)     │
│              │  │              │  │              │
│ Credentials  │  │ Multi-tier   │  │ Registry     │
│ Resolution   │  │ Cache        │  │ Client       │
└──────────────┘  └──────────────┘  └──────────────┘
                            ↓
        ┌──────────────────┼──────────────────┐
        ↓                  ↓                   ↓
┌──────────────┐  ┌──────────────┐  ┌──────────────┐
│ imagematerialize│ │  container   │  │   (apps)     │
│    (NEW)       │  │              │  │              │
│ OCI → overlayfs│  │ Interface    │  │ Executors    │
│ OCI → ext4     │  │ Provider     │  │              │
└──────────────┘  └──────────────┘  └──────────────┘
```

### New Package Structure

#### 1. `imagefetch` - Orchestration Layer

**Purpose**: Single entry point for all image operations

```go
package imagefetch

// Fetcher is the main interface for fetching container images
type Fetcher interface {
    // FetchImage returns an abstract image that can be materialized to different formats
    FetchImage(ctx context.Context, ref ImageReference, creds Credentials) (*Image, error)

    // IsImageCached checks if image is available in any cache tier
    IsImageCached(ctx context.Context, ref ImageReference) (bool, error)

    // InvalidateCache removes image from all cache tiers
    InvalidateCache(ctx context.Context, ref ImageReference) error
}

// ImageReference represents an image (tag or digest)
type ImageReference struct {
    Registry   string
    Repository string
    Reference  string  // tag or digest
    Platform   Platform
}

// Image is an abstract representation of a container image
type Image struct {
    Digest       digest.Digest
    Manifest     *Manifest
    Config       *ConfigFile
    Layers       []*Layer

    // Metadata for observability
    Source       ImageSource  // cache tier that provided image
    FetchMetrics Metrics
}

// Layer represents an image layer with lazy loading
type Layer struct {
    Digest      digest.Digest
    Size        int64
    MediaType   string

    // Open returns a reader for layer contents
    // Handles cache-through automatically
    Open(ctx context.Context) (io.ReadCloser, error)
}

type ImageSource int
const (
    SourceMemoryCache ImageSource = iota
    SourceBuildBuddyCache
    SourceDiskCache
    SourceRemoteRegistry
)

// Credentials are opaque to the fetcher
type Credentials struct {
    Username string
    Password string
    // Future: could add token, certificate, etc.
}

// Config for creating a fetcher
type Config struct {
    Cache          CacheConfig
    Auth           AuthResolver
    Registry       RegistryConfig
    Env            environment.Env
}
```

**Implementation Details**:
- Single singleflight group keyed by (ImageReference, Credentials)
- Checks cache tiers in order: memory → BuildBuddy AC+CAS → disk → registry
- Returns `Image` with lazy layers that cache-through on read
- Metrics recorded: cache hit rate, fetch latency, bytes transferred per tier

#### 2. `imageauth` - Credential Resolution

**Purpose**: Single place to resolve credentials for an image

```go
package imageauth

// Resolver determines credentials for accessing an image
type Resolver interface {
    // ResolveCredentials returns credentials for accessing an image
    // Returns empty credentials if no auth required
    ResolveCredentials(ctx context.Context, ref ImageReference) (Credentials, error)
}

// ChainResolver tries multiple sources in order
type ChainResolver struct {
    resolvers []Resolver
}

// Implementation resolvers (in priority order):
type PlatformPropertiesResolver struct{}  // From platform.Properties
type ExecutorConfigResolver struct{}      // From --executor.container_registries
type DefaultKeychainResolver struct{}     // From docker/podman config

// CredentialCache wraps a resolver with TTL caching
type CredentialCache struct {
    resolver Resolver
    ttl      time.Duration
    cache    *lru.LRU[cacheKey, cachedCreds]
}

// Helper to create standard auth chain
func NewStandardResolver(env environment.Env) Resolver {
    chain := &ChainResolver{
        resolvers: []Resolver{
            &PlatformPropertiesResolver{env},
            &ExecutorConfigResolver{env},
            &DefaultKeychainResolver{env},
        },
    }
    return &CredentialCache{
        resolver: chain,
        ttl:      15 * time.Minute,
    }
}
```

**Key Improvements**:
- Credentials resolved once, passed through entire fetch pipeline
- No credential evaluation inside hot path
- Easy to add new credential sources
- Explicit caching with configurable TTL
- Testable without side effects

#### 3. `imagecache` - Unified Cache Layer

**Purpose**: Single interface to multi-tier cache

```go
package imagecache

// Cache provides multi-tier caching for images
type Cache interface {
    // Manifest operations
    GetManifest(ctx context.Context, ref ImageReference) (*Manifest, error)
    PutManifest(ctx context.Context, ref ImageReference, manifest *Manifest) error

    // Layer operations return readers that may be cached or remote
    GetLayer(ctx context.Context, digest digest.Digest) (io.ReadCloser, error)
    PutLayer(ctx context.Context, digest digest.Digest, reader io.Reader) error

    // Config operations
    GetConfig(ctx context.Context, digest digest.Digest) (*ConfigFile, error)
    PutConfig(ctx context.Context, digest digest.Digest, config *ConfigFile) error

    // Invalidation
    Invalidate(ctx context.Context, ref ImageReference) error
}

// TieredCache implements Cache with multiple tiers
type TieredCache struct {
    tiers []CacheTier
}

// CacheTier is a single cache layer
type CacheTier interface {
    Name() string
    Get(ctx context.Context, key CacheKey) ([]byte, error)
    Put(ctx context.Context, key CacheKey, value []byte) error
}

// Standard tier implementations:
type MemoryTier struct {
    lru *lru.LRU[CacheKey, []byte]
}

type BuildBuddyTier struct {
    cache interfaces.Cache  // AC+CAS
}

type DiskTier struct {
    rootDir string
}

// ReadThroughCache wraps a reader to write to cache as it's read
type ReadThroughCache struct {
    reader io.ReadCloser
    cache  Cache
    digest digest.Digest
}
```

**Key Improvements**:
- Single cache interface, multiple implementations
- Clear tier ordering and fallback
- Write-through and read-through supported
- Metrics per tier: hit rate, latency, size
- Anonymous users can be handled by skipping BuildBuddyTier

#### 4. `imageregistry` - Registry Client

**Purpose**: Interact with remote container registries

```go
package imageregistry

// Client fetches images from remote registries
type Client interface {
    // ResolveDigest converts tag to digest (with caching)
    ResolveDigest(ctx context.Context, ref ImageReference, creds Credentials) (digest.Digest, error)

    // FetchManifest retrieves image manifest
    FetchManifest(ctx context.Context, ref ImageReference, creds Credentials) (*Manifest, error)

    // FetchLayer retrieves layer by digest
    FetchLayer(ctx context.Context, ref ImageReference, digest digest.Digest, creds Credentials) (io.ReadCloser, error)

    // FetchConfig retrieves config file
    FetchConfig(ctx context.Context, ref ImageReference, digest digest.Digest, creds Credentials) (*ConfigFile, error)
}

// StandardClient wraps go-containerregistry with BuildBuddy specifics
type StandardClient struct {
    mirrorConfig MirrorConfig
    allowedIPs   []*net.IPNet
    tagCache     *lru.LRU[string, digest.Digest]  // Tag resolution cache
}

// MirrorConfig supports registry mirrors/proxies
type MirrorConfig struct {
    Mirrors map[string][]string  // registry -> mirror URLs
}
```

**Key Improvements**:
- Single responsibility: talk to registries
- Credentials passed explicitly, no resolution here
- Mirror/proxy logic contained
- Tag resolution caching explicit and configurable
- Easy to mock for testing

#### 5. `imagematerialize` - Format Conversion

**Purpose**: Convert abstract Image to concrete formats

```go
package imagematerialize

// Materializer converts an abstract image to a concrete format
type Materializer interface {
    // Materialize converts image to desired format and returns path or metadata
    Materialize(ctx context.Context, image *imagefetch.Image, opts MaterializeOptions) (*MaterializeResult, error)

    // IsMaterialized checks if image is already in desired format
    IsMaterialized(ctx context.Context, image *imagefetch.Image, opts MaterializeOptions) (bool, error)

    // Cleanup removes materialized artifacts
    Cleanup(ctx context.Context, result *MaterializeResult) error
}

type MaterializeOptions struct {
    Format       ImageFormat
    CacheDir     string
    WorkDir      string
    NetworkPool  *networking.Pool  // Optional, for OCI
}

type ImageFormat int
const (
    FormatOverlayFS ImageFormat = iota  // For OCI runtime
    FormatExt4                          // For Firecracker
)

type MaterializeResult struct {
    Format    ImageFormat

    // For OverlayFS
    LowerDirs []string
    UpperDir  string
    WorkDir   string

    // For Ext4
    DiskImagePath string

    // Metadata
    SizeBytes     int64
    MaterializeTime time.Duration
}

// Implementations
type OverlayFSMaterializer struct {
    cacheDir       string
    pullGroup      singleflight.Group  // Dedupe layer extractions
}

type Ext4Materializer struct {
    cacheDir       string
    conversionGroup singleflight.Group  // Dedupe conversions
}
```

**Key Improvements**:
- Materialization is explicit, not hidden in fetch
- Both formats use same abstract Image
- Easy to add new formats (e.g., Kata, gVisor)
- Materialization can be cached separately from fetch
- Clear ownership of disk resources

### Simplified Data Flow

```
┌─────────────────────────────────────────────────────────────┐
│ 1. Executor needs image for remote execution action         │
└────────────────────────┬────────────────────────────────────┘
                         ↓
┌─────────────────────────────────────────────────────────────┐
│ 2. imageauth.Resolver.ResolveCredentials(ref)               │
│    → Checks cache (TTL 15min)                                │
│    → If miss: platform props → executor config → keychain   │
│    → Returns: Credentials                                    │
└────────────────────────┬────────────────────────────────────┘
                         ↓
┌─────────────────────────────────────────────────────────────┐
│ 3. imagefetch.Fetcher.FetchImage(ref, creds)                │
│    → Singleflight deduplication by (ref, creds)             │
│    → Returns: *Image (abstract representation)              │
└────────────────────────┬────────────────────────────────────┘
                         ↓
                ┌────────┴────────┐
                ↓                 ↓
┌──────────────────────────┐  ┌──────────────────────────┐
│ 3a. Check Cache          │  │ 3b. Check Registry       │
│                          │  │                          │
│ imagecache.GetManifest() │  │ imageregistry.           │
│   ↓                      │  │   FetchManifest()        │
│ MemoryTier               │  │                          │
│   ↓ (miss)               │  │ + Credentials            │
│ BuildBuddyTier (AC+CAS)  │  │ + Mirror support         │
│   ↓ (miss)               │  │                          │
│ DiskTier                 │  │ Store in cache tiers     │
│   ↓ (miss)          ────────→ (BuildBuddy, Disk)       │
│                          │  │                          │
│ Manifest → Parse layers  │  │ Manifest → Parse layers  │
└──────────┬───────────────┘  └──────────┬───────────────┘
           ↓                             ↓
┌─────────────────────────────────────────────────────────────┐
│ 4. Return Image with lazy layers                            │
│    - Manifest, Config in memory                             │
│    - Layers have Open() method                              │
│    - First Open() fetches and caches layer                  │
└────────────────────────┬────────────────────────────────────┘
                         ↓
┌─────────────────────────────────────────────────────────────┐
│ 5. imagematerialize.Materializer.Materialize(image, opts)   │
│    → Check if already materialized                          │
│    → If not: iterate image.Layers                           │
└────────────────────────┬────────────────────────────────────┘
                         ↓
         ┌───────────────┴───────────────┐
         ↓                               ↓
┌─────────────────────┐         ┌─────────────────────┐
│ 5a. OverlayFS       │         │ 5b. Ext4            │
│                     │         │                     │
│ For each layer:     │         │ Extract all layers  │
│   layer.Open()      │         │ to temp dir:        │
│     ↓               │         │   layer.Open()      │
│   Cache tier lookup │         │     ↓               │
│     ↓ (miss)        │         │   Cache tier lookup │
│   Registry fetch    │         │     ↓ (miss)        │
│     ↓               │         │   Registry fetch    │
│   Read-through cache│         │     ↓               │
│     ↓               │         │   Read to disk      │
│   Extract to        │         │                     │
│   cacheDir/layers/  │         │ mke2fs → ext4 image │
│                     │         │   ↓                 │
│ Return OverlayFS    │         │ Store in cache      │
│ mount points        │         │   ↓                 │
│                     │         │ Return disk path    │
└─────────────────────┘         └─────────────────────┘
                         ↓
┌─────────────────────────────────────────────────────────────┐
│ 6. Container implementation uses materialized image          │
│    - ociruntime: Sets up overlayfs mount                    │
│    - firecracker: Attaches ext4 as block device             │
└─────────────────────────────────────────────────────────────┘
```

### Cache Hierarchy Details

```
Request for layer digest "sha256:abcd..."

1. MemoryTier (in-process)
   Key: digest
   TTL: LRU eviction only
   Size: ~100MB (configurable)
   Hit: → Return []byte
   Miss: ↓

2. BuildBuddyTier (AC+CAS, remote)
   Key: hash(registry + repo + digest + groupID)
   Storage: CAS (compressed with zstd)
   Hit: → Stream from CAS → Cache in MemoryTier → Return
   Miss: ↓
   Skip if: Anonymous user

3. DiskTier (local filesystem)
   Key: digest → cacheDir/layers/{algo}/{digest}/data
   Hit: → Read file → Cache in BuildBuddyTier + MemoryTier → Return
   Miss: ↓

4. Registry (remote)
   Request: HTTP GET to registry with credentials
   Response: → Stream
           → ReadThroughCache writes to BuildBuddyTier + DiskTier
           → Return

Metrics per tier:
- Hits / Misses
- Bytes served
- Latency (p50, p95, p99)
- Errors
```

### Migration Path

This is a significant refactoring. Suggested migration approach:

#### Phase 1: Create new packages alongside old (1-2 weeks)
- Implement `imageauth`, `imagecache`, `imageregistry` packages
- Add comprehensive tests for each
- Wire up to `imagefetch` orchestrator
- Run in shadow mode with metrics

#### Phase 2: Migrate OCI runtime (1 week)
- Update `ociruntime.ImageStore` to use `imagefetch` + `imagematerialize`
- Keep old code path behind feature flag
- A/B test with real traffic
- Compare performance and error rates

#### Phase 3: Migrate Firecracker (1 week)
- Update Firecracker to use `imagefetch` + `imagematerialize`
- Consolidate with OCI runtime for image fetching
- Only materialization differs

#### Phase 4: Remove old packages (1 week)
- Delete/archive `oci`, `ocicache`, `ociconv` packages
- Update all references
- Clean up feature flags
- Update documentation

#### Phase 5: Optimize (ongoing)
- Tune cache sizes and TTLs based on production metrics
- Add predictive prefetching
- Implement cache warming for common images
- Consider cross-region cache replication

## Benefits of Simplified Architecture

### For Developers

1. **Single Entry Point**: Only need to understand `imagefetch.Fetcher` interface
2. **Clear Separation**: Auth, caching, registry, materialization are separate concerns
3. **Testability**: Each package can be tested independently with mocks
4. **Debuggability**: Metrics per tier make it easy to identify bottlenecks
5. **Extensibility**: New formats (Kata, gVisor) just implement `Materializer`

### For Operations

1. **Observability**: Clear metrics at each tier (memory → BB cache → disk → registry)
2. **Cache Management**: Single `InvalidateCache()` method clears all tiers
3. **Configuration**: Centralized cache config instead of spread across packages
4. **Troubleshooting**: Data flow is predictable and traceable

### For Performance

1. **Deduplication**: Single singleflight group instead of multiple
2. **Read-through Caching**: Automatic cache population on fetch
3. **Lazy Loading**: Layers not fetched until needed
4. **Parallel Fetching**: Materializer can parallelize layer extraction
5. **Metrics-Driven**: Easy to identify and optimize slow paths

## Open Questions

1. **Backward Compatibility**: How to handle existing on-disk cache formats?
   - Suggested: Migrate lazily on access, or bulk migration script

2. **Cache Eviction**: Who manages disk space limits?
   - Suggested: `imagecache.DiskTier` manages LRU eviction with size limits

3. **Multi-Platform Images**: Should `ImageReference` include platform?
   - Suggested: Yes, makes cache keys clearer

4. **Anonymous Users**: Skip BuildBuddyTier or use limited cache?
   - Suggested: Skip for now, revisit if causing registry load

5. **Mirror Failover**: Should mirrors be tried in parallel or sequential?
   - Suggested: Sequential with timeout, parallel can cause thundering herd

6. **Credential Refresh**: How to handle expired tokens during long pulls?
   - Suggested: `Layer.Open()` re-resolves creds if 401/403

7. **Cache Warming**: Proactive cache population for common images?
   - Suggested: Phase 5 optimization, not needed for MVP

8. **Cross-Region Caching**: Share BuildBuddy cache across regions?
   - Suggested: Already supported by BuildBuddy cache, no changes needed

## Success Metrics

Track these before and after migration:

1. **Complexity**
   - Lines of code in image fetching path
   - Number of packages involved
   - Cyclomatic complexity of hot paths

2. **Performance**
   - P50/P95/P99 image fetch latency
   - Cache hit rates per tier
   - Bytes transferred from registry
   - Number of duplicate fetches (should decrease)

3. **Reliability**
   - Error rate on image pulls
   - Timeout rate
   - Authentication failure rate

4. **Developer Experience**
   - Time to add new isolation type
   - Test coverage percentage
   - Onboarding documentation length

Target improvements:
- 30% reduction in code complexity
- 5% reduction in P95 latency (from better deduplication)
- 10% increase in cache hit rate (from unified cache)
- 50% faster to add new isolation type

## Conclusion

The proposed architecture simplifies container image fetching by:

1. **Single orchestrator** (`imagefetch`) instead of scattered logic
2. **Explicit concerns** (auth, cache, registry, materialization) as separate packages
3. **Layered caching** with clear hierarchy and consistent invalidation
4. **Abstract representation** (`Image`) that works for all materialization formats
5. **Built-in observability** at each layer boundary

This maintains current performance characteristics while significantly reducing complexity and improving developer experience.

The migration can be done incrementally with low risk, and success can be measured objectively with metrics.
