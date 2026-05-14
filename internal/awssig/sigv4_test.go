package awssig

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// testCreds is the public AWS-provided test key pair used throughout the
// SigV4 test suite documentation. NOT a real credential.
var testCreds = Credentials{
	AccessKeyID:     "AKIDEXAMPLE",
	SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
}

// testTime matches the SigV4 test-suite timestamp (2015-08-30T12:36:00Z).
var testTime = time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)

// TestSignRequestGetVanilla replays the "get-vanilla" case from the AWS-
// published SigV4 test suite: GET https://example.amazonaws.com/, no query,
// no body, service "service", region "us-east-1". The expected signature
// 5fa00fa3... is the canonical fixture used by aws-sdk-go-v2's signer tests
// and reproduced in many third-party SigV4 implementations.
//
// If this test breaks, every signed request we make to AWS would silently
// fail authentication — it's the most important assertion in the package.
func TestSignRequestGetVanilla(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Pinning Host explicitly removes any dependency on what Go's http stack
	// would compute from URL.Host (which it does only at transport time).
	req.Host = "example.amazonaws.com"

	if err := SignRequest(req, nil, "service", "us-east-1", testCreds, testTime); err != nil {
		t.Fatalf("SignRequest: %v", err)
	}

	if got := req.Header.Get("X-Amz-Date"); got != "20150830T123600Z" {
		t.Errorf("X-Amz-Date = %q, want 20150830T123600Z", got)
	}
	// service != "s3" so the X-Amz-Content-Sha256 header must NOT be set.
	if got := req.Header.Get("X-Amz-Content-Sha256"); got != "" {
		t.Errorf("X-Amz-Content-Sha256 was set for non-S3 service: %q", got)
	}

	wantAuth := "AWS4-HMAC-SHA256 " +
		"Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, " +
		"SignedHeaders=host;x-amz-date, " +
		"Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got := req.Header.Get("Authorization"); got != wantAuth {
		t.Errorf("Authorization\n got: %q\nwant: %q", got, wantAuth)
	}
}

// TestSignRequestIAMListUsers replays the IAM ListUsers tutorial example from
// the AWS-published SigV4 request examples doc:
//
//	https://docs.aws.amazon.com/general/latest/gr/sigv4-signed-request-examples.html
//
// This case is independent of get-vanilla and exercises both a non-empty
// canonical query string and a signed Content-Type header.
func TestSignRequestIAMListUsers(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet,
		"https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "iam.amazonaws.com"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")

	if err := SignRequest(req, nil, "iam", "us-east-1", testCreds, testTime); err != nil {
		t.Fatalf("SignRequest: %v", err)
	}

	wantAuth := "AWS4-HMAC-SHA256 " +
		"Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request, " +
		"SignedHeaders=content-type;host;x-amz-date, " +
		"Signature=5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"
	if got := req.Header.Get("Authorization"); got != wantAuth {
		t.Errorf("Authorization\n got: %q\nwant: %q", got, wantAuth)
	}
}

// TestSignRequestS3PutSetsContentSha256 verifies that an S3-flavored request
// (service "s3", real body) gets X-Amz-Content-Sha256 filled in with the
// body's hash and folded into SignedHeaders alongside content-type.
func TestSignRequestS3PutSetsContentSha256(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	req, err := http.NewRequest(http.MethodPut,
		"https://bucket.s3.us-east-1.amazonaws.com/key.json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	if err := SignRequest(req, body, "s3", "us-east-1", testCreds, testTime); err != nil {
		t.Fatalf("SignRequest: %v", err)
	}

	sum := sha256.Sum256(body)
	wantHash := hex.EncodeToString(sum[:])
	if got := req.Header.Get("X-Amz-Content-Sha256"); got != wantHash {
		t.Errorf("X-Amz-Content-Sha256 = %q, want %q", got, wantHash)
	}

	auth := req.Header.Get("Authorization")
	if !strings.Contains(auth, "SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date") {
		t.Errorf("Authorization SignedHeaders incorrect, got: %q", auth)
	}
}

// TestSignRequestEmptyBodyHash verifies the precomputed empty-body hash is
// used (instead of running sha256 over a nil slice every call) and matches
// the AWS-published constant.
func TestSignRequestEmptyBodyHash(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://bucket.s3.us-east-1.amazonaws.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := SignRequest(req, nil, "s3", "us-east-1", testCreds, testTime); err != nil {
		t.Fatalf("SignRequest: %v", err)
	}
	if got := req.Header.Get("X-Amz-Content-Sha256"); got != emptyPayloadHash {
		t.Errorf("X-Amz-Content-Sha256 = %q, want %q", got, emptyPayloadHash)
	}
}

// TestSignRequestSessionToken verifies that temporary-credential support
// (STS / assumed-role / IAM Identity Center) sets X-Amz-Security-Token AND
// folds it into the signed-headers list — both are required by AWS, missing
// either produces a SignatureDoesNotMatch response.
func TestSignRequestSessionToken(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://cloudfront.amazonaws.com/2020-05-31/distribution", nil)
	if err != nil {
		t.Fatal(err)
	}
	creds := testCreds
	creds.SessionToken = "FQoGZXIvYXdzEXAMPLEsessiontokenpayload=="
	if err := SignRequest(req, nil, "cloudfront", "us-east-1", creds, testTime); err != nil {
		t.Fatalf("SignRequest: %v", err)
	}

	if got := req.Header.Get("X-Amz-Security-Token"); got != creds.SessionToken {
		t.Errorf("X-Amz-Security-Token = %q, want %q", got, creds.SessionToken)
	}
	auth := req.Header.Get("Authorization")
	if !strings.Contains(auth, "x-amz-security-token") {
		t.Errorf("Authorization should sign x-amz-security-token, got: %q", auth)
	}
}

// TestSignRequestRejectsRequestWithoutHost guards against accidentally signing
// a request that won't have a Host when it ships, which would produce a valid-
// looking but server-rejected signature (since the server canonicalizes its
// view of the request including the Host header).
func TestSignRequestRejectsRequestWithoutHost(t *testing.T) {
	req := &http.Request{
		Method: http.MethodGet,
		URL:    &url.URL{Scheme: "https", Path: "/"},
		Header: http.Header{},
	}
	if err := SignRequest(req, nil, "s3", "us-east-1", testCreds, testTime); err == nil {
		t.Error("SignRequest with no Host: nil error, want a rejection")
	}
}

func TestUriEncode(t *testing.T) {
	cases := []struct {
		in          string
		encodeSlash bool
		want        string
	}{
		{"", false, ""},
		{"/", false, "/"},               // slash preserved in path mode
		{"/", true, "%2F"},              // slash encoded in query mode
		{"foo bar", false, "foo%20bar"}, // space
		{"hello.world", false, "hello.world"},
		{"~test_-.", false, "~test_-."},         // every unreserved
		{"abc/def", false, "abc/def"},           // multiple segments preserved
		{"abc/def", true, "abc%2Fdef"},          // multiple segments encoded
		{"&=?", false, "%26%3D%3F"},             // gen-delims
		{"%", false, "%25"},                     // the percent itself
		{"é", false, "%C3%A9"},                  // UTF-8 multi-byte
		{"AB12-_.~", false, "AB12-_.~"},         // mixed unreserved
		{"a b/c d", false, "a%20b/c%20d"},       // mixed, path mode
		{"a b/c d", true, "a%20b%2Fc%20d"},      // mixed, query mode
		{"My Object?", false, "My%20Object%3F"}, // S3-style key with reserved
	}
	for _, c := range cases {
		got := uriEncode(c.in, c.encodeSlash)
		if got != c.want {
			t.Errorf("uriEncode(%q, encodeSlash=%v) = %q, want %q",
				c.in, c.encodeSlash, got, c.want)
		}
	}
}

func TestCanonicalQueryString(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"a=1", "a=1"},
		{"b=2&a=1", "a=1&b=2"},                 // keys sorted
		{"foo=bar baz", "foo=bar%20baz"},       // value encoded
		{"a=1&a=2", "a=1&a=2"},                 // multi-value, already sorted
		{"a=2&a=1", "a=1&a=2"},                 // multi-value, re-sorted
		{"key/path=value", "key%2Fpath=value"}, // '/' in key encoded
		{"prefix=foo&list-type=2", "list-type=2&prefix=foo"}, // S3 list-objects-v2 style
	}
	for _, c := range cases {
		v, err := url.ParseQuery(c.in)
		if err != nil {
			t.Fatalf("ParseQuery(%q): %v", c.in, err)
		}
		got := canonicalQueryString(v)
		if got != c.want {
			t.Errorf("canonicalQueryString(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCanonicalHeadersTrimAndCollapse(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Amz-Meta-Foo", "  hello   world  ")
	req.Header.Set("Content-Type", "  text/plain  ")
	req.Header.Set("Authorization", "should-not-be-signed") // not in our signed set
	req.Header.Set("Accept", "should-not-be-signed-either")

	canon, signed := canonicalHeaders(req)
	if !strings.Contains(canon, "x-amz-meta-foo:hello world\n") {
		t.Errorf("expected collapsed value 'hello world', got canonical:\n%s", canon)
	}
	if !strings.Contains(canon, "content-type:text/plain\n") {
		t.Errorf("expected trimmed content-type, got canonical:\n%s", canon)
	}
	if signed != "content-type;host;x-amz-meta-foo" {
		t.Errorf("signed headers = %q, want %q", signed, "content-type;host;x-amz-meta-foo")
	}
	if strings.Contains(signed, "authorization") || strings.Contains(signed, "accept") {
		t.Errorf("unexpected headers folded into signed: %q", signed)
	}
}

// TestCanonicalHeadersMatchesCaseInsensitively makes sure a header set via the
// raw map in non-canonical case (or x-amz-* added by a lowercase variant) is
// still picked up — we range over req.Header and lowercase ourselves rather
// than relying on http.Header.Get's canonical-form matching.
func TestCanonicalHeadersMatchesCaseInsensitively(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Bypass Header.Set's CanonicalMIMEHeaderKey normalization by writing to
	// the map directly with a lowercase name — exercises our case-insensitive
	// match.
	req.Header["x-amz-weird-case"] = []string{"value"}
	_, signed := canonicalHeaders(req)
	if !strings.Contains(signed, "x-amz-weird-case") {
		t.Errorf("expected x-amz-weird-case to be signed, got: %q", signed)
	}
}

// TestCanonicalPath verifies path encoding preserves '/' between segments and
// percent-encodes everything else per AWS rules. The empty path canonicalizes
// to "/" so the canonical request is never missing the path field.
func TestCanonicalPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "/"},
		{"/", "/"},
		{"/foo", "/foo"},
		{"/foo/bar", "/foo/bar"},
		{"/My Object", "/My%20Object"},
		{"/path/with/é", "/path/with/%C3%A9"},
	}
	for _, c := range cases {
		if got := canonicalPath(c.in); got != c.want {
			t.Errorf("canonicalPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
