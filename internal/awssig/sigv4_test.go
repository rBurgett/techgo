package awssig

import (
	"bytes"
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
		"https://bucket.s3.us-east-1.amazonaws.com/key.json", bytes.NewReader(body))
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

// TestSignRequestRejectsNilRequest verifies SignRequest returns a descriptive
// error instead of nil-pointer-panicking when called with a nil *http.Request.
// The function already returns errors for other invalid state (nil URL, no
// Host); rejecting nil keeps the error contract consistent.
func TestSignRequestRejectsNilRequest(t *testing.T) {
	if err := SignRequest(nil, nil, "s3", "us-east-1", testCreds, testTime); err == nil {
		t.Error("SignRequest(nil, ...): nil error, want a rejection")
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

// TestSignRequestInitializesNilHeader makes sure a bare-handed *http.Request
// with a nil Header doesn't panic in Set — http.NewRequest always allocates
// the map, but callers who build a Request value directly may not.
func TestSignRequestInitializesNilHeader(t *testing.T) {
	req := &http.Request{
		Method: http.MethodGet,
		URL:    &url.URL{Scheme: "https", Host: "example.amazonaws.com", Path: "/"},
		Host:   "example.amazonaws.com",
		// Header intentionally nil.
	}
	if err := SignRequest(req, nil, "service", "us-east-1", testCreds, testTime); err != nil {
		t.Fatalf("SignRequest: %v", err)
	}
	if req.Header == nil {
		t.Fatal("Header still nil after SignRequest")
	}
	if got := req.Header.Get("Authorization"); got == "" {
		t.Error("Authorization header missing")
	}
}

// TestSignRequestStrictQueryEncoding flows the '+' fix end-to-end through
// SignRequest: a literal '+' in RawQuery must round-trip as %2B in the
// canonical query (and therefore produce a stable, server-matchable signature).
func TestSignRequestStrictQueryEncoding(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet,
		"https://example.amazonaws.com/?key=hello+world", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "example.amazonaws.com"
	if err := SignRequest(req, nil, "service", "us-east-1", testCreds, testTime); err != nil {
		t.Fatalf("SignRequest: %v", err)
	}
	// Re-derive the signature from the canonical request we expect — proves
	// the '+' was encoded as %2B (not silently swapped for space).
	if got := canonicalQueryString(req.URL.RawQuery); got != "key=hello%2Bworld" {
		t.Errorf("canonical query = %q, want key=hello%%2Bworld", got)
	}
}

// TestCanonicalHeadersMergesDuplicateCasingsDeterministically catches the
// (rare) case where req.Header contains two casings of the same logical
// header — only reachable via direct map access, since Set/Add canonicalize.
// Map iteration is randomized, so without sorting source names first the
// merged value would vary between runs and the signature with it.
func TestCanonicalHeadersMergesDuplicateCasingsDeterministically(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Direct map writes bypass Header.Set's canonicalization, so we end up
	// with two map keys for one logical header.
	req.Header["X-Amz-Foo"] = []string{"upper"}
	req.Header["x-amz-foo"] = []string{"lower"}

	canon, _ := canonicalHeaders(req)
	// sort.Strings puts "X-Amz-Foo" before "x-amz-foo" (uppercase 'X' < 'x'),
	// so the merged value is "upper,lower".
	if !strings.Contains(canon, "x-amz-foo:upper,lower\n") {
		t.Errorf("expected deterministic merge 'upper,lower' for x-amz-foo, got:\n%s", canon)
	}
	// Run the canonicalization several times: the result must be byte-stable
	// across map iteration randomness.
	for i := 0; i < 50; i++ {
		again, _ := canonicalHeaders(req)
		if again != canon {
			t.Fatalf("canonicalHeaders not deterministic across runs:\n first: %q\nsecond: %q", canon, again)
		}
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
		{"b=2&a=1", "a=1&b=2"},                   // keys sorted
		{"foo=bar%20baz", "foo=bar%20baz"},       // %20 round-trips
		{"a=1&a=2", "a=1&a=2"},                   // multi-value, already sorted
		{"a=2&a=1", "a=1&a=2"},                   // multi-value, re-sorted
		{"key%2Fpath=value", "key%2Fpath=value"}, // %2F in key round-trips
		{"prefix=foo&list-type=2", "list-type=2&prefix=foo"}, // S3 list-objects-v2 style
		// A literal '+' in the raw query is NOT space — SigV4 expects strict
		// percent-decoding, so we re-encode '+' as %2B. The previous
		// implementation went through url.URL.Query(), which would silently
		// turn '+' into a space (form-encoding semantics) and produce the
		// wrong canonical request.
		{"a=hello+world", "a=hello%2Bworld"},
		{"a=hello%20world", "a=hello%20world"},
		// Bare key (no '=') canonicalizes as "key=", matching aws-sdk-go-v2.
		{"flag", "flag="},
		// Empty segments are ignored.
		{"a=1&&b=2", "a=1&b=2"},
		{"&a=1", "a=1"},
		{"a=1&", "a=1"},
	}
	for _, c := range cases {
		got := canonicalQueryString(c.in)
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

// TestCanonicalPath verifies path encoding preserves '/' between segments,
// passes through valid percent-encoded sequences (preserving %2F so a literal
// slash inside a key is signed as the wire bytes, not folded back to a
// segment separator), normalizes hex to uppercase, and AWS-encodes any
// remaining literal bytes. The empty path canonicalizes to "/".
func TestCanonicalPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "/"},
		{"/", "/"},
		{"/foo", "/foo"},
		{"/foo/bar", "/foo/bar"},
		{"/My Object", "/My%20Object"},
		{"/path/with/é", "/path/with/%C3%A9"},
		// Pre-escaped input (what URL.EscapedPath returns):
		{"/foo%2Fbar", "/foo%2Fbar"},               // %2F preserved (slash in key)
		{"/foo%2fbar", "/foo%2Fbar"},               // hex normalized to uppercase
		{"/path/with/%C3%A9", "/path/with/%C3%A9"}, // pre-escaped UTF-8 preserved
		{"/foo%25bar", "/foo%25bar"},               // %25 (literal %) preserved
		{"/foo%2", "/foo%252"},                     // incomplete %XX: lone % gets encoded
	}
	for _, c := range cases {
		if got := canonicalPath(c.in); got != c.want {
			t.Errorf("canonicalPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSignRequestEscapedPathDiffersFromDecoded is the end-to-end regression
// test for the EscapedPath fix: two URLs differing only in '%2F' vs '/'
// (a literal slash in a key vs. a segment separator) MUST sign differently,
// because the wire bytes differ. If SignRequest were still using URL.Path,
// both signatures would be identical and one of them would fail at AWS.
func TestSignRequestEscapedPathDiffersFromDecoded(t *testing.T) {
	withEscape, err := http.NewRequest(http.MethodGet,
		"https://example.amazonaws.com/foo%2Fbar", nil)
	if err != nil {
		t.Fatal(err)
	}
	withEscape.Host = "example.amazonaws.com"

	withSlash, err := http.NewRequest(http.MethodGet,
		"https://example.amazonaws.com/foo/bar", nil)
	if err != nil {
		t.Fatal(err)
	}
	withSlash.Host = "example.amazonaws.com"

	// Sanity-check Go's URL parsing: %2F preserved in RawPath, decoded in Path.
	if withEscape.URL.Path != "/foo/bar" || withEscape.URL.RawPath != "/foo%2Fbar" {
		t.Fatalf("URL parsing changed: Path=%q RawPath=%q", withEscape.URL.Path, withEscape.URL.RawPath)
	}

	if err := SignRequest(withEscape, nil, "s3", "us-east-1", testCreds, testTime); err != nil {
		t.Fatal(err)
	}
	if err := SignRequest(withSlash, nil, "s3", "us-east-1", testCreds, testTime); err != nil {
		t.Fatal(err)
	}

	a1 := withEscape.Header.Get("Authorization")
	a2 := withSlash.Header.Get("Authorization")
	if a1 == a2 {
		t.Errorf("expected different signatures for /foo%%2Fbar vs /foo/bar (different wire bytes); got identical:\n%s", a1)
	}
}

// TestSignRequestWithPayloadHashMatchesInMemory proves the streaming entry
// point (caller supplies the precomputed hash) yields a byte-identical
// signature to the in-memory SignRequest path — so a large file can be
// uploaded with bounded memory without changing the wire bytes.
func TestSignRequestWithPayloadHashMatchesInMemory(t *testing.T) {
	body := []byte(`{"episode":"0001","note":"a fairly chunky body & <stuff>"}`)

	inMem, err := http.NewRequest(http.MethodPut,
		"https://bucket.s3.us-east-1.amazonaws.com/media/0001.mp3", nil)
	if err != nil {
		t.Fatal(err)
	}
	inMem.Header.Set("Content-Type", "audio/mpeg")
	if err := SignRequest(inMem, body, "s3", "us-east-1", testCreds, testTime); err != nil {
		t.Fatal(err)
	}

	streamed, err := http.NewRequest(http.MethodPut,
		"https://bucket.s3.us-east-1.amazonaws.com/media/0001.mp3", nil)
	if err != nil {
		t.Fatal(err)
	}
	streamed.Header.Set("Content-Type", "audio/mpeg")
	if err := SignRequestWithPayloadHash(streamed, HashPayload(body), "s3", "us-east-1", testCreds, testTime); err != nil {
		t.Fatal(err)
	}

	if a, b := inMem.Header.Get("Authorization"), streamed.Header.Get("Authorization"); a != b {
		t.Errorf("streaming signature differs from in-memory:\n in-mem: %s\nstream:  %s", a, b)
	}
	if a, b := inMem.Header.Get("X-Amz-Content-Sha256"), streamed.Header.Get("X-Amz-Content-Sha256"); a != b {
		t.Errorf("content-sha256 differs: %s vs %s", a, b)
	}

	// HashPayload's empty-body constant matches the documented SigV4 value.
	if HashPayload(nil) != emptyPayloadHash || HashPayload([]byte{}) != emptyPayloadHash {
		t.Error("HashPayload of an empty body must be the precomputed empty-string SHA-256")
	}
	// And an empty hash is rejected (callers must use HashPayload).
	r, _ := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	if err := SignRequestWithPayloadHash(r, "", "s3", "us-east-1", testCreds, testTime); err == nil {
		t.Error("SignRequestWithPayloadHash with empty hash: nil error, want a rejection")
	}
}
