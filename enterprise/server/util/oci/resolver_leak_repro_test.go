package oci_test

// Independent reproducer (V10) for per-request HTTP transport retention in
// oci.Resolver. Counts client-side persistConn goroutines and socket FDs
// after N Resolver operations against a live registry, after settling.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/buildbuddy-io/buildbuddy/enterprise/server/testutil/testregistry"
	"github.com/buildbuddy-io/buildbuddy/enterprise/server/util/oci"
	"github.com/buildbuddy-io/buildbuddy/server/testutil/testenv"
	"github.com/buildbuddy-io/buildbuddy/server/util/testing/flags"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/require"
)

var tlsCertPEM, tlsKeyPEM []byte

// TestMain generates a self-signed cert for 127.0.0.2 and points
// SSL_CERT_FILE at it before anything loads the system cert pool, so that
// the production httpclient transport (which uses system roots) trusts the
// TLS test registry. 127.0.0.2 is used because go-containerregistry treats
// localhost/127.0.0.1 as plain-HTTP registries.
func TestMain(m *testing.M) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "v10-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.2")},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		panic(err)
	}
	tlsCertPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	tlsKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
	dir, err := os.MkdirTemp("", "v10")
	if err != nil {
		panic(err)
	}
	p := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(p, tlsCertPEM, 0644); err != nil {
		panic(err)
	}
	os.Setenv("SSL_CERT_FILE", p)
	os.Exit(m.Run())
}

type regUnderTest struct {
	// imageAddress(i) returns a distinct tagged image name for index i.
	imageAddress func(i int) string
}

func startPlainRegistry(t *testing.T, n int) regUnderTest {
	reg := testregistry.Run(t, testregistry.Opts{})
	_, img := reg.PushNamedImage(t, "img", nil)
	for i := range n {
		reg.Push(t, img, fmt.Sprintf("img:t%d", i), nil)
	}
	t.Cleanup(func() { reg.Shutdown() })
	return regUnderTest{imageAddress: func(i int) string { return reg.ImageAddress(fmt.Sprintf("img:t%d", i)) }}
}

func startTLSRegistry(t *testing.T, n int) regUnderTest {
	cert, err := tls.X509KeyPair(tlsCertPEM, tlsKeyPEM)
	require.NoError(t, err)
	lis, err := tls.Listen("tcp", "127.0.0.2:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	require.NoError(t, err)
	srv := &http.Server{Handler: registry.New()}
	go srv.Serve(lis)
	t.Cleanup(func() { srv.Close() })
	addr := lis.Addr().String()

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(tlsCertPEM)
	pushTr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
	defer pushTr.CloseIdleConnections()
	img, err := random.Image(64, 1)
	require.NoError(t, err)
	for i := range n {
		ref, err := name.ParseReference(fmt.Sprintf("%s/img:t%d", addr, i))
		require.NoError(t, err)
		require.NoError(t, remote.Write(ref, img, remote.WithTransport(pushTr)))
	}
	return regUnderTest{imageAddress: func(i int) string { return fmt.Sprintf("%s/img:t%d", addr, i) }}
}

type counts struct {
	readLoop, writeLoop, serverConns, sockets int
}

func snapshot() counts {
	buf := make([]byte, 16<<20)
	buf = buf[:runtime.Stack(buf, true)]
	var c counts
	for g := range strings.SplitSeq(string(buf), "\n\n") {
		switch {
		case strings.Contains(g, "net/http.(*persistConn).readLoop("):
			c.readLoop++
		case strings.Contains(g, "net/http.(*persistConn).writeLoop("):
			c.writeLoop++
		case strings.Contains(g, "net/http.(*conn).serve("):
			c.serverConns++
		}
	}
	ents, _ := os.ReadDir("/proc/self/fd")
	for _, e := range ents {
		if l, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name())); err == nil && strings.HasPrefix(l, "socket:") {
			c.sockets++
		}
	}
	return c
}

func (c counts) sub(o counts) counts {
	return counts{c.readLoop - o.readLoop, c.writeLoop - o.writeLoop, c.serverConns - o.serverConns, c.sockets - o.sockets}
}

func TestV10ResolverConnRetention(t *testing.T) {
	ctx := context.Background()
	for _, transport := range []string{"plain", "tls"} {
		for _, op := range []string{"Resolve", "ResolveImageDigest", "AuthenticateWithRegistry"} {
			for _, n := range []int{1, 10, 50} {
				t.Run(fmt.Sprintf("%s/%s/N=%d", transport, op, n), func(t *testing.T) {
					flags.Set(t, "executor.container_registry_allowed_private_ips", []string{"127.0.0.0/8"})
					te := testenv.GetTestEnv(t)
					var reg regUnderTest
					if transport == "plain" {
						reg = startPlainRegistry(t, n)
					} else {
						reg = startTLSRegistry(t, n)
					}
					r, err := oci.NewResolver(te)
					require.NoError(t, err)
					// Let push-side connections settle out of the baseline.
					http.DefaultTransport.(*http.Transport).CloseIdleConnections()
					remote.DefaultTransport.(*http.Transport).CloseIdleConnections()
					time.Sleep(200 * time.Millisecond)
					base := snapshot()
					for i := range n {
						img := reg.imageAddress(i)
						switch op {
						case "Resolve":
							_, err = r.Resolve(ctx, img, oci.RuntimePlatform(), oci.Credentials{}, false)
						case "ResolveImageDigest":
							_, err = r.ResolveImageDigest(ctx, img, oci.RuntimePlatform(), oci.Credentials{})
						case "AuthenticateWithRegistry":
							err = r.AuthenticateWithRegistry(ctx, img, oci.RuntimePlatform(), oci.Credentials{})
						}
						require.NoError(t, err)
					}
					// Settle: poll up to 3s for counts to drop.
					var d counts
					deadline := time.Now().Add(3 * time.Second)
					for {
						d = snapshot().sub(base)
						if d.readLoop <= 1 || time.Now().After(deadline) {
							break
						}
						time.Sleep(50 * time.Millisecond)
					}
					t.Logf("V10RESULT transport=%s op=%s N=%d readLoop=+%d writeLoop=+%d serverConns=+%d sockets=+%d",
						transport, op, n, d.readLoop, d.writeLoop, d.serverConns, d.sockets)
					if n >= 10 {
						require.LessOrEqual(t, d.readLoop, 2, "client persistConn readLoop goroutines grew with N")
					}
				})
			}
		}
	}
}
