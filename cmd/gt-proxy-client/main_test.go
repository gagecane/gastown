package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ----------------------------------------------------------------------------
// Unit tests: pure functions
// ----------------------------------------------------------------------------

func TestToolNameFromArg0(t *testing.T) {
	cases := []struct {
		name string
		arg0 string
		want string
	}{
		{"bare gt", "gt", "gt"},
		{"bare bd", "bd", "bd"},
		{"absolute gt path", "/usr/local/bin/gt", "gt"},
		{"absolute bd path", "/usr/local/bin/bd", "bd"},
		{"relative path", "./bin/gt", "gt"},
		{"nested path", "/opt/gastown/bin/gt-proxy-client", "gt-proxy-client"},
		{"trailing slash stripped by Base", "/usr/bin/foo/", "foo"},
		{"empty string returns dot (filepath.Base convention)", "", "."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toolNameFromArg0(tc.arg0)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ----------------------------------------------------------------------------
// Integration tests: build the binary once, run it as a subprocess
// ----------------------------------------------------------------------------

// buildClient compiles the gt-proxy-client binary once per test run and returns
// its path. The binary is placed in t.TempDir() so it's cleaned up automatically.
// If the Go toolchain is unavailable (e.g. in some minimal CI images), the test
// using this helper is skipped rather than failed.
func buildClient(t *testing.T) string {
	t.Helper()

	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available; skipping build-based integration test")
	}

	dir := t.TempDir()
	binName := "gt-proxy-client-test"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(dir, binName)

	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}
	return binPath
}

// runWithArgv invokes the client binary with argv[0] set to a specific value
// (so the client can infer the tool name). On unix we use a symlink; on windows
// we fall back to a copy because symlinks require elevated privileges.
func runWithArgv(t *testing.T, binPath, argv0 string, env []string, args ...string) ([]byte, []byte, int) {
	t.Helper()

	linkDir := t.TempDir()
	linkPath := filepath.Join(linkDir, argv0)
	if runtime.GOOS == "windows" {
		linkPath += ".exe"
	}

	if runtime.GOOS == "windows" {
		// Copy the binary on windows so the test works without privileges.
		src, err := os.ReadFile(binPath) //nolint:gosec // test file paths
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(linkPath, src, 0o755)) //nolint:gosec // test binary
	} else {
		require.NoError(t, os.Symlink(binPath, linkPath))
	}

	cmd := exec.Command(linkPath, args...) //nolint:gosec // test binary under t.TempDir()
	cmd.Env = env

	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()

	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			t.Fatalf("running subprocess: %v (stderr: %s)", err, errBuf.String())
		}
	}
	return []byte(outBuf.String()), []byte(errBuf.String()), exitCode
}

// ----------------------------------------------------------------------------
// Test: missing proxy env vars falls through to exec of GT_REAL_BIN
// ----------------------------------------------------------------------------

// TestExecRealFallback verifies that when the proxy env vars are NOT all set,
// the client execs GT_REAL_BIN. We point GT_REAL_BIN at a known binary
// (/bin/echo or the Windows cmd echo equivalent) and check its output.
func TestExecRealFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec fallback test uses unix /bin/echo; skipped on windows")
	}

	binPath := buildClient(t)

	echoPath, err := exec.LookPath("echo")
	require.NoError(t, err, "echo must be on PATH")

	// No GT_PROXY_URL/CERT/KEY/CA → client should exec GT_REAL_BIN (echo).
	// Client passes os.Args to the real binary, so argv[1:] flows through.
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"GT_REAL_BIN=" + echoPath,
		// Explicitly unset proxy vars (a clean env slate).
	}

	stdout, stderr, code := runWithArgv(t, binPath, "gt", env, "hello", "world")
	assert.Equal(t, 0, code, "stderr: %s", stderr)
	assert.Equal(t, "hello world\n", string(stdout))
}

// TestExecRealMissingBinary verifies that when GT_REAL_BIN points at a path that
// does not exist, the client prints a helpful error to stderr and exits with
// status 1 (rather than panicking or exiting 0).
func TestExecRealMissingBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec fallback test uses syscall.Exec semantics; skipped on windows")
	}

	binPath := buildClient(t)

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"GT_REAL_BIN=" + missing,
	}

	_, stderr, code := runWithArgv(t, binPath, "gt", env)
	assert.Equal(t, 1, code)
	assert.Contains(t, string(stderr), "gt-proxy-client: exec")
	assert.Contains(t, string(stderr), missing)
}

// TestExecRealDefaultBinaryMissing verifies that when GT_REAL_BIN is unset and
// the default /usr/local/bin/gt.real does not exist, the error message mentions
// the default path. We only run this when the default does NOT exist on the
// host so we don't accidentally invoke a real gt binary.
func TestExecRealDefaultBinaryMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("default path is unix-specific")
	}
	if _, err := os.Stat("/usr/local/bin/gt.real"); err == nil {
		t.Skip("/usr/local/bin/gt.real exists on this host; skipping default-path test")
	}

	binPath := buildClient(t)
	env := []string{"PATH=" + os.Getenv("PATH")} // GT_REAL_BIN unset

	_, stderr, code := runWithArgv(t, binPath, "gt", env)
	assert.Equal(t, 1, code)
	assert.Contains(t, string(stderr), "/usr/local/bin/gt.real")
}

// ----------------------------------------------------------------------------
// Test: proxy env vars set → client talks to HTTPS server over mTLS
// ----------------------------------------------------------------------------

// testCA is a minimal CA + server/client cert bundle produced in-memory for
// mTLS integration tests. It's independent of the production internal/proxy CA
// so tests don't depend on the real CA implementation.
type testCA struct {
	caCertPEM     []byte
	serverCertPEM []byte
	serverKeyPEM  []byte
	clientCertPEM []byte
	clientKeyPEM  []byte
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()

	// CA
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	caTpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(1 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	// Server cert: SAN includes 127.0.0.1 and localhost so httptest.Server works.
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	serverTpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "test-server"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(1 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:     []string{"localhost"},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTpl, caCert, &serverKey.PublicKey, caKey)
	require.NoError(t, err)

	// Client cert
	clientKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	clientTpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "test-client"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(1 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTpl, caCert, &clientKey.PublicKey, caKey)
	require.NoError(t, err)

	return &testCA{
		caCertPEM:     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		serverCertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}),
		serverKeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)}),
		clientCertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER}),
		clientKeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(clientKey)}),
	}
}

// writeCerts drops the PEM blobs into files under dir and returns their paths.
func (ca *testCA) writeCerts(t *testing.T, dir string) (caFile, clientCert, clientKey string) {
	t.Helper()
	caFile = filepath.Join(dir, "ca.crt")
	clientCert = filepath.Join(dir, "client.crt")
	clientKey = filepath.Join(dir, "client.key")
	require.NoError(t, os.WriteFile(caFile, ca.caCertPEM, 0o644))     //nolint:gosec
	require.NoError(t, os.WriteFile(clientCert, ca.clientCertPEM, 0o644)) //nolint:gosec
	require.NoError(t, os.WriteFile(clientKey, ca.clientKeyPEM, 0o600))
	return caFile, clientCert, clientKey
}

// newTLSServer returns an httptest.Server configured with the testCA's server
// cert and requiring client certificates signed by the testCA.
func newTLSServer(t *testing.T, ca *testCA, handler http.Handler) *httptest.Server {
	t.Helper()

	srvCert, err := tls.X509KeyPair(ca.serverCertPEM, ca.serverKeyPEM)
	require.NoError(t, err)

	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(ca.caCertPEM))

	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{srvCert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// TestProxyExecSuccess spins up an in-process mTLS server that answers /v1/exec
// with a known response, points the client binary at it via env vars, and
// verifies that the client forwards argv correctly and proxies stdout/stderr/exit.
func TestProxyExecSuccess(t *testing.T) {
	binPath := buildClient(t)
	ca := newTestCA(t)

	var receivedArgv []string
	handler := http.NewServeMux()
	handler.HandleFunc("/v1/exec", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var req execRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		receivedArgv = req.Argv

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(execResponse{
			Stdout:   "proxy-stdout\n",
			Stderr:   "proxy-stderr\n",
			ExitCode: 42,
		})
	})

	srv := newTLSServer(t, ca, handler)

	certDir := t.TempDir()
	caFile, certFile, keyFile := ca.writeCerts(t, certDir)

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"GT_PROXY_URL=" + srv.URL,
		"GT_PROXY_CERT=" + certFile,
		"GT_PROXY_KEY=" + keyFile,
		"GT_PROXY_CA=" + caFile,
	}

	stdout, stderr, code := runWithArgv(t, binPath, "bd", env, "show", "abc-123")
	assert.Equal(t, 42, code)
	assert.Equal(t, "proxy-stdout\n", string(stdout))
	assert.Equal(t, "proxy-stderr\n", string(stderr))
	assert.Equal(t, []string{"bd", "show", "abc-123"}, receivedArgv,
		"client must rewrite argv[0] to the tool name derived from the binary")
}

// TestProxyExecServerError verifies that non-200 responses surface as exit 1
// with the server body echoed to stderr.
func TestProxyExecServerError(t *testing.T) {
	binPath := buildClient(t)
	ca := newTestCA(t)

	handler := http.NewServeMux()
	handler.HandleFunc("/v1/exec", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("subcommand not allowed"))
	})

	srv := newTLSServer(t, ca, handler)

	certDir := t.TempDir()
	caFile, certFile, keyFile := ca.writeCerts(t, certDir)

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"GT_PROXY_URL=" + srv.URL,
		"GT_PROXY_CERT=" + certFile,
		"GT_PROXY_KEY=" + keyFile,
		"GT_PROXY_CA=" + caFile,
	}

	_, stderr, code := runWithArgv(t, binPath, "gt", env, "nuke")
	assert.Equal(t, 1, code)
	assert.Contains(t, string(stderr), "server error 403")
	assert.Contains(t, string(stderr), "subcommand not allowed")
}

// TestProxyExecBadCertFile verifies that a missing / unreadable client cert
// results in a clear stderr message and exit 1, not a panic.
func TestProxyExecBadCertFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses syscall.Exec semantics; skipped on windows")
	}
	binPath := buildClient(t)

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"GT_PROXY_URL=https://127.0.0.1:1",
		"GT_PROXY_CERT=/nonexistent/client.crt",
		"GT_PROXY_KEY=/nonexistent/client.key",
		"GT_PROXY_CA=/nonexistent/ca.crt",
	}

	_, stderr, code := runWithArgv(t, binPath, "gt", env, "hook")
	assert.Equal(t, 1, code)
	assert.Contains(t, string(stderr), "load client cert")
}

// TestProxyExecBadCAFile verifies that a malformed CA PEM is rejected with a
// clear error.
func TestProxyExecBadCAFile(t *testing.T) {
	binPath := buildClient(t)
	ca := newTestCA(t)

	certDir := t.TempDir()
	_, certFile, keyFile := ca.writeCerts(t, certDir)

	// Write a non-PEM blob to a file and use it as the CA.
	badCA := filepath.Join(certDir, "bad-ca.crt")
	require.NoError(t, os.WriteFile(badCA, []byte("this is not pem"), 0o644)) //nolint:gosec

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"GT_PROXY_URL=https://127.0.0.1:1",
		"GT_PROXY_CERT=" + certFile,
		"GT_PROXY_KEY=" + keyFile,
		"GT_PROXY_CA=" + badCA,
	}

	_, stderr, code := runWithArgv(t, binPath, "gt", env, "hook")
	assert.Equal(t, 1, code)
	assert.Contains(t, string(stderr), "invalid CA PEM")
}

// TestProxyExecConnectionRefused verifies that a connection failure to the
// proxy URL surfaces cleanly.
func TestProxyExecConnectionRefused(t *testing.T) {
	binPath := buildClient(t)
	ca := newTestCA(t)

	certDir := t.TempDir()
	caFile, certFile, keyFile := ca.writeCerts(t, certDir)

	// Bind and immediately close a listener to get a definitely-closed port.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"GT_PROXY_URL=https://" + addr,
		"GT_PROXY_CERT=" + certFile,
		"GT_PROXY_KEY=" + keyFile,
		"GT_PROXY_CA=" + caFile,
	}

	_, stderr, code := runWithArgv(t, binPath, "gt", env, "hook")
	assert.Equal(t, 1, code)
	assert.Contains(t, string(stderr), "proxy request failed")
}
