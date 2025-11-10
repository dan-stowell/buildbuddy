# Container Image Flow for Remote Execution

This document traces the data flow for how remote execution fetches container images, with specific focus on how **image name**, **credentials**, and **platform info** flow through the system.

## Table of Contents

1. [Overview](#overview)
2. [Data Flow Diagram](#data-flow-diagram)
3. [Detailed Flow](#detailed-flow)
4. [Key Components](#key-components)
5. [Container Implementations](#container-implementations)

## Overview

BuildBuddy's remote execution system supports multiple container isolation types (Docker, Podman, Firecracker, OCI runtime, etc.). Container images are pulled on-demand before task execution, with sophisticated credential resolution, caching, and authentication mechanisms.

## Data Flow Diagram

```
Remote Execution Request
  └─> Platform Properties Extraction
      └─> Credential Resolution
          └─> Image Pull Orchestration
              └─> Container-Specific Pull Implementation
                  └─> Registry Authentication & Image Fetch
```

## Detailed Flow

### 1. Platform Properties Extraction

**Location:** `platform/platform.go`

When a remote execution task arrives, platform properties are parsed from the execution request:

```go
// ParseProperties extracts platform properties from ExecutionTask (lines 348-503)
func ParseProperties(task *repb.ExecutionTask) (*Properties, error)
```

**Key Properties Extracted:**
- **Image Name:** `container-image` property (line 466)
  - Example: `"docker://gcr.io/flame-public/rbe-ubuntu20-04@sha256:..."`
  - Stored in `Properties.ContainerImage` (line 197)

- **Credentials:** (lines 467-468)
  - `container-registry-username` → `Properties.ContainerRegistryUsername` (line 198)
  - `container-registry-password` → `Properties.ContainerRegistryPassword` (line 199)

- **Platform Info:** (lines 456-457)
  - `OSFamily` → `Properties.OS` (line 188) - defaults to "linux"
  - `Arch` → `Properties.Arch` (line 189) - defaults to "amd64"

**Normalization** (lines 639-661):
- The `docker://` prefix is stripped from the image name
- Empty or "none" values use the executor's default image
- Region placeholders like `{{region}}` are replaced if configured

**Remote Header Overrides:**
Platform properties can be overridden via gRPC metadata headers with the prefix `x-buildbuddy-platform.` (lines 507-528). This allows modifying properties without changing the cached Action digest.

### 2. Credential Resolution

**Location:** `util/oci/oci.go`

Credentials are resolved with a three-tier fallback mechanism:

```go
// CredentialsFromProperties resolves credentials (lines 128-181)
func CredentialsFromProperties(props *platform.Properties) (Credentials, error)
```

**Resolution Order:**

1. **Platform Properties** (lines 136-152)
   - Uses `container-registry-username` and `container-registry-password` if provided
   - Admin-only: `container-registry-bypass` skips registry auth (line 136)

2. **Executor Config** (lines 156-172)
   - Falls back to `--executor.container_registries` flag
   - Matches image hostname against configured registries
   - Returns matching username/password

3. **Default Keychain** (lines 176-210)
   - Falls back to Docker/Podman config files (`~/.docker/config.json`, `$XDG_RUNTIME_DIR/containers/auth.json`)
   - May invoke credential helpers if configured
   - Only enabled if `--executor.container_registry_default_keychain_enabled` is set

**Credentials Structure** (lines 111-118):
```go
type Credentials struct {
    Username       string
    Password       string
    bypassRegistry bool  // admin-only flag to skip registry
}
```

### 3. Runner Preparation

**Location:** `runner/runner.go`

Before executing a task, the runner prepares the container and pulls the image:

```go
// PrepareForTask pulls the container image (lines 254-283)
func (r *taskRunner) PrepareForTask(ctx context.Context) error {
    // Extract credentials from platform properties
    creds, err := r.pullCredentials()  // line 250-252

    // Pull image if necessary
    err = container.PullImageIfNecessary(
        ctx, r.env,
        r.Container, creds, r.PlatformProperties.ContainerImage,
    )  // lines 274-280
}
```

### 4. Image Pull Orchestration

**Location:** `container/container.go`

The pull orchestration layer manages caching, authentication tokens, and synchronization:

```go
// PullImageIfNecessary orchestrates the pull (lines 494-564)
func PullImageIfNecessary(ctx context.Context, env environment.Env,
    ctr CommandContainer, creds oci.Credentials, imageRef string) error
```

**Key Features:**

**A. Timeout Management** (lines 504-508):
- Default 5-minute timeout via `--executor.image_pull_timeout`
- Returns `Unavailable` error on timeout to allow client retry

**B. Image Cache Authentication** (lines 547-563):
- Creates short-lived tokens (15 min TTL) to avoid repeated registry auth
- Token includes: `(groupID, imageRef)` tuple
- Stored in `imageCacheAuthenticator` (lines 585-631)

**C. Pull Synchronization** (lines 535-542):
- Uses `sync.Map` keyed by `(isolationType, imageRef)`
- Prevents duplicate concurrent pulls of the same image
- One pull operation per unique image+isolation combination

**D. Cache Check** (lines 543-546):
- Calls `IsImageCached(ctx)` on the container implementation
- Skips pull if image exists locally and credentials are authorized

### 5. OCI Image Resolution

**Location:** `util/oci/oci.go`

For OCI-based containers (Firecracker, OCI runtime), the resolver handles registry operations:

```go
// Resolve fetches image from registry or cache (lines 361-424)
func (r *Resolver) Resolve(ctx context.Context, imageName string,
    platform *rgpb.Platform, credentials Credentials) (gcr.Image, error)
```

**Platform-Aware Resolution** (lines 573-597):
```go
func (r *Resolver) getRemoteOpts(ctx context.Context,
    platform *rgpb.Platform, credentials Credentials) []remote.Option {
    remoteOpts := []remote.Option{
        remote.WithPlatform(
            gcr.Platform{
                Architecture: platform.GetArch(),      // "amd64", "arm64", etc.
                OS:           platform.GetOs(),        // "linux", "darwin", etc.
                Variant:      platform.GetVariant(),   // "v7" for ARM variants
            },
        ),
    }

    // Add credentials if provided
    if !credentials.IsEmpty() {
        remoteOpts = append(remoteOpts, remote.WithAuth(&authn.Basic{
            Username: credentials.Username,
            Password: credentials.Password,
        }))
    }

    return remoteOpts
}
```

**Multi-Architecture Support:**
- For image indices (multi-arch manifests), the resolver selects the appropriate image for the target platform
- See `imageFromDescriptorAndManifest()` (lines 535-571)

**Registry Mirroring** (lines 63-103):
- Supports rewriting registry URLs to use mirrors
- Configured via `--executor.container_registry_mirrors`
- Falls back to original registry on mirror failure

**Cache Integration:**
- Can read/write manifests and layers from BuildBuddy's remote cache
- Controlled by `--executor.container_registry.use_cache_percent`
- See `fetchImageFromCacheOrRemote()` (lines 430-530)

### 6. Container-Specific Pull Implementation

Each container type implements the `PullImage` method differently:

#### Podman Implementation

**Location:** `containers/podman/podman.go` (lines 543-639)

```go
func (c *podmanCommandContainer) PullImage(ctx context.Context, creds oci.Credentials) error {
    // Synchronize pulls per image name
    ps := getPullStatus(c.image)

    ps.mu.Lock()
    defer ps.mu.Unlock()

    return c.pullImage(ctx, creds)
}

func (c *podmanCommandContainer) pullImage(ctx context.Context, creds oci.Credentials) error {
    podmanArgs := make([]string, 0)

    // Add credentials if provided
    if !creds.IsEmpty() {
        podmanArgs = append(podmanArgs, fmt.Sprintf("--creds=%s", creds.String()))
    }

    podmanArgs = append(podmanArgs, c.image)

    // Execute: podman pull --creds=username:password <image>
    pullResult := c.runPodman(pullCtx, "pull", stdio, podmanArgs...)

    return pullResult.Error
}
```

**Pull Behavior:**
- Uses `podman pull` command with `--creds` flag
- 10-minute timeout (configurable via `--executor.podman.pull_timeout`)
- Logs output at configured level (`--executor.podman.pull_log_level`)
- Per-image pull synchronization to avoid duplicates

#### Firecracker Implementation

**Location:** `containers/firecracker/firecracker.go`

For Firecracker VMs, image pulling is handled via the OCI resolver:

```go
func (c *FirecrackerContainer) PullImage(ctx context.Context, creds oci.Credentials) error {
    if c.imageCacheAuth != nil && c.imageCacheAuth.IsAuthorized(token) {
        // Already authenticated
        return nil
    }

    // Authenticate with registry
    return c.resolver.AuthenticateWithRegistry(
        ctx, c.vmConfig.ContainerImage,
        c.runtimePlatform(), creds,
    )
}
```

**Image Extraction:**
- Images are resolved to manifest + layers via OCI resolver
- Layers are downloaded and extracted to the VM filesystem
- Supports both cached and remote layer fetching

#### Docker Implementation

Similar to Podman, uses `docker pull` with credential handling.

#### Bare Runner

No image pulling - executes directly on host.

## Key Components

### container.CommandContainer Interface

**Location:** `container/container.go` (lines 394-466)

All container implementations must satisfy this interface:

```go
type CommandContainer interface {
    // Returns the isolation type of this container
    IsolationType() string

    // IsImageCached returns whether the configured image is cached locally
    IsImageCached(ctx context.Context) (bool, error)

    // PullImage pulls the container image from the remote registry
    PullImage(ctx context.Context, creds oci.Credentials) error

    // Create creates a new container in "ready to execute" state
    Create(ctx context.Context, workingDir string) error

    // Exec runs a command inside the container
    Exec(ctx context.Context, command *repb.Command, stdio *interfaces.Stdio) *interfaces.CommandResult

    // Remove kills processes and removes container resources
    Remove(ctx context.Context) error

    // Stats returns current resource usage
    Stats(ctx context.Context) (*repb.UsageStats, error)
}
```

### container.Init Struct

**Location:** `container/container.go` (lines 95-120)

Initialization parameters passed to container providers:

```go
type Init struct {
    // WorkDir is the working directory for the task
    WorkDir string

    // Task is the execution task
    Task *repb.ScheduledTask

    // Props contains parsed platform properties with:
    // - ContainerImage (image name without "docker://" prefix)
    // - ContainerRegistryUsername
    // - ContainerRegistryPassword
    // - OS, Arch (platform info)
    Props *platform.Properties

    // BlockDevice for build root
    BlockDevice *block_io.Device

    // CgroupParent for resource isolation
    CgroupParent string

    // Publisher for execution progress updates
    Publisher *operation.Publisher
}
```

### platform.Properties Struct

**Location:** `platform/platform.go` (lines 186-307)

Parsed platform properties including:

```go
type Properties struct {
    OS                        string  // "linux", "darwin", "windows"
    Arch                      string  // "amd64", "arm64"
    ContainerImage            string  // Image name (no "docker://" prefix)
    ContainerRegistryUsername string  // Registry username
    ContainerRegistryPassword string  // Registry password
    ContainerRegistryBypass   bool    // Skip registry (admin-only)
    WorkloadIsolationType     string  // "docker", "podman", "firecracker", "oci", "sandbox", "none"

    // ... other properties ...
}
```

### oci.Credentials Struct

**Location:** `util/oci/oci.go` (lines 111-118)

```go
type Credentials struct {
    Username       string
    Password       string
    bypassRegistry bool  // Set by server admins to skip registry auth
}
```

### oci.Resolver

**Location:** `util/oci/oci.go` (lines 254-286)

Handles OCI registry operations:

```go
type Resolver struct {
    env                 environment.Env
    imageTagToDigestLRU *lru.LRU[tagToDigestEntry]  // Cache tag→digest mappings
    allowedPrivateIPs   []*net.IPNet                 // Allowed private IP ranges
    clock               clockwork.Clock
}
```

**Key Methods:**
- `AuthenticateWithRegistry()` - Authenticates credentials with registry (line 292)
- `ResolveImageDigest()` - Resolves image tag to digest (line 320)
- `Resolve()` - Fetches full image manifest and layers (line 361)

### imageCacheAuthenticator

**Location:** `container/container.go` (lines 585-631)

Manages short-lived tokens for accessing cached images:

```go
type imageCacheAuthenticator struct {
    opts             ImageCacheAuthenticatorOpts
    tokenExpireTimes map[interfaces.ImageCacheToken]time.Time  // Token→expiry
}

type ImageCacheToken struct {
    GroupID  string  // Authenticated group ID
    ImageRef string  // Image reference
}
```

**Behavior:**
- Default TTL: 15 minutes (`defaultImageCacheTokenTTL`)
- Tokens grant access to locally cached images without re-authenticating
- Purges expired tokens periodically

## Container Implementations

### Podman

**Location:** `containers/podman/podman.go`

**Image Caching:**
- Uses `podman image exists <image>` to check cache (line 525)
- Caches positive results for 5 minutes (line 104)

**Pulling:**
- Executes `podman pull --creds=<username>:<password> <image>`
- 10-minute timeout
- Per-image pull synchronization

### Firecracker

**Location:** `containers/firecracker/firecracker.go`

**Image Handling:**
- Uses OCI resolver to fetch image manifest + layers
- Extracts layers to VM filesystem
- Supports UFFD (userfaultfd) for lazy loading

### Docker

**Location:** `containers/docker/docker.go`

**Image Handling:**
- Similar to Podman
- Uses Docker CLI/API

### OCI Runtime

**Location:** `containers/ociruntime/ociruntime.go`

**Image Handling:**
- Uses OCI resolver
- Extracts to OCI runtime bundle format

### Bare

**Location:** `containers/bare/bare.go`

**Image Handling:**
- No images - executes directly on host
- Returns error if image is specified

## Flow Summary

```
1. Remote Execution Request
   ├─ Platform Properties: container-image, container-registry-{username,password}, OSFamily, Arch
   │
2. Platform Parsing (platform/platform.go)
   ├─ Extract image name → ContainerImage (strips "docker://", applies defaults)
   ├─ Extract credentials → ContainerRegistryUsername, ContainerRegistryPassword
   └─ Extract platform → OS, Arch
   │
3. Credential Resolution (oci/oci.go)
   ├─ Try platform properties credentials
   ├─ Fallback to executor config registries
   └─ Fallback to default keychain (docker/podman configs)
   │
4. Runner Preparation (runner/runner.go)
   ├─ Call pullCredentials() → oci.Credentials
   └─ Call PullImageIfNecessary()
   │
5. Pull Orchestration (container/container.go)
   ├─ Apply timeout (5 min default)
   ├─ Check image cache authentication token
   ├─ Synchronize per (isolationType, imageRef)
   ├─ Check if image is cached (IsImageCached)
   └─ Call PullImage if needed
   │
6. Container-Specific Pull
   ├─ Podman: podman pull --creds=user:pass image
   ├─ Firecracker: OCI resolver + layer extraction
   ├─ Docker: docker pull with credentials
   └─ OCI: resolve + extract to bundle
   │
7. Registry Operations (oci/oci.go)
   ├─ Construct remote options with platform (arch, os, variant)
   ├─ Add auth if credentials provided
   ├─ Support registry mirroring
   ├─ Handle multi-arch images (select by platform)
   └─ Optional cache integration (manifests + layers)
```

## Important Flags

### Image Pull Configuration
- `--executor.image_pull_timeout` - Timeout for image pulls (default: 5 min)
- `--debug_use_local_images_only` - Only use cached images (dev mode)

### Credential Configuration
- `--executor.container_registries` - Default credentials per registry
- `--executor.container_registry_default_keychain_enabled` - Enable docker/podman configs

### Podman-Specific
- `--executor.podman.pull_timeout` - Podman pull timeout (default: 10 min)
- `--executor.podman.parallel_pulls` - Max parallel layer pulls
- `--executor.podman.pull_log_level` - Log level for pull output

### OCI-Specific
- `--executor.container_registry_mirrors` - Registry mirrors for fallback
- `--executor.container_registry_allowed_private_ips` - Allowed private IP ranges
- `--executor.container_registry.use_cache_percent` - % of pulls to use cache

### Platform Defaults
- `--executor.default_image` - Default image if none specified
- `--executor.default_isolation_type` - Default isolation type

## Security Considerations

1. **Credential Handling:**
   - Credentials are passed in-memory, never written to disk
   - Platform property credentials override all defaults
   - Admin-only bypass flag for testing cached images

2. **Private IP Protection:**
   - Private IPs are blocked by default for registries
   - Configurable via `--executor.container_registry_allowed_private_ips`

3. **Authentication Caching:**
   - Short-lived tokens (15 min) reduce registry load
   - Tokens are group-scoped (multi-tenant safe)
   - Expired tokens are purged automatically

4. **Image Verification:**
   - Supports digest-based image refs for immutability
   - Optional tag→digest resolution caching (15 min)

## Debugging Tips

1. **Enable Podman Pull Logs:**
   ```
   --executor.podman.pull_log_level=debug
   ```

2. **Check Image Cache:**
   - Podman: `podman image exists <image>`
   - Look for cache hit logs in executor output

3. **Verify Credentials:**
   - Check platform property values in execution request
   - Verify `--executor.container_registries` config
   - Test with default keychain (`~/.docker/config.json`)

4. **Trace Pull Flow:**
   - Look for OpenTelemetry spans: `PullImageIfNecessary`, `PullImage`
   - Check for `imageCacheAuthenticator` token hits/misses

5. **Image Pull Timeout:**
   - Default 5 minutes may be too short for large images
   - Increase via `--executor.image_pull_timeout`

## Related Files

- `container/container.go` - Core container interfaces and pull orchestration
- `platform/platform.go` - Platform property parsing and normalization
- `util/oci/oci.go` - OCI registry operations and credential resolution
- `runner/runner.go` - Task runner coordination
- `containers/podman/podman.go` - Podman-specific implementation
- `containers/firecracker/firecracker.go` - Firecracker-specific implementation
- `containers/docker/docker.go` - Docker-specific implementation
- `containers/ociruntime/ociruntime.go` - Generic OCI runtime implementation
