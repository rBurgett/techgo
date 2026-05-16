package awss3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rBurgett/techgo/internal/awssig"
)

// testCreds reuses the public AWS test key pair so any signed request lands
// with a deterministic Authorization header (we don't assert the signature
// value here — Phase 7's vector tests already cover that — but pinning the
// creds keeps test failures legible).
var testCreds = awssig.Credentials{
	AccessKeyID:     "AKIDEXAMPLE",
	SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
}

// fixedTime pins the signer's clock for stable canonical requests in tests.
func fixedTime() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) }

// testBucket is the bucket every test client targets. With an Endpoint
// override the client uses path-style addressing, so every request path is
// prefixed with "/" + testBucket — wirePath() builds the expectation.
const testBucket = "techgo-site"

// wirePath returns the path-style request path the client produces for a
// given object key (or "" for the list endpoint): /<bucket>[/<key>].
func wirePath(key string) string {
	if key == "" {
		return "/" + testBucket
	}
	return "/" + testBucket + "/" + key
}

// newTestServer spins up an httptest.Server with the given handler and
// returns a Client targeting it. Callers handle the server's Close in
// their own t.Cleanup if they need access to the server itself.
func newTestServer(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	c := &Client{
		Bucket:   testBucket,
		Region:   "us-east-1",
		Creds:    testCreds,
		HTTP:     ts.Client(),
		Endpoint: ts.URL,
		Now:      fixedTime,
	}
	return c, ts
}

func TestPutObjectShape(t *testing.T) {
	var got *http.Request
	var gotBody []byte
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	})

	body := []byte("<html>hi</html>")
	err := c.PutObject(context.Background(), "index.html", body,
		"text/html; charset=utf-8", "public, max-age=0, must-revalidate")
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	if got.Method != http.MethodPut {
		t.Errorf("method = %q, want PUT", got.Method)
	}
	if got.URL.Path != wirePath("index.html") {
		t.Errorf("path = %q, want %q", got.URL.Path, wirePath("index.html"))
	}
	if got.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got.Header.Get("Content-Type"))
	}
	if got.Header.Get("Cache-Control") != "public, max-age=0, must-revalidate" {
		t.Errorf("Cache-Control = %q", got.Header.Get("Cache-Control"))
	}
	if got.ContentLength != int64(len(body)) {
		t.Errorf("Content-Length = %d, want %d", got.ContentLength, len(body))
	}
	if got.Header.Get("X-Amz-Date") == "" {
		t.Error("X-Amz-Date not set (signer didn't run?)")
	}
	if got.Header.Get("X-Amz-Content-Sha256") == "" {
		t.Error("X-Amz-Content-Sha256 not set — required for S3")
	}
	if !strings.HasPrefix(got.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/") {
		t.Errorf("Authorization = %q, want AWS4-HMAC-SHA256 with our test key", got.Header.Get("Authorization"))
	}
	if string(gotBody) != string(body) {
		t.Errorf("body = %q, want %q", gotBody, body)
	}
}

func TestPutObjectAppliesPrefix(t *testing.T) {
	var gotPath string
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	})
	c.Prefix = "site"

	if err := c.PutObject(context.Background(), "css/style.css", []byte("body{}"), "text/css", ""); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if want := wirePath("site/css/style.css"); gotPath != want {
		t.Errorf("path = %q, want %q (prefix applied)", gotPath, want)
	}
}

func TestPutObjectNormalizesPrefix(t *testing.T) {
	cases := map[string]string{
		"":         wirePath("key"),
		"site":     wirePath("site/key"),
		"site/":    wirePath("site/key"),
		"/site":    wirePath("site/key"),
		"/site/":   wirePath("site/key"),
		"a/b":      wirePath("a/b/key"),
		"//a//b//": wirePath("a/b/key"),
	}
	for prefix, wantPath := range cases {
		var gotPath string
		c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.WriteHeader(http.StatusOK)
		})
		c.Prefix = prefix
		if err := c.PutObject(context.Background(), "key", nil, "", ""); err != nil {
			t.Fatalf("Prefix=%q: %v", prefix, err)
		}
		if gotPath != wantPath {
			t.Errorf("Prefix=%q: path = %q, want %q", prefix, gotPath, wantPath)
		}
	}
}

func TestPutObjectNon2xxIncludesAWSErrorBody(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code><Message>blocked</Message></Error>`))
	})
	err := c.PutObject(context.Background(), "x", nil, "", "")
	if err == nil {
		t.Fatal("PutObject returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "AccessDenied") || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("error = %v, want it to surface the AWS XML body (AccessDenied / blocked)", err)
	}
}

func TestGetObjectFoundAndNotFound(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == wirePath("exists") {
			_, _ = w.Write([]byte("hello"))
			return
		}
		http.NotFound(w, r)
	})

	body, found, err := c.GetObject(context.Background(), "exists")
	if err != nil {
		t.Fatalf("GetObject(exists): %v", err)
	}
	if !found || string(body) != "hello" {
		t.Errorf("GetObject(exists) = (%q, found=%v), want (hello, true)", body, found)
	}

	body, found, err = c.GetObject(context.Background(), "missing")
	if err != nil {
		t.Fatalf("GetObject(missing) should not error on 404: %v", err)
	}
	if found || body != nil {
		t.Errorf("GetObject(missing) = (%q, %v), want (nil, false)", body, found)
	}
}

func TestDeleteObjectAcceptsBoth204And200(t *testing.T) {
	status := http.StatusNoContent
	var sawMethod, sawPath string
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		sawMethod = r.Method
		sawPath = r.URL.Path
		w.WriteHeader(status)
	})

	if err := c.DeleteObject(context.Background(), "obsolete.html"); err != nil {
		t.Fatalf("DeleteObject (204): %v", err)
	}
	if sawMethod != http.MethodDelete || sawPath != wirePath("obsolete.html") {
		t.Errorf("got %s %s, want DELETE %s", sawMethod, sawPath, wirePath("obsolete.html"))
	}

	status = http.StatusOK
	if err := c.DeleteObject(context.Background(), "also.html"); err != nil {
		t.Errorf("DeleteObject should accept 200 too: %v", err)
	}

	status = http.StatusInternalServerError
	if err := c.DeleteObject(context.Background(), "boom.html"); err == nil {
		t.Error("DeleteObject (500) returned nil, want an error")
	}
}

func TestListObjectsPagination(t *testing.T) {
	// Two-page response: the first carries IsTruncated=true plus a token,
	// the second resolves with IsTruncated=false. Verify the client follows
	// the chain and returns the union (prefix-stripped).
	var calls int
	var seenTokens []string
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wirePath("") {
			t.Errorf("list path = %q, want %q", r.URL.Path, wirePath(""))
		}
		q := r.URL.Query()
		if q.Get("list-type") != "2" {
			t.Errorf("missing list-type=2: %s", r.URL.RawQuery)
		}
		if q.Get("prefix") != "site/" {
			t.Errorf("prefix = %q, want site/", q.Get("prefix"))
		}
		seenTokens = append(seenTokens, q.Get("continuation-token"))
		calls++
		w.Header().Set("Content-Type", "application/xml")
		if calls == 1 {
			_, _ = io.WriteString(w, `<?xml version="1.0"?>
<ListBucketResult>
  <Name>techgo-site</Name>
  <Prefix>site/</Prefix>
  <IsTruncated>true</IsTruncated>
  <NextContinuationToken>tok-2</NextContinuationToken>
  <Contents><Key>site/index.html</Key></Contents>
  <Contents><Key>site/css/style.css</Key></Contents>
</ListBucketResult>`)
			return
		}
		_, _ = io.WriteString(w, `<?xml version="1.0"?>
<ListBucketResult>
  <Name>techgo-site</Name>
  <Prefix>site/</Prefix>
  <IsTruncated>false</IsTruncated>
  <Contents><Key>site/media/0001.mp3</Key></Contents>
</ListBucketResult>`)
	})
	c.Prefix = "site"

	keys, err := c.ListObjects(context.Background())
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	wantKeys := []string{"index.html", "css/style.css", "media/0001.mp3"}
	if fmt.Sprintf("%v", keys) != fmt.Sprintf("%v", wantKeys) {
		t.Errorf("keys = %v, want %v (prefix stripped, document order)", keys, wantKeys)
	}
	if calls != 2 {
		t.Errorf("server saw %d calls, want 2 (paginated)", calls)
	}
	if len(seenTokens) != 2 || seenTokens[0] != "" || seenTokens[1] != "tok-2" {
		t.Errorf("continuation tokens = %v, want [\"\", \"tok-2\"]", seenTokens)
	}
}

func TestListObjectsEmptyResult(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<?xml version="1.0"?>
<ListBucketResult><Name>techgo-site</Name><IsTruncated>false</IsTruncated></ListBucketResult>`)
	})
	keys, err := c.ListObjects(context.Background())
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected zero keys, got %v", keys)
	}
}

func TestEndpointDerivedFromBucketRegion(t *testing.T) {
	c := &Client{Bucket: "techgo-site", Region: "us-east-1"}
	if got := c.endpoint(); got != "https://techgo-site.s3.us-east-1.amazonaws.com" {
		t.Errorf("default endpoint = %q", got)
	}
	c.Endpoint = "https://localstack:4566/"
	if got := c.endpoint(); got != "https://localstack:4566" {
		t.Errorf("endpoint override = %q, want trailing slash trimmed", got)
	}
}

// TestListObjectsBuildsQueryWithAWSEscape ensures the prefix and continuation
// token go through awssig.URIEscape (so a literal '+' or '/' arrives as %2B /
// %2F), not url.QueryEscape (which would form-encode them).
func TestListObjectsBuildsQueryWithAWSEscape(t *testing.T) {
	var gotRawQuery string
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		_, _ = io.WriteString(w, `<?xml version="1.0"?>
<ListBucketResult><Name>b</Name><IsTruncated>false</IsTruncated></ListBucketResult>`)
	})
	c.Prefix = "a+b/c"

	if _, err := c.ListObjects(context.Background()); err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	// Spaces would be '+' under url.QueryEscape; the '+' itself would also
	// stay unencoded. AWS rules demand '%2B' for '+' and '%2F' for '/'.
	if !strings.Contains(gotRawQuery, "prefix=a%2Bb%2Fc%2F") {
		t.Errorf("raw query = %q, want prefix=a%%2Bb%%2Fc%%2F (AWS-escaped)", gotRawQuery)
	}
}

// TestPathStyleAddressing verifies the Endpoint-override path-style form puts
// the bucket in the URL path (so LocalStack/MinIO know which bucket) and
// preserves any base path the endpoint carries — and that the default
// (no override) stays virtual-hosted with the bucket only in the host.
func TestPathStyleAddressing(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	// Endpoint with a base path: /s3/<bucket>/<key>.
	c := &Client{
		Bucket: "techgo-site", Region: "us-east-1", Creds: testCreds,
		HTTP: srv.Client(), Endpoint: srv.URL + "/s3", Now: fixedTime,
	}
	if err := c.PutObject(context.Background(), "media/0001.mp3", []byte("x"), "audio/mpeg", ""); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if gotPath != "/s3/techgo-site/media/0001.mp3" {
		t.Errorf("path-style path = %q, want /s3/techgo-site/media/0001.mp3", gotPath)
	}

	// Default (no override) is virtual-hosted: bucket is in endpoint()'s host,
	// the request path is just the key.
	vc := &Client{Bucket: "techgo-site", Region: "us-east-1"}
	if vc.endpoint() != "https://techgo-site.s3.us-east-1.amazonaws.com" {
		t.Errorf("virtual-host endpoint = %q", vc.endpoint())
	}
}

// TestPutObjectStreamHashesWithoutHoldingBodyInMemory verifies the streaming
// PUT path: the body is signed correctly (the server, recomputing nothing,
// just needs the bytes to arrive) and the request carries the SigV4
// X-Amz-Content-Sha256 of the streamed content. It also asserts the reader
// was rewound (the wire body equals the full input, not a post-hash empty
// reader).
func TestPutObjectStreamHashesWithoutHoldingBodyInMemory(t *testing.T) {
	payload := bytes.Repeat([]byte("techgo-podcast-bytes "), 4096) // ~84 KiB
	wantHash := sha256.Sum256(payload)

	var gotBody []byte
	var gotSha string
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotSha = r.Header.Get("X-Amz-Content-Sha256")
		w.WriteHeader(http.StatusOK)
	})

	if err := c.PutObjectStream(context.Background(), "media/0001.mp4",
		bytes.NewReader(payload), int64(len(payload)), "video/mp4",
		"public, max-age=86400, must-revalidate"); err != nil {
		t.Fatalf("PutObjectStream: %v", err)
	}
	if !bytes.Equal(gotBody, payload) {
		t.Errorf("wire body length = %d, want %d (reader not rewound after hashing?)", len(gotBody), len(payload))
	}
	if gotSha != hex.EncodeToString(wantHash[:]) {
		t.Errorf("X-Amz-Content-Sha256 = %q, want %x", gotSha, wantHash)
	}
}
