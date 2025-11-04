package oci

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"sync"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/remote_execution/platform"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/util/ocicache"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/util/ocimanifest"
	"github.com/buildbuddy-io/buildbuddy/server/environment"
	"github.com/buildbuddy-io/buildbuddy/server/http/httpclient"
	"github.com/buildbuddy-io/buildbuddy/server/util/authutil"
	"github.com/buildbuddy-io/buildbuddy/server/util/claims"
	"github.com/buildbuddy-io/buildbuddy/server/util/flag"
	"github.com/buildbuddy-io/buildbuddy/server/util/log"
	"github.com/buildbuddy-io/buildbuddy/server/util/lru"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/buildbuddy-io/buildbuddy/server/util/tracing"
	"github.com/distribution/reference"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/jonboulle/clockwork"

	rgpb "github.com/buildbuddy-io/buildbuddy/proto/registry"
	repb "github.com/buildbuddy-io/buildbuddy/proto/remote_execution"
	gcrname "github.com/google/go-containerregistry/pkg/name"
	gcr "github.com/google/go-containerregistry/pkg/v1"
	bspb "google.golang.org/genproto/googleapis/bytestream"
)

const (
	// resolveImageDigestLRUMaxEntries limits the number of entries in the image-tag-to-digest cache.
	resolveImageDigestLRUMaxEntries = 1000
	resolveImageDigestLRUDuration   = 15 * time.Minute
)

var (
	registries             = flag.Slice("executor.container_registries", []Registry{}, "")
	mirrors                = flag.Slice("executor.container_registry_mirrors", []MirrorConfig{}, "")
	defaultKeychainEnabled = flag.Bool("executor.container_registry_default_keychain_enabled", false, "Enable the default container registry keychain, respecting both docker configs and podman configs.")
	allowedPrivateIPs      = flag.Slice("executor.container_registry_allowed_private_ips", []string{}, "Allowed private IP ranges for container registries. Private IPs are disallowed by default.")

	useCachePercent = flag.Int("executor.container_registry.use_cache_percent", 0, "Percentage of image pulls that should use the BuildBuddy remote cache for manifests and layers.")
	// TODO: remove from configs and delete
	_ = flag.Bool("executor.container_registry.write_manifests_to_cache", false, "", flag.Internal)
	_ = flag.Bool("executor.container_registry.read_manifests_from_cache", false, "", flag.Internal)
	_ = flag.Bool("executor.container_registry.write_layers_to_cache", false, "", flag.Internal)
	_ = flag.Bool("executor.container_registry.read_layers_from_cache", false, "", flag.Internal)
)

type MirrorConfig struct {
	OriginalURL string `yaml:"original_url" json:"original_url"`
	MirrorURL   string `yaml:"mirror_url" json:"mirror_url"`
}

func (mc MirrorConfig) matches(u *url.URL) (bool, error) {
	originalURL, err := url.Parse(mc.OriginalURL)
	if err != nil {
		return false, err
	}
	match := originalURL.Host == u.Host
	return match, nil
}

func (mc MirrorConfig) rewriteRequest(originalRequest *http.Request) (*http.Request, error) {
	mirrorURL, err := url.Parse(mc.MirrorURL)
	if err != nil {
		return nil, err
	}
	originalURL := originalRequest.URL.String()
	req := originalRequest.Clone(originalRequest.Context())
	req.URL.Scheme = mirrorURL.Scheme
	req.URL.Host = mirrorURL.Host
	//Set X-Forwarded-Host so the mirror knows which remote registry to make requests to.
	//ociregistry looks for this header and will default to forwarding requests to Docker Hub if not found.
	req.Header.Set("X-Forwarded-Host", originalRequest.URL.Host)
	log.Debugf("%q rewritten to %s", originalURL, req.URL.String())
	return req, nil
}

func (mc MirrorConfig) rewriteFallbackRequest(originalRequest *http.Request) (*http.Request, error) {
	originalURL, err := url.Parse(mc.OriginalURL)
	if err != nil {
		return nil, err
	}
	req := originalRequest.Clone(originalRequest.Context())
	req.URL.Scheme = originalURL.Scheme
	req.URL.Host = originalURL.Host
	log.Debugf("(fallback) %q rewritten to %s", originalURL, req.URL.String())
	return req, nil
}

type Registry struct {
	Hostnames []string `yaml:"hostnames" json:"hostnames"`
	Username  string   `yaml:"username" json:"username"`
	Password  string   `yaml:"password" json:"password" config:"secret"`
}

type Credentials struct {
	Username string
	Password string

	// Set if registry auth should be bypassed (can only be set by server
	// admins).
	bypassRegistry bool
}

func CredentialsFromProto(creds *rgpb.Credentials) (Credentials, error) {
	return credentials(creds.GetUsername(), creds.GetPassword())
}

// Extracts the container registry Credentials from the provided platform
// properties, falling back to credentials specified in
// --executor.container_registries if the platform properties credentials are
// absent, then falling back to the default keychain (docker/podman config JSON)
func CredentialsFromProperties(props *platform.Properties) (Credentials, error) {
	imageRef := props.ContainerImage
	if imageRef == "" {
		return Credentials{}, nil
	}

	// Server admins can bypass registry auth (this platform property is guarded
	// by an authorization check in the execution server).
	if props.ContainerRegistryBypass {
		return Credentials{
			bypassRegistry: true,
			// Still forward the username and password - there might be some
			// cases where we actually do have credentials (e.g. our own private
			// images) but still want to bypass the registry if the image is
			// cached.
			Username: props.ContainerRegistryUsername,
			Password: props.ContainerRegistryPassword,
		}, nil
	}

	creds, err := credentials(props.ContainerRegistryUsername, props.ContainerRegistryPassword)
	if err != nil {
		return Credentials{}, fmt.Errorf("Received invalid container-registry-username / container-registry-password combination: %w", err)
	} else if !creds.IsEmpty() {
		return creds, nil
	}

	// If no credentials were provided, fallback to any specified by
	// --executor.container_registries.
	ref, err := reference.ParseNormalizedNamed(imageRef)
	if err != nil {
		log.Debugf("Failed to parse image ref %q: %s", imageRef, err)
		return Credentials{}, nil
	}
	refHostname := reference.Domain(ref)
	for _, cfg := range *registries {
		for _, cfgHostname := range cfg.Hostnames {
			if refHostname == cfgHostname {
				return Credentials{
					Username: cfg.Username,
					Password: cfg.Password,
				}, nil
			}
		}
	}

	// No matching registries were found in the executor config. Fall back to
	// the default keychain.
	if *defaultKeychainEnabled {
		return resolveWithDefaultKeychain(ref)
	}

	return Credentials{}, nil
}

// Reads the auth configuration from a set of commonly supported config file
// locations such as ~/.docker/config.json or
// $XDG_RUNTIME_DIR/containers/auth.json, and returns any configured
// credentials, possibly by invoking a credential helper if applicable.
func resolveWithDefaultKeychain(ref reference.Named) (Credentials, error) {
	// TODO: parse the errors below and if they're 403/401 errors then return
	// Unauthenticated/PermissionDenied
	ctrRef, err := gcrname.ParseReference(ref.String())
	if err != nil {
		log.Debugf("Failed to parse image ref %q: %s", ref.String(), err)
		return Credentials{}, nil
	}
	authenticator, err := authn.DefaultKeychain.Resolve(ctrRef.Context())
	if err != nil {
		return Credentials{}, status.UnavailableErrorf("resolve default keychain: %s", err)
	}
	authConfig, err := authenticator.Authorization()
	if err != nil {
		return Credentials{}, status.UnavailableErrorf("authorize via default keychain: %s", err)
	}
	if authConfig == nil {
		return Credentials{}, nil
	}
	return Credentials{
		Username: authConfig.Username,
		Password: authConfig.Password,
	}, nil
}

func credentials(username, password string) (Credentials, error) {
	if username == "" && password != "" {
		return Credentials{}, status.InvalidArgumentError(
			"malformed credentials: password present with no username")
	} else if username != "" && password == "" {
		return Credentials{}, status.InvalidArgumentError(
			"malformed credentials: username present with no password")
	} else {
		return Credentials{
			Username: username,
			Password: password,
		}, nil
	}
}

func (c Credentials) ToProto() *rgpb.Credentials {
	return &rgpb.Credentials{
		Username: c.Username,
		Password: c.Password,
	}
}

func (c Credentials) IsEmpty() bool {
	return c == Credentials{}
}

func (c Credentials) String() string {
	if c.IsEmpty() {
		return ""
	}
	return c.Username + ":" + c.Password
}

func (c Credentials) Equals(o Credentials) bool {
	return c.Username == o.Username && c.Password == o.Password
}

type tagToDigestEntry struct {
	nameWithDigest string
	expiration     time.Time
}

type Resolver struct {
	env environment.Env

	allowedPrivateIPs []*net.IPNet

	mu                  sync.Mutex
	imageTagToDigestLRU *lru.LRU[tagToDigestEntry]
	clock               clockwork.Clock
}

func NewResolver(env environment.Env) (*Resolver, error) {
	allowedPrivateIPNets := make([]*net.IPNet, 0, len(*allowedPrivateIPs))
	for _, r := range *allowedPrivateIPs {
		_, ipNet, err := net.ParseCIDR(r)
		if err != nil {
			return nil, status.InvalidArgumentErrorf("invald value %q for executor.container_registry_allowed_private_ips flag: %s", r, err)
		}
		allowedPrivateIPNets = append(allowedPrivateIPNets, ipNet)
	}
	imageTagToDigestLRU, err := lru.NewLRU[tagToDigestEntry](&lru.Config[tagToDigestEntry]{
		SizeFn:  func(_ tagToDigestEntry) int64 { return 1 },
		MaxSize: int64(resolveImageDigestLRUMaxEntries),
	})
	if err != nil {
		return nil, err
	}
	return &Resolver{
		env:                 env,
		imageTagToDigestLRU: imageTagToDigestLRU,
		allowedPrivateIPs:   allowedPrivateIPNets,
		clock:               env.GetClock(),
	}, nil
}

// AuthenticateWithRegistry makes a HEAD request to a remote registry with the input credentials.
// Any errors encountered are returned.
// Otherwise, the function returns nil and it is safe to assume the input credentials grant access
// to the image.
func (r *Resolver) AuthenticateWithRegistry(ctx context.Context, imageName string, platform *rgpb.Platform, credentials Credentials) error {
	if credentials.bypassRegistry {
		return nil
	}

	imageRef, err := gcrname.ParseReference(imageName)
	if err != nil {
		return status.InvalidArgumentErrorf("invalid image %q", imageName)
	}
	log.CtxInfof(ctx, "Authenticating with registry %q", imageRef.Context().RegistryStr())

	client := r.getRegistryClient(credentials)
	_, err = client.headManifest(ctx, imageRef)
	if err != nil {
		return err
	}
	return nil
}

// ResolveImageDigest takes an image name and returns an image name with a digest.
// If the input image name includes a digest, a canonicalized version of the name is returned.
// If the input image name refers to a tag (either explictly or implicity), ResolveImageDigest
// will make a HEAD request to the remote registry.
// ResolveImageDigest keeps an LRU cache that maps between canonical image names with tags
// to image names with digests, to reduce the number of HEAD requests.
func (r *Resolver) ResolveImageDigest(ctx context.Context, imageName string, platform *rgpb.Platform, credentials Credentials) (string, error) {
	if imageRefWithDigest, err := gcrname.NewDigest(imageName); err == nil {
		return imageRefWithDigest.String(), nil
	}
	tagRef, err := gcrname.ParseReference(imageName)
	if err != nil {
		return "", status.InvalidArgumentErrorf("invalid image name %q", imageName)
	}

	r.mu.Lock()
	entry, ok := r.imageTagToDigestLRU.Get(tagRef.String())
	r.mu.Unlock()
	if ok {
		if entry.expiration.After(r.clock.Now()) {
			return entry.nameWithDigest, nil
		}
		// Expired; evict and refresh via remote.Head below.
		r.mu.Lock()
		r.imageTagToDigestLRU.Remove(tagRef.String())
		r.mu.Unlock()
	}

	client := r.getRegistryClient(credentials)
	desc, err := client.headManifest(ctx, tagRef)
	if err != nil {
		return "", err
	}
	imageNameWithDigest := tagRef.Context().Digest(desc.Digest.String()).String()
	entryToAdd := tagToDigestEntry{
		nameWithDigest: imageNameWithDigest,
		expiration:     r.clock.Now().Add(resolveImageDigestLRUDuration),
	}
	r.mu.Lock()
	r.imageTagToDigestLRU.Add(tagRef.String(), entryToAdd)
	r.mu.Unlock()
	return imageNameWithDigest, nil
}

func (r *Resolver) Resolve(ctx context.Context, imageName string, platform *rgpb.Platform, credentials Credentials) (gcr.Image, error) {
	ctx, span := tracing.StartSpan(ctx)
	defer span.End()

	imageRef, err := gcrname.ParseReference(imageName)
	if err != nil {
		return nil, status.InvalidArgumentErrorf("invalid image %q", imageName)
	}
	log.CtxInfof(ctx, "Resolving image %q", imageRef)

	client := r.getRegistryClient(credentials)
	puller := newHTTPPuller(ctx, client)

	useCache := false
	if *useCachePercent >= 100 {
		useCache = true
	} else if *useCachePercent > 0 && *useCachePercent < 100 {
		useCache = rand.Intn(100) < *useCachePercent
	}

	if useCache {
		return fetchImageFromCacheOrRemote(
			ctx,
			imageRef,
			gcr.Platform{
				Architecture: platform.GetArch(),
				OS:           platform.GetOs(),
				Variant:      platform.GetVariant(),
			},
			r.env.GetActionCacheClient(),
			r.env.GetByteStreamClient(),
			puller,
			credentials.bypassRegistry,
		)
	}

	remoteDesc, err := puller.Get(ctx, imageRef)
	if err != nil {
		return nil, err
	}

	// The manifest was fetched - now we need to parse it and create an Image
	return imageFromDescriptorAndManifest(
		ctx,
		imageRef.Context(),
		remoteDesc.Descriptor,
		remoteDesc.Manifest,
		gcr.Platform{
			Architecture: platform.GetArch(),
			OS:           platform.GetOs(),
			Variant:      platform.GetVariant(),
		},
		r.env.GetActionCacheClient(),
		r.env.GetByteStreamClient(),
		puller,
		credentials.bypassRegistry,
	)
}

// fetchImageFromCacheOrRemote first tries to fetch the manifest for the given image reference from the cache,
// then falls back to fetching from the upstream remote registry.
// If the referenced manifest is actually an image index, fetchImageFromCacheOrRemote will recur at most once
// to fetch a child image matching the given platform.
func fetchImageFromCacheOrRemote(ctx context.Context, digestOrTagRef gcrname.Reference, platform gcr.Platform, acClient repb.ActionCacheClient, bsClient bspb.ByteStreamClient, puller *httpPuller, bypassRegistry bool) (gcr.Image, error) {
	canUseCache := !isAnonymousUser(ctx)
	if !canUseCache {
		log.CtxInfof(ctx, "Anonymous user request, skipping manifest cache for %s", digestOrTagRef)
	}
	if canUseCache {
		var desc *gcr.Descriptor
		digest, hasDigest := getDigest(digestOrTagRef)
		// For now, we cannot bypass the registry for tag references,
		// since cached manifest AC entries need the resolved digest as part of
		// the key. Log a warning in this case.
		if !hasDigest && bypassRegistry {
			log.CtxWarningf(ctx, "Cannot bypass registry for tag reference %q (need to make a registry request to resolve tag to digest)", digestOrTagRef)
		}
		// Make a HEAD request for the manifest. This does two things:
		// - Authenticates with the registry (if not bypassing)
		// - Resolves the tag to a digest (if not already present)
		if !hasDigest || !bypassRegistry {
			var err error
			desc, err = puller.Head(ctx, digestOrTagRef)
			if err != nil {
				if t, ok := err.(*transport.Error); ok && t.StatusCode == http.StatusUnauthorized {
					return nil, status.PermissionDeniedErrorf("cannot access image manifest: %s", err)
				}
				return nil, status.UnavailableErrorf("cannot retrieve manifest metadata from remote: %s", err)
			}
			digest = desc.Digest
		}

		mc, err := ocicache.FetchManifestFromAC(
			ctx,
			acClient,
			digestOrTagRef.Context(),
			digest,
			digestOrTagRef,
		)
		if err != nil && !status.IsNotFoundError(err) {
			log.CtxWarningf(ctx, "Error fetching manifest from cache: %s", err)
		}
		if mc != nil && err == nil {
			// If we skipped fetching the manifest descriptor (because the
			// reference already contained a resolved digest), then build a
			// descriptor from the cached manifest entry. We aren't populating
			// all of the descriptor fields here, but this should still
			// represent a complete manifest descriptor (the implementation of
			// [puller.Head] only sets these fields as well).
			if desc == nil {
				desc = &gcr.Descriptor{
					Digest:    digest,
					Size:      int64(len(mc.GetRaw())),
					MediaType: types.MediaType(mc.GetContentType()),
				}
			}
			return imageFromDescriptorAndManifest(
				ctx,
				digestOrTagRef.Context(),
				*desc,
				mc.GetRaw(),
				platform,
				acClient,
				bsClient,
				puller,
				bypassRegistry,
			)
		}
	}

	remoteDesc, err := puller.Get(ctx, digestOrTagRef)
	if err != nil {
		if t, ok := err.(*transport.Error); ok && t.StatusCode == http.StatusUnauthorized {
			return nil, status.PermissionDeniedErrorf("not authorized to retrieve image manifest: %s", err)
		}
		return nil, status.UnavailableErrorf("could not retrieve manifest from remote: %s", err)
	}

	if canUseCache {
		err := ocicache.WriteManifestToAC(
			ctx,
			remoteDesc.Manifest,
			acClient,
			digestOrTagRef.Context(),
			remoteDesc.Digest,
			string(remoteDesc.MediaType),
			digestOrTagRef,
		)
		if err != nil {
			log.CtxWarningf(ctx, "Could not write manifest to cache: %s", err)
		}
	}
	return imageFromDescriptorAndManifest(
		ctx,
		digestOrTagRef.Context(),
		remoteDesc.Descriptor,
		remoteDesc.Manifest,
		platform,
		acClient,
		bsClient,
		puller,
		bypassRegistry,
	)
}

// imageFromDescriptorAndManifest returns an Image from the given manifest (if the manifest is an image manifest),
// finds a child image matching the given platform (and fetches a manifest for it) if the given manifest is an index,
// and otherwise returns an error.
func imageFromDescriptorAndManifest(ctx context.Context, repo gcrname.Repository, desc gcr.Descriptor, rawManifest []byte, platform gcr.Platform, acClient repb.ActionCacheClient, bsClient bspb.ByteStreamClient, puller *httpPuller, bypassRegistry bool) (gcr.Image, error) {
	if desc.MediaType.IsSchema1() {
		return nil, status.UnknownErrorf("unsupported MediaType %q", desc.MediaType)
	}

	if desc.MediaType.IsIndex() {
		indexManifest, err := gcr.ParseIndexManifest(bytes.NewReader(rawManifest))
		if err != nil {
			return nil, status.UnknownErrorf("error parsing index manifest: %s", err)
		}

		desc, err := ocimanifest.FindFirstImageManifest(*indexManifest, platform)
		if err != nil {
			return nil, status.UnknownErrorf("Could not find child image for platform in index: %s", err)
		}
		ref := repo.Digest(desc.Digest.String())
		return fetchImageFromCacheOrRemote(
			ctx,
			ref,
			platform,
			acClient,
			bsClient,
			puller,
			bypassRegistry,
		)
	}

	return newImageFromRawManifest(
		ctx,
		repo,
		desc,
		rawManifest,
		acClient,
		bsClient,
		puller,
	), nil
}

func (r *Resolver) getRemoteOpts(ctx context.Context, platform *rgpb.Platform, credentials Credentials) []remote.Option {
	remoteOpts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithPlatform(
			gcr.Platform{
				Architecture: platform.GetArch(),
				OS:           platform.GetOs(),
				Variant:      platform.GetVariant(),
			},
		),
	}
	if !credentials.IsEmpty() {
		remoteOpts = append(remoteOpts, remote.WithAuth(&authn.Basic{
			Username: credentials.Username,
			Password: credentials.Password,
		}))
	}

	tr := httpclient.New(r.allowedPrivateIPs, "oci").Transport
	if len(*mirrors) > 0 {
		remoteOpts = append(remoteOpts, remote.WithTransport(newMirrorTransport(tr, *mirrors)))
	} else {
		remoteOpts = append(remoteOpts, remote.WithTransport(tr))
	}
	return remoteOpts
}

func (r *Resolver) getRegistryClient(credentials Credentials) *registryClient {
	tr := httpclient.New(r.allowedPrivateIPs, "oci").Transport
	if len(*mirrors) > 0 {
		tr = newMirrorTransport(tr, *mirrors)
	}
	return newRegistryClient(tr, credentials)
}

// RuntimePlatform returns the platform on which the program is being executed,
// as reported by the go runtime.
func RuntimePlatform() *rgpb.Platform {
	return &rgpb.Platform{
		Arch: runtime.GOARCH,
		Os:   runtime.GOOS,
	}
}

// getDigest returns the digest from the given reference, if it contains one.
// Otherwise, it returns (nil, false).
func getDigest(ref gcrname.Reference) (gcr.Hash, bool) {
	d, ok := ref.(gcrname.Digest)
	if !ok {
		return gcr.Hash{}, false
	}
	hash, err := gcr.NewHash(d.DigestStr())
	if err != nil {
		return gcr.Hash{}, false
	}
	return hash, true
}

// verify that mirrorTransport implements the RoundTripper interface.
var _ http.RoundTripper = (*mirrorTransport)(nil)

type mirrorTransport struct {
	inner   http.RoundTripper
	mirrors []MirrorConfig
}

func newMirrorTransport(inner http.RoundTripper, mirrors []MirrorConfig) http.RoundTripper {
	return &mirrorTransport{
		inner:   inner,
		mirrors: mirrors,
	}
}

func (t *mirrorTransport) RoundTrip(in *http.Request) (out *http.Response, err error) {
	for _, mirror := range t.mirrors {
		if match, err := mirror.matches(in.URL); err == nil && match {
			mirroredRequest, err := mirror.rewriteRequest(in)
			if err != nil {
				log.Errorf("error mirroring request: %s", err)
				continue
			}
			out, err := t.inner.RoundTrip(mirroredRequest)
			if err != nil {
				log.Errorf("mirror err: %s", err)
				continue
			}
			if out.StatusCode < http.StatusOK || out.StatusCode >= 300 {
				fallbackRequest, err := mirror.rewriteFallbackRequest(in)
				if err != nil {
					log.Errorf("error rewriting fallback request: %s", err)
					continue
				}
				return t.inner.RoundTrip(fallbackRequest)
			}
			return out, nil // Return successful mirror response
		}
	}
	return t.inner.RoundTrip(in)
}

func newImageFromRawManifest(ctx context.Context, repo gcrname.Repository, desc gcr.Descriptor, rawManifest []byte, acClient repb.ActionCacheClient, bsClient bspb.ByteStreamClient, puller *httpPuller) *imageFromRawManifest {
	i := &imageFromRawManifest{
		repo:        repo,
		desc:        desc,
		rawManifest: rawManifest,
		ctx:         ctx,
		acClient:    acClient,
		bsClient:    bsClient,
		puller:      puller,
	}
	i.fetchRawConfigOnce = sync.OnceValues(func() ([]byte, error) {
		manifest, err := i.Manifest()
		if err != nil {
			return nil, err
		}
		if manifest.Config.Data != nil {
			return manifest.Config.Data, nil
		}
		layer := newLayerFromDigest(
			i.repo,
			manifest.Config.Digest,
			i,
			i.puller,
			nil,
		)

		rc, err := layer.Uncompressed()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	})
	return i
}

var _ gcr.Image = (*imageFromRawManifest)(nil)

// imageFromRawManifest implements the go-containerregistry Image interface.
// It allows us to construct an Image from a raw manifest from either the cache
// or an upstream remote registry.
// It also allows us to read layers from and write layers to the cache.
type imageFromRawManifest struct {
	repo        gcrname.Repository
	desc        gcr.Descriptor
	rawManifest []byte

	ctx      context.Context
	acClient repb.ActionCacheClient
	bsClient bspb.ByteStreamClient
	puller   *httpPuller

	fetchRawConfigOnce func() ([]byte, error)
}

func (i *imageFromRawManifest) Digest() (gcr.Hash, error) {
	return i.desc.Digest, nil
}

func (i *imageFromRawManifest) RawManifest() ([]byte, error) {
	return i.rawManifest, nil
}

func (i *imageFromRawManifest) Manifest() (*gcr.Manifest, error) {
	rawManifest, err := i.RawManifest()
	if err != nil {
		return nil, err
	}
	manifest, err := gcr.ParseManifest(bytes.NewReader(rawManifest))
	if err != nil {
		return nil, err
	}
	return manifest, nil
}

func (i *imageFromRawManifest) MediaType() (types.MediaType, error) {
	return i.desc.MediaType, nil
}

func (i *imageFromRawManifest) Size() (int64, error) {
	return i.desc.Size, nil
}

// RawConfigFile looks for the raw config file bytes
// in the rawConfigFile field, then in the manifest's Config section,
// then from the upstream registry.
func (i *imageFromRawManifest) RawConfigFile() ([]byte, error) {
	return i.fetchRawConfigOnce()
}

func (i *imageFromRawManifest) ConfigFile() (*gcr.ConfigFile, error) {
	rawConfigFile, err := i.RawConfigFile()
	if err != nil {
		return nil, err
	}
	return gcr.ParseConfigFile(bytes.NewReader(rawConfigFile))
}

func (i *imageFromRawManifest) ConfigName() (gcr.Hash, error) {
	manifest, err := i.Manifest()
	if err != nil {
		return gcr.Hash{}, err
	}
	return manifest.Config.Digest, nil
}

func (i *imageFromRawManifest) Layers() ([]gcr.Layer, error) {
	m, err := i.Manifest()
	if err != nil {
		return nil, err
	}
	layers := make([]gcr.Layer, 0, len(m.Layers))
	for _, layerDesc := range m.Layers {
		layer := newLayerFromDigest(
			i.repo,
			layerDesc.Digest,
			i,
			i.puller,
			&layerDesc,
		)

		layers = append(layers, layer)
	}
	return layers, nil
}

func (i *imageFromRawManifest) LayerByDigest(digest gcr.Hash) (gcr.Layer, error) {
	return newLayerFromDigest(
		i.repo,
		digest,
		i,
		i.puller,
		nil,
	), nil
}

func (i *imageFromRawManifest) LayerByDiffID(diffID gcr.Hash) (gcr.Layer, error) {
	digest, err := partial.DiffIDToBlob(i, diffID)
	if err != nil {
		return nil, err
	}
	return newLayerFromDigest(
		i.repo,
		digest,
		i,
		i.puller,
		nil,
	), nil
}

func newLayerFromDigest(repo gcrname.Repository, digest gcr.Hash, image *imageFromRawManifest, puller *httpPuller, desc *gcr.Descriptor) *layerFromDigest {
	return &layerFromDigest{
		repo:   repo,
		digest: digest,
		image:  image,
		puller: puller,
		desc:   desc,
		createRemoteLayer: sync.OnceValues(func() (gcr.Layer, error) {
			ref := repo.Digest(digest.String())
			return puller.Layer(image.ctx, ref)
		}),
	}
}

var _ gcr.Layer = (*layerFromDigest)(nil)

// layerFromDigest implements the go-containerregistry Layer interface.
// It allows us to read layers from and write layers to the cache.
type layerFromDigest struct {
	repo   gcrname.Repository
	digest gcr.Hash
	image  *imageFromRawManifest

	puller *httpPuller
	desc   *gcr.Descriptor

	createRemoteLayer func() (gcr.Layer, error)
}

func (l *layerFromDigest) Digest() (gcr.Hash, error) {
	return l.digest, nil
}

func (l *layerFromDigest) DiffID() (gcr.Hash, error) {
	return partial.BlobToDiffID(l.image, l.digest)
}

func (l *layerFromDigest) Compressed() (io.ReadCloser, error) {
	canUseCache := !isAnonymousUser(l.image.ctx)
	if !canUseCache {
		log.CtxInfof(l.image.ctx, "Anonymous user request, skipping layer cache for %s:%s", l.image.repo, l.image.desc.Digest)
	}
	if canUseCache {
		rc, err := l.fetchLayerFromCache()
		if err != nil && !status.IsNotFoundError(err) {
			log.CtxWarningf(l.image.ctx, "Error fetching layer from cache: %s", err)
		}
		if rc != nil && err == nil {
			return rc, nil
		}
	}

	remoteLayer, err := l.createRemoteLayer()
	if err != nil {
		return nil, err
	}
	upstream, err := remoteLayer.Compressed()
	if err != nil {
		return nil, err
	}

	if canUseCache {
		mediaType, err := l.MediaType()
		if err != nil {
			log.CtxWarningf(l.image.ctx, "Could not get media type for layer: %s", err)
			return upstream, nil
		}
		contentLength, err := l.Size()
		if err != nil {
			log.CtxWarningf(l.image.ctx, "Could not get size for layer: %s", err)
			return upstream, nil
		}
		rc, err := ocicache.NewBlobReadThroughCacher(
			l.image.ctx,
			upstream,
			l.image.bsClient,
			l.image.acClient,
			l.repo,
			l.digest,
			string(mediaType),
			contentLength,
		)
		if err != nil {
			return upstream, nil
		}
		return rc, nil
	}

	return upstream, nil
}

// Uncompressed fetches the compressed bytes from the upstream server
// and returns a ReadCloser that decompresses as it reads.
func (l *layerFromDigest) Uncompressed() (io.ReadCloser, error) {
	cl, err := partial.CompressedToLayer(l)
	if err != nil {
		return nil, err
	}
	return cl.Uncompressed()
}

func (l *layerFromDigest) Size() (int64, error) {
	if l.desc != nil {
		return l.desc.Size, nil
	}
	remoteLayer, err := l.createRemoteLayer()
	if err != nil {
		return 0, err
	}
	return remoteLayer.Size()
}

func (l *layerFromDigest) MediaType() (types.MediaType, error) {
	if l.desc != nil {
		return l.desc.MediaType, nil
	}
	remoteLayer, err := l.createRemoteLayer()
	if err != nil {
		return "", err
	}
	return remoteLayer.MediaType()
}

func (l *layerFromDigest) fetchLayerFromCache() (io.ReadCloser, error) {
	metadata, err := ocicache.FetchBlobMetadataFromCache(
		l.image.ctx,
		l.image.bsClient,
		l.image.acClient,
		l.repo,
		l.digest,
	)
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		err := ocicache.FetchBlobFromCache(
			l.image.ctx,
			pw,
			l.image.bsClient,
			l.digest,
			metadata.GetContentLength(),
		)
		if err != nil {
			log.Warningf("Error fetching blob from cache: %s", err)
			pw.CloseWithError(err)
		}
	}()
	return pr, nil
}

func isAnonymousUser(ctx context.Context) bool {
	_, err := claims.ClaimsFromContext(ctx)
	return authutil.IsAnonymousUserError(err)
}

// registryClient handles HTTP requests to OCI registries with authentication
type registryClient struct {
	transport   http.RoundTripper
	credentials Credentials
	// Cache for auth tokens per registry
	tokenCache sync.Map // map[string]string (registry -> token)
}

// httpPuller is a replacement for remote.Puller that uses our HTTP client
type httpPuller struct {
	client *registryClient
	ctx    context.Context
}

// remoteDescriptor mirrors the structure returned by remote.Puller.Get()
type remoteDescriptor struct {
	Descriptor gcr.Descriptor
	Manifest   []byte
	MediaType  types.MediaType
	Digest     gcr.Hash
}

func (d *remoteDescriptor) Image() (gcr.Image, error) {
	// This is called when we have a manifest and need to convert it to an Image
	// We'll handle this in the calling code
	return nil, status.UnimplementedError("Image() not implemented on httpPuller descriptor")
}

func newHTTPPuller(ctx context.Context, client *registryClient) *httpPuller {
	return &httpPuller{
		client: client,
		ctx:    ctx,
	}
}

func (p *httpPuller) Get(ctx context.Context, ref gcrname.Reference) (*remoteDescriptor, error) {
	desc, manifest, err := p.client.getManifest(ctx, ref)
	if err != nil {
		return nil, err
	}
	return &remoteDescriptor{
		Descriptor: *desc,
		Manifest:   manifest,
		MediaType:  desc.MediaType,
		Digest:     desc.Digest,
	}, nil
}

func (p *httpPuller) Head(ctx context.Context, ref gcrname.Reference) (*gcr.Descriptor, error) {
	return p.client.headManifest(ctx, ref)
}

func (p *httpPuller) Layer(ctx context.Context, ref gcrname.Reference) (gcr.Layer, error) {
	// Extract digest from reference
	digest, ok := ref.(gcrname.Digest)
	if !ok {
		return nil, status.InvalidArgumentError("Layer() requires a digest reference")
	}
	hash, err := gcr.NewHash(digest.DigestStr())
	if err != nil {
		return nil, err
	}

	// Create a simple layer implementation
	return &httpLayer{
		puller: p,
		repo:   ref.Context(),
		digest: hash,
	}, nil
}

// httpLayer implements gcr.Layer for blobs fetched via HTTP
type httpLayer struct {
	puller *httpPuller
	repo   gcrname.Repository
	digest gcr.Hash
}

func (l *httpLayer) Digest() (gcr.Hash, error) {
	return l.digest, nil
}

func (l *httpLayer) DiffID() (gcr.Hash, error) {
	// DiffID is typically computed from the uncompressed layer
	// For now, we'll return an error since we don't have this info
	return gcr.Hash{}, status.UnimplementedError("DiffID not available for httpLayer")
}

func (l *httpLayer) Compressed() (io.ReadCloser, error) {
	return l.puller.client.getBlob(l.puller.ctx, l.repo, l.digest)
}

func (l *httpLayer) Uncompressed() (io.ReadCloser, error) {
	cl, err := partial.CompressedToLayer(l)
	if err != nil {
		return nil, err
	}
	return cl.Uncompressed()
}

func (l *httpLayer) Size() (int64, error) {
	return 0, status.UnimplementedError("Size not available for httpLayer")
}

func (l *httpLayer) MediaType() (types.MediaType, error) {
	return "", status.UnimplementedError("MediaType not available for httpLayer")
}

func newRegistryClient(transport http.RoundTripper, credentials Credentials) *registryClient {
	return &registryClient{
		transport:   transport,
		credentials: credentials,
	}
}

// authChallenge represents a WWW-Authenticate challenge from a registry
type authChallenge struct {
	scheme string
	realm  string
	service string
	scope  string
}

// parseAuthChallenge parses a WWW-Authenticate header value
func parseAuthChallenge(header string) (*authChallenge, error) {
	// Example: Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/ubuntu:pull"
	parts := bytes.SplitN([]byte(header), []byte(" "), 2)
	if len(parts) != 2 {
		return nil, status.InvalidArgumentErrorf("invalid auth challenge format")
	}

	challenge := &authChallenge{
		scheme: string(parts[0]),
	}

	// Parse key=value pairs
	paramsStr := string(parts[1])
	for _, param := range bytes.Split([]byte(paramsStr), []byte(",")) {
		kv := bytes.SplitN(param, []byte("="), 2)
		if len(kv) != 2 {
			continue
		}
		key := string(bytes.TrimSpace(kv[0]))
		value := string(bytes.Trim(bytes.TrimSpace(kv[1]), `"`))

		switch key {
		case "realm":
			challenge.realm = value
		case "service":
			challenge.service = value
		case "scope":
			challenge.scope = value
		}
	}

	return challenge, nil
}

// getBearerToken fetches a bearer token from the auth service
func (c *registryClient) getBearerToken(ctx context.Context, challenge *authChallenge) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", challenge.realm, nil)
	if err != nil {
		return "", err
	}

	q := req.URL.Query()
	if challenge.service != "" {
		q.Set("service", challenge.service)
	}
	if challenge.scope != "" {
		q.Set("scope", challenge.scope)
	}
	req.URL.RawQuery = q.Encode()

	// Add basic auth if we have credentials
	if !c.credentials.IsEmpty() {
		req.SetBasicAuth(c.credentials.Username, c.credentials.Password)
	}

	client := &http.Client{Transport: c.transport}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", status.UnavailableErrorf("auth request failed with status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	// Parse token from response (usually JSON with "token" field)
	var tokenResp struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", err
	}

	token := tokenResp.Token
	if token == "" {
		token = tokenResp.AccessToken
	}

	return token, nil
}

// doRequest performs an HTTP request with authentication handling
func (c *registryClient) doRequest(ctx context.Context, method, url string, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}

	// Set headers
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// Try with basic auth first if we have credentials
	if !c.credentials.IsEmpty() {
		req.SetBasicAuth(c.credentials.Username, c.credentials.Password)
	}

	client := &http.Client{Transport: c.transport}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	// If unauthorized and we got a challenge, try token auth
	if resp.StatusCode == http.StatusUnauthorized {
		authHeader := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()

		if authHeader == "" {
			return nil, status.PermissionDeniedError("unauthorized and no auth challenge provided")
		}

		challenge, err := parseAuthChallenge(authHeader)
		if err != nil {
			return nil, err
		}

		if challenge.scheme == "Bearer" {
			token, err := c.getBearerToken(ctx, challenge)
			if err != nil {
				return nil, err
			}

			// Retry with token
			req, err = http.NewRequestWithContext(ctx, method, url, nil)
			if err != nil {
				return nil, err
			}
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			req.Header.Set("Authorization", "Bearer "+token)

			return client.Do(req)
		}

		return nil, status.PermissionDeniedError("unsupported auth scheme")
	}

	return resp, nil
}

// headManifest performs a HEAD request for a manifest
func (c *registryClient) headManifest(ctx context.Context, ref gcrname.Reference) (*gcr.Descriptor, error) {
	registry := ref.Context().RegistryStr()
	repo := ref.Context().RepositoryStr()

	var reference string
	if digest, ok := ref.(gcrname.Digest); ok {
		reference = digest.DigestStr()
	} else if tag, ok := ref.(gcrname.Tag); ok {
		reference = tag.TagStr()
	} else {
		reference = "latest"
	}

	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", registry, repo, reference)

	headers := map[string]string{
		"Accept": string(types.OCIManifestSchema1) + "," +
			string(types.DockerManifestSchema2) + "," +
			string(types.OCIImageIndex) + "," +
			string(types.DockerManifestList),
	}

	resp, err := c.doRequest(ctx, "HEAD", url, headers)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, status.UnavailableErrorf("HEAD manifest failed with status %d", resp.StatusCode)
	}

	digestStr := resp.Header.Get("Docker-Content-Digest")
	if digestStr == "" {
		return nil, status.DataLossError("missing Docker-Content-Digest header")
	}

	digest, err := gcr.NewHash(digestStr)
	if err != nil {
		return nil, err
	}

	contentLength := resp.ContentLength
	mediaType := types.MediaType(resp.Header.Get("Content-Type"))

	return &gcr.Descriptor{
		Digest:    digest,
		Size:      contentLength,
		MediaType: mediaType,
	}, nil
}

// getManifest performs a GET request for a manifest
func (c *registryClient) getManifest(ctx context.Context, ref gcrname.Reference) (*gcr.Descriptor, []byte, error) {
	registry := ref.Context().RegistryStr()
	repo := ref.Context().RepositoryStr()

	var reference string
	if digest, ok := ref.(gcrname.Digest); ok {
		reference = digest.DigestStr()
	} else if tag, ok := ref.(gcrname.Tag); ok {
		reference = tag.TagStr()
	} else {
		reference = "latest"
	}

	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", registry, repo, reference)

	headers := map[string]string{
		"Accept": string(types.OCIManifestSchema1) + "," +
			string(types.DockerManifestSchema2) + "," +
			string(types.OCIImageIndex) + "," +
			string(types.DockerManifestList),
	}

	resp, err := c.doRequest(ctx, "GET", url, headers)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, status.UnavailableErrorf("GET manifest failed with status %d", resp.StatusCode)
	}

	digestStr := resp.Header.Get("Docker-Content-Digest")
	if digestStr == "" {
		return nil, nil, status.DataLossError("missing Docker-Content-Digest header")
	}

	digest, err := gcr.NewHash(digestStr)
	if err != nil {
		return nil, nil, err
	}

	contentLength := resp.ContentLength
	mediaType := types.MediaType(resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}

	return &gcr.Descriptor{
		Digest:    digest,
		Size:      contentLength,
		MediaType: mediaType,
	}, body, nil
}

// getBlob performs a GET request for a blob
func (c *registryClient) getBlob(ctx context.Context, repo gcrname.Repository, digest gcr.Hash) (io.ReadCloser, error) {
	registry := repo.RegistryStr()
	repoName := repo.RepositoryStr()

	url := fmt.Sprintf("https://%s/v2/%s/blobs/%s", registry, repoName, digest.String())

	resp, err := c.doRequest(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, status.UnavailableErrorf("GET blob failed with status %d", resp.StatusCode)
	}

	return resp.Body, nil
}
