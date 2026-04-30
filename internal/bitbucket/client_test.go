package bitbucket

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// NewClient / Option tests
// ---------------------------------------------------------------------------

func TestNewClient_WithTokenOverridesEnv(t *testing.T) {
	// Even if the env var is set, WithToken should take precedence.
	t.Setenv("BITBUCKET_TOKEN", "env-token")
	c, err := NewClient(WithToken("override-token"))
	require.NoError(t, err)
	assert.Equal(t, "override-token", c.token)
}

func TestNewClient_EmptyWithTokenStillErrors(t *testing.T) {
	// Explicitly passing an empty token while env is empty should still fail.
	t.Setenv("BITBUCKET_TOKEN", "")
	_, err := NewClient(WithToken(""))
	require.Error(t, err)
	assert.ErrorContains(t, err, "BITBUCKET_TOKEN is required")
}

func TestNewClient_DefaultsRESTBaseAndHTTPClient(t *testing.T) {
	t.Setenv("BITBUCKET_TOKEN", "tok")
	c, err := NewClient()
	require.NoError(t, err)
	assert.Equal(t, defaultRESTBase, c.restBase)
	assert.Same(t, http.DefaultClient, c.httpClient)
}

func TestNewClient_OptionsApplied(t *testing.T) {
	custom := &http.Client{}
	c, err := NewClient(
		WithToken("tok"),
		WithHTTPClient(custom),
		WithRESTBase("https://example.test/api"),
	)
	require.NoError(t, err)
	assert.Same(t, custom, c.httpClient)
	assert.Equal(t, "https://example.test/api", c.restBase)
	assert.Equal(t, "tok", c.token)
}

// ---------------------------------------------------------------------------
// restRequest tests
// ---------------------------------------------------------------------------

// simpleBody is a plain struct used as the decoded response type in tests.
type simpleBody struct {
	Hello string `json:"hello"`
}

func newRawTestClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := NewClient(
		WithToken("test-token"),
		WithHTTPClient(srv.Client()),
		WithRESTBase(srv.URL),
	)
	require.NoError(t, err)
	return c, srv
}

func TestRestRequest_SetsAuthAndAcceptHeaders_NoBody(t *testing.T) {
	t.Parallel()
	var gotAuth, gotAccept, gotContentType string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /thing", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"hello":"world"}`))
	})

	c, _ := newRawTestClient(t, mux)
	var out simpleBody
	err := c.restRequest(t.Context(), http.MethodGet, "/thing", nil, &out)
	require.NoError(t, err)
	assert.Equal(t, "Bearer test-token", gotAuth)
	assert.Equal(t, "application/json", gotAccept)
	// Content-Type must NOT be set when there is no request body.
	assert.Equal(t, "", gotContentType)
	assert.Equal(t, "world", out.Hello)
}

func TestRestRequest_SetsContentTypeWhenBodyPresent(t *testing.T) {
	t.Parallel()
	var gotContentType string
	var gotBody string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /thing", func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	})

	c, _ := newRawTestClient(t, mux)
	err := c.restRequest(t.Context(), http.MethodPost, "/thing", map[string]string{"a": "b"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "application/json", gotContentType)
	assert.JSONEq(t, `{"a":"b"}`, gotBody)
}

func TestRestRequest_SuccessNilResultIgnoresBody(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /thing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"hello":"world"}`))
	})

	c, _ := newRawTestClient(t, mux)
	// result=nil means caller doesn't care about decoding — must not error.
	err := c.restRequest(t.Context(), http.MethodGet, "/thing", nil, nil)
	require.NoError(t, err)
}

func TestRestRequest_SuccessEmptyBodyWithResult(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /thing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		// no body
	})

	c, _ := newRawTestClient(t, mux)
	var out simpleBody
	// Empty response body must NOT be treated as a decode error even if result is non-nil.
	err := c.restRequest(t.Context(), http.MethodGet, "/thing", nil, &out)
	require.NoError(t, err)
	assert.Equal(t, simpleBody{}, out)
}

func TestRestRequest_MarshalErrorSurfaces(t *testing.T) {
	t.Parallel()
	c, err := NewClient(
		WithToken("tok"),
		WithRESTBase("http://unused.invalid"),
	)
	require.NoError(t, err)

	// math.NaN() cannot be marshaled to JSON; triggers the marshal error path.
	err = c.restRequest(context.Background(), http.MethodPost, "/x", math.NaN(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal request")
}

func TestRestRequest_CreateRequestErrorSurfaces(t *testing.T) {
	t.Parallel()
	c, err := NewClient(
		WithToken("tok"),
		WithRESTBase("http://example.test"),
	)
	require.NoError(t, err)

	// Methods containing CTLs are rejected by http.NewRequestWithContext.
	err = c.restRequest(context.Background(), "BAD\nMETHOD", "/x", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create request")
}

// errRoundTripper always returns an error from RoundTrip, exercising the
// "httpClient.Do returns error" branch.
type errRoundTripper struct{ err error }

func (e errRoundTripper) RoundTrip(*http.Request) (*http.Response, error) { return nil, e.err }

func TestRestRequest_HTTPDoErrorSurfaces(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("network down")
	c, err := NewClient(
		WithToken("tok"),
		WithRESTBase("http://example.test"),
		WithHTTPClient(&http.Client{Transport: errRoundTripper{err: sentinel}}),
	)
	require.NoError(t, err)

	err = c.restRequest(context.Background(), http.MethodGet, "/x", nil, nil)
	require.Error(t, err)
	// The error is wrapped with method + path for debuggability.
	assert.Contains(t, err.Error(), "GET /x")
	assert.ErrorIs(t, err, sentinel)
}

// badBodyRoundTripper returns a response whose Body.Read always errors,
// exercising the "io.ReadAll failed" branch.
type badBodyRoundTripper struct{}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }
func (e errReader) Close() error             { return nil }

func (badBodyRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       errReader{err: errors.New("boom")},
		Header:     make(http.Header),
	}, nil
}

func TestRestRequest_ReadBodyErrorSurfaces(t *testing.T) {
	t.Parallel()
	c, err := NewClient(
		WithToken("tok"),
		WithRESTBase("http://example.test"),
		WithHTTPClient(&http.Client{Transport: badBodyRoundTripper{}}),
	)
	require.NoError(t, err)

	err = c.restRequest(context.Background(), http.MethodGet, "/x", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read response")
}

func TestRestRequest_DecodeErrorSurfaces(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /thing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	})

	c, _ := newRawTestClient(t, mux)
	var out simpleBody
	err := c.restRequest(t.Context(), http.MethodGet, "/thing", nil, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode response")
}

func TestRestRequest_NonSuccessReturnsAPIError(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /thing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"error":"nope"}`))
	})

	c, _ := newRawTestClient(t, mux)
	err := c.restRequest(t.Context(), http.MethodPost, "/thing", map[string]string{"a": "b"}, nil)
	require.Error(t, err)

	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.MethodPost, apiErr.Method)
	assert.Equal(t, "/thing", apiErr.Path)
	assert.Equal(t, http.StatusTeapot, apiErr.StatusCode)
	assert.Contains(t, apiErr.Body, `"error":"nope"`)
}

func TestRestRequest_BoundaryStatusCodesAreSuccess(t *testing.T) {
	t.Parallel()
	// The implementation treats [200, 300) as success. Verify the edges:
	// 200 succeeds; 299 succeeds; 300 is an error.
	// (We intentionally do not test the <200 edge because Go's net/http
	// server treats 1xx codes as informational and still writes 200.)
	cases := []struct {
		name       string
		statusCode int
		wantErr    bool
	}{
		{"200 OK", http.StatusOK, false},
		{"299 edge", 299, false},
		{"300 MultipleChoices", http.StatusMultipleChoices, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mux := http.NewServeMux()
			mux.HandleFunc("GET /thing", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.statusCode)
			})
			c, _ := newRawTestClient(t, mux)
			err := c.restRequest(t.Context(), http.MethodGet, "/thing", nil, nil)
			if tc.wantErr {
				require.Error(t, err)
				var apiErr *APIError
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tc.statusCode, apiErr.StatusCode)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRestRequest_ContextCancellationPropagates(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /thing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	c, _ := newRawTestClient(t, mux)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the request is dispatched

	err := c.restRequest(ctx, http.MethodGet, "/thing", nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestRestRequest_URLIsBuiltFromRESTBasePlusPath(t *testing.T) {
	t.Parallel()
	var gotPath, gotHost string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/foo/bar", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHost = r.Host
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, err := NewClient(
		WithToken("tok"),
		WithHTTPClient(srv.Client()),
		// Include a path prefix in the base URL to verify concatenation.
		WithRESTBase(srv.URL+"/v1"),
	)
	require.NoError(t, err)

	err = c.restRequest(t.Context(), http.MethodGet, "/foo/bar", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "/v1/foo/bar", gotPath)
	assert.NotEmpty(t, gotHost)
	// Sanity: the server URL host matches.
	assert.True(t, strings.HasSuffix(srv.URL, gotHost) || strings.Contains(srv.URL, gotHost))
}

// ---------------------------------------------------------------------------
// APIError tests
// ---------------------------------------------------------------------------

func TestAPIError_ErrorMessageFormat(t *testing.T) {
	t.Parallel()
	e := &APIError{
		Method:     http.MethodPost,
		Path:       "/repositories/x/y/pullrequests/42",
		StatusCode: http.StatusForbidden,
		Body:       `{"error":"forbidden"}`,
	}
	msg := e.Error()
	assert.Contains(t, msg, "bitbucket:")
	assert.Contains(t, msg, http.MethodPost)
	assert.Contains(t, msg, "/repositories/x/y/pullrequests/42")
	assert.Contains(t, msg, "403")
	assert.Contains(t, msg, `"error":"forbidden"`)
}

func TestAPIError_ImplementsErrorInterface(t *testing.T) {
	t.Parallel()
	var err error = &APIError{Method: "GET", Path: "/x", StatusCode: 500, Body: ""}
	require.Error(t, err)
	assert.NotEmpty(t, err.Error())
}
