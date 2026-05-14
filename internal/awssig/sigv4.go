// Package awssig signs HTTP requests with AWS Signature Version 4 using only
// the standard library — crypto/hmac, crypto/sha256, encoding/hex, net/http,
// net/url, sort, strings. It is consumed by internal/awss3 (S3 PutObject /
// GetObject / ListObjects / DeleteObject) and internal/awscf (CloudFront
// CreateInvalidation), so the techgo CLI can deploy without an external AWS
// SDK or the aws CLI.
//
// Scope: this implements the subset of SigV4 we need:
//
//   - HMAC-SHA256 signatures via Authorization header (no presigned URLs).
//   - Static credentials (long-lived access keys), plus optional X-Amz-
//     Security-Token for STS / IAM Identity Center / assumed-role temporary
//     credentials.
//   - Single-encoded canonical URI (the S3 convention). The non-S3 services we
//     target only use path components that are unaffected by the double-
//     encoding wart in the SigV4 spec, so single-encoding gives the same
//     canonical bytes either way. If a future caller signs a non-S3 path with
//     reserved characters, that assumption needs revisiting.
//
// The package is intentionally small (one type, one exported function, a
// handful of helpers) and exhaustively unit-tested against two published AWS
// SigV4 test vectors — get-vanilla and the IAM ListUsers tutorial — plus
// targeted regression tests for each canonicalization step.
package awssig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	algorithm  = "AWS4-HMAC-SHA256"
	terminator = "aws4_request"
	// emptyPayloadHash is the SHA-256 hex of the empty string. Used for any
	// request with no body, so we don't pay the cost of hashing nothing every
	// call (and so callers can pass a nil body without surprises).
	emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	// upperHex is the alphabet AWS canonicalization uses for percent-encoding
	// (uppercase). Shared by uriEncode and canonicalPath.
	upperHex = "0123456789ABCDEF"
)

// Credentials is the static-credentials triple. SessionToken is empty for
// long-lived IAM access keys and non-empty for STS / assumed-role / IAM
// Identity Center temporary credentials — in which case SignRequest adds the
// X-Amz-Security-Token header and folds it into the signed headers, as AWS
// requires.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// SignRequest applies AWS Signature Version 4 to req in place. It mutates req
// to:
//   - set X-Amz-Date to now (formatted as 20060102T150405Z, UTC);
//   - set X-Amz-Content-Sha256 to the hex SHA-256 of body, but ONLY when
//     service == "s3" (S3 requires it; other services would refuse the request
//     if we set it without also signing it the way they expect);
//   - set X-Amz-Security-Token when creds.SessionToken is non-empty (and fold
//     it into the signed headers);
//   - set Authorization to the SigV4 string.
//
// body is the full request body (nil/empty is fine — the empty-body hash is
// precomputed). service is the AWS service name ("s3", "cloudfront", …);
// region is the relevant region: the bucket region for S3, "us-east-1" for the
// CloudFront API regardless of where the distribution lives. now is the
// signing time, injected so tests are deterministic.
func SignRequest(req *http.Request, body []byte, service, region string, creds Credentials, now time.Time) error {
	if req == nil {
		return fmt.Errorf("awssig: request is nil")
	}
	if req.URL == nil {
		return fmt.Errorf("awssig: request URL is nil")
	}
	if req.Host == "" && req.URL.Host == "" {
		return fmt.Errorf("awssig: request has no Host (set req.Host or req.URL.Host)")
	}
	// http.NewRequest always initializes Header, but a bare-handed *http.Request
	// might not; .Set on a nil http.Header would panic, so make the empty map.
	if req.Header == nil {
		req.Header = make(http.Header)
	}

	amzDate := now.UTC().Format("20060102T150405Z")
	dateStamp := now.UTC().Format("20060102")

	payloadHash := emptyPayloadHash
	if len(body) > 0 {
		sum := sha256.Sum256(body)
		payloadHash = hex.EncodeToString(sum[:])
	}

	// Set the signed-by-default headers BEFORE building the canonical request,
	// so they're included in the signature.
	req.Header.Set("X-Amz-Date", amzDate)
	if service == "s3" {
		req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	}
	if creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", creds.SessionToken)
	}

	// EscapedPath, not Path: net/http sends the escaped form on the wire, and
	// AWS canonicalizes from the bytes it receives. Signing the *decoded* path
	// would silently fold '%2F' (a literal slash inside a key) into '/' (a
	// segment separator), producing a signature over different bytes than the
	// server computes and a SignatureDoesNotMatch failure.
	canonicalURI := canonicalPath(req.URL.EscapedPath())
	canonicalQuery := canonicalQueryString(req.URL.RawQuery)
	canonicalHeadersStr, signedHeaders := canonicalHeaders(req)

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQuery,
		canonicalHeadersStr,
		signedHeaders,
		payloadHash,
	}, "\n")

	credentialScope := strings.Join([]string{dateStamp, region, service, terminator}, "/")
	crSum := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		algorithm,
		amzDate,
		credentialScope,
		hex.EncodeToString(crSum[:]),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+creds.SecretAccessKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	kSigning := hmacSHA256(kService, []byte(terminator))
	signature := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))

	req.Header.Set("Authorization",
		algorithm+" Credential="+creds.AccessKeyID+"/"+credentialScope+
			", SignedHeaders="+signedHeaders+
			", Signature="+signature)
	return nil
}

// hmacSHA256 returns HMAC-SHA256(key, data).
func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// canonicalPath turns a URL's *escaped* path (URL.EscapedPath(), not Path)
// into the SigV4 canonical URI. Valid percent-encoded sequences (%XX with two
// hex digits) pass through with their hex digits normalized to uppercase — the
// AWS canonical form. Any remaining literal byte is AWS-encoded if it isn't in
// the unreserved set; '/' is always preserved as a segment separator (a slash
// embedded in a key is already %2F in the input). An empty path becomes "/".
//
// Working from the escaped form is what makes a key containing a literal '/'
// sign correctly: URL.Path = "/foo/bar", URL.RawPath = "/foo%2Fbar". Signing
// Path would sign different bytes than net/http sends; signing
// EscapedPath() ("/foo%2Fbar") matches the wire.
func canonicalPath(escapedPath string) string {
	if escapedPath == "" {
		return "/"
	}
	var b strings.Builder
	b.Grow(len(escapedPath))
	for i := 0; i < len(escapedPath); {
		c := escapedPath[i]
		if c == '%' && i+2 < len(escapedPath) && isHexDigit(escapedPath[i+1]) && isHexDigit(escapedPath[i+2]) {
			b.WriteByte('%')
			b.WriteByte(toUpperHex(escapedPath[i+1]))
			b.WriteByte(toUpperHex(escapedPath[i+2]))
			i += 3
			continue
		}
		switch {
		case isUnreserved(c), c == '/':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(upperHex[c>>4])
			b.WriteByte(upperHex[c&0xf])
		}
		i++
	}
	return b.String()
}

// canonicalQueryString returns the canonical sorted, percent-encoded query
// string per SigV4: <encoded-key>=<encoded-value> pairs joined by '&', sorted
// by encoded key, ties broken by encoded value (so multi-value keys are also
// deterministic). Returns "" when rawQuery is empty.
//
// We parse rawQuery ourselves with PathUnescape (NOT url.URL.Query, which uses
// form-decoding and turns '+' into space). AWS SigV4 expects strict
// percent-decoding of the query — a literal '+' in a key or value is preserved
// (re-encoded as %2B), not silently converted to space. Empty segments and a
// missing '=' are tolerated the same way the AWS SDKs handle them.
func canonicalQueryString(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	type kv struct{ k, v string }
	var pairs []kv
	for _, pair := range strings.Split(rawQuery, "&") {
		if pair == "" {
			continue
		}
		rawK, rawV, _ := strings.Cut(pair, "=")
		decK, err := url.PathUnescape(rawK)
		if err != nil {
			decK = rawK
		}
		decV, err := url.PathUnescape(rawV)
		if err != nil {
			decV = rawV
		}
		pairs = append(pairs, kv{uriEncode(decK, true), uriEncode(decV, true)})
	}
	if len(pairs) == 0 {
		return ""
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(p.k)
		b.WriteByte('=')
		b.WriteString(p.v)
	}
	return b.String()
}

// canonicalHeaders builds the canonical headers block ("name:value\n" per
// signed header, in sorted lowercase-name order) plus the semicolon-joined
// signedHeaders string. The set of signed headers is host (always),
// content-type (if set), and every x-amz-* header present on the request —
// exactly the AWS-required minimum for our use cases. Header names are
// matched case-insensitively (mixed-cased x-amz-* set by a future caller
// would still be picked up). Header values are trimmed and have internal
// whitespace runs collapsed to a single space, per the SigV4 spec.
//
// The source-name iteration is sorted before map insertion so that two
// different casings of the same logical header (only reachable via direct
// map access, since Set/Add canonicalize) merge in deterministic order —
// otherwise map-iteration randomness would make the signature vary across
// runs for the same request.
func canonicalHeaders(req *http.Request) (canonical, signed string) {
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}

	srcNames := make([]string, 0, len(req.Header))
	for n := range req.Header {
		srcNames = append(srcNames, n)
	}
	sort.Strings(srcNames)

	headers := map[string]string{"host": host}
	for _, name := range srcNames {
		lname := strings.ToLower(name)
		if lname != "content-type" && !strings.HasPrefix(lname, "x-amz-") {
			continue
		}
		joined := strings.Join(req.Header[name], ",")
		if existing, ok := headers[lname]; ok {
			headers[lname] = existing + "," + joined
		} else {
			headers[lname] = joined
		}
	}
	for k, v := range headers {
		// strings.Fields drops leading/trailing whitespace and collapses any
		// internal whitespace runs (spaces, tabs, newlines) into single spaces.
		headers[k] = strings.Join(strings.Fields(v), " ")
	}

	outNames := make([]string, 0, len(headers))
	for k := range headers {
		outNames = append(outNames, k)
	}
	sort.Strings(outNames)

	var cb, sb strings.Builder
	for i, n := range outNames {
		cb.WriteString(n)
		cb.WriteByte(':')
		cb.WriteString(headers[n])
		cb.WriteByte('\n')
		if i > 0 {
			sb.WriteByte(';')
		}
		sb.WriteString(n)
	}
	return cb.String(), sb.String()
}

// URIEscape percent-encodes s using AWS SigV4 rules for a URI component:
// unreserved characters pass through, every other byte (including '/', '+',
// and other RFC-3986 reserved characters) becomes %XX with uppercase hex.
// Exported so callers that build URLs / query strings to be signed by
// SignRequest can use the *same* encoding the signer will canonicalize from
// — url.QueryEscape encodes spaces as '+' (form-encoding), which the AWS
// servers interpret as a literal '+', not a space.
func URIEscape(s string) string { return uriEncode(s, true) }

// uriEncode percent-encodes s per AWS SigV4 rules: unreserved characters
// (A-Z, a-z, 0-9, '-', '.', '_', '~') pass through unchanged; '/' passes
// through unchanged when encodeSlash is false (path segments); every other
// byte becomes %XX with uppercase hex. The byte-at-a-time loop is intentional:
// multi-byte UTF-8 runes are encoded as their constituent bytes, matching
// AWS's spec.
func uriEncode(s string, encodeSlash bool) string {
	// Fast path: nothing to encode. Slightly speeds up the common case (paths
	// that are pure ASCII without reserved characters) and avoids one alloc.
	clean := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !isUnreserved(c) && !(c == '/' && !encodeSlash) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case isUnreserved(c):
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(upperHex[c>>4])
			b.WriteByte(upperHex[c&0xf])
		}
	}
	return b.String()
}

func isUnreserved(c byte) bool {
	switch {
	case 'A' <= c && c <= 'Z',
		'a' <= c && c <= 'z',
		'0' <= c && c <= '9',
		c == '-' || c == '.' || c == '_' || c == '~':
		return true
	}
	return false
}

func isHexDigit(c byte) bool {
	return ('0' <= c && c <= '9') || ('a' <= c && c <= 'f') || ('A' <= c && c <= 'F')
}

func toUpperHex(c byte) byte {
	if 'a' <= c && c <= 'f' {
		return c - ('a' - 'A')
	}
	return c
}
