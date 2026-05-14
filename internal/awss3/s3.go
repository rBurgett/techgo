// Package awss3 is a minimal S3 client — just enough surface area for
// `techgo deploy`. It signs requests with internal/awssig (SigV4 over the
// standard library) and parses ListBucketResult responses with encoding/xml.
// No external SDK.
//
// Scope: the four operations the deploy command needs.
//
//   - PutObject — virtual-hosted PUT with explicit Content-Type / Cache-Control.
//   - GetObject — virtual-hosted GET, with a "found" boolean so callers can
//     probe for the deploy marker without treating 404 as fatal.
//   - ListObjects — ListObjectsV2 with continuation-token pagination, returning
//     keys *relative* to the optional client prefix.
//   - DeleteObject — virtual-hosted DELETE for prune.
//
// All four prepend Client.Prefix when constructing the URL and strip it back
// off list results, so callers work in terms of unscoped keys. URLs are the
// virtual-hosted regional form (https://<bucket>.s3.<region>.amazonaws.com/...);
// Endpoint can be overridden for LocalStack / emulator / tests, in which case
// the bucket is NOT folded into the hostname (the override is used verbatim).
package awss3

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/rBurgett/techgo/internal/awssig"
)

// Client is an S3 client for a single bucket/region.
type Client struct {
	Bucket string
	Region string
	// Prefix is an optional key prefix scoping every operation. A leading
	// slash is trimmed and a trailing slash is added internally, so callers
	// can write either "site" or "site/" or even "site/sub".
	Prefix string
	Creds  awssig.Credentials
	// HTTP is the http client used for transport; defaults to
	// http.DefaultClient when nil.
	HTTP *http.Client
	// Endpoint overrides the AWS endpoint. Empty (the default) uses the
	// virtual-hosted regional URL <bucket>.s3.<region>.amazonaws.com.
	// Setting Endpoint targets a non-AWS URL (LocalStack, an httptest
	// server) directly — the bucket is NOT folded into the hostname; the
	// override is used verbatim and the bucket only appears in the signed
	// request via the URL path or virtual host, depending on the server.
	// For our actual tests this lets us point at httptest.Server.URL.
	Endpoint string
	// Now is the time source used for SigV4 dates; defaults to time.Now.
	// Tests pin it to a fixed instant to keep canonical requests stable.
	Now func() time.Time
}

// PutObject uploads body to <prefix><key> with the given Content-Type and
// Cache-Control headers (either may be empty to skip). The body is read into
// memory once for signing (SigV4 hashes the payload) and once for the wire.
func (c *Client) PutObject(ctx context.Context, key string, body []byte, contentType, cacheControl string) error {
	req, err := c.newRequest(ctx, http.MethodPut, c.prefixedKey(key), "", bytes.NewReader(body))
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if cacheControl != "" {
		req.Header.Set("Cache-Control", cacheControl)
	}
	req.ContentLength = int64(len(body))
	if err := c.sign(req, body); err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("s3 PUT %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("s3 PUT %s: %s", key, errorBody(resp))
	}
	return nil
}

// GetObject fetches the object at <prefix><key>. A 404 returns found=false
// with no error so callers can probe for the deploy marker without ceremony.
// Any other non-2xx is returned as an error.
func (c *Client) GetObject(ctx context.Context, key string) (body []byte, found bool, err error) {
	req, err := c.newRequest(ctx, http.MethodGet, c.prefixedKey(key), "", nil)
	if err != nil {
		return nil, false, err
	}
	if err := c.sign(req, nil); err != nil {
		return nil, false, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("s3 GET %s: %w", key, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		b, rerr := io.ReadAll(resp.Body)
		if rerr != nil {
			return nil, true, fmt.Errorf("s3 GET %s: reading body: %w", key, rerr)
		}
		return b, true, nil
	case http.StatusNotFound:
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("s3 GET %s: %s", key, errorBody(resp))
	}
}

// DeleteObject deletes the object at <prefix><key>. Both 204 (the canonical
// success) and 200 are accepted.
func (c *Client) DeleteObject(ctx context.Context, key string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, c.prefixedKey(key), "", nil)
	if err != nil {
		return err
	}
	if err := c.sign(req, nil); err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("s3 DELETE %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("s3 DELETE %s: %s", key, errorBody(resp))
	}
	return nil
}

// ListObjects returns every key under the client's prefix, transparently
// following the IsTruncated/NextContinuationToken pagination chain. The
// returned keys are *relative* to Prefix (the prefix is stripped) so callers
// can compare them against the local file set directly.
func (c *Client) ListObjects(ctx context.Context) ([]string, error) {
	var (
		out    []string
		token  string
		prefix = c.effectivePrefix()
		// Safety cap so a misbehaving server can't loop forever.
		maxPages = 10_000
	)
	for page := 0; page < maxPages; page++ {
		keys, next, err := c.listOnce(ctx, prefix, token)
		if err != nil {
			return nil, err
		}
		out = append(out, keys...)
		if next == "" {
			return out, nil
		}
		token = next
	}
	return nil, fmt.Errorf("s3 ListObjects: pagination exceeded %d pages — refusing to loop forever", maxPages)
}

// listObjectsV2Result is the response body of ListObjectsV2. Element names
// match the S3 XML schema (the wire form is namespaced, but encoding/xml
// matches by local name).
type listObjectsV2Result struct {
	XMLName               xml.Name `xml:"ListBucketResult"`
	IsTruncated           bool     `xml:"IsTruncated"`
	NextContinuationToken string   `xml:"NextContinuationToken"`
	Contents              []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
}

func (c *Client) listOnce(ctx context.Context, prefix, token string) (keys []string, nextToken string, err error) {
	q := "list-type=2"
	if prefix != "" {
		q += "&prefix=" + awssig.URIEscape(prefix)
	}
	if token != "" {
		q += "&continuation-token=" + awssig.URIEscape(token)
	}
	// List operates at the bucket root (not under the key prefix in the URL
	// path) — the prefix appears in the query string instead.
	req, err := c.newRequest(ctx, http.MethodGet, "", q, nil)
	if err != nil {
		return nil, "", err
	}
	if err := c.sign(req, nil); err != nil {
		return nil, "", err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("s3 ListObjectsV2: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("s3 ListObjectsV2: %s", errorBody(resp))
	}
	body, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		return nil, "", fmt.Errorf("s3 ListObjectsV2: reading body: %w", rerr)
	}
	var parsed listObjectsV2Result
	if err := xml.Unmarshal(body, &parsed); err != nil {
		return nil, "", fmt.Errorf("s3 ListObjectsV2: parsing response: %w", err)
	}
	keys = make([]string, 0, len(parsed.Contents))
	for _, e := range parsed.Contents {
		k := strings.TrimPrefix(e.Key, prefix)
		keys = append(keys, k)
	}
	if parsed.IsTruncated {
		return keys, parsed.NextContinuationToken, nil
	}
	return keys, "", nil
}

// newRequest builds an http.Request with the right URL for one of the four
// operations. wirePath is the *prefixed* object key for object operations
// (PutObject etc. pass c.prefixedKey(key)) and "" for the list endpoint
// (which targets the bucket root regardless of Prefix — the prefix scopes
// the list via the query string instead). rawQuery is the pre-encoded
// canonical query (e.g. "list-type=2&prefix=foo"). body may be nil.
func (c *Client) newRequest(ctx context.Context, method, wirePath, rawQuery string, body io.Reader) (*http.Request, error) {
	u, err := url.Parse(c.endpoint())
	if err != nil {
		return nil, fmt.Errorf("awss3: invalid endpoint %q: %w", c.endpoint(), err)
	}
	// The signer reads URL.EscapedPath() and re-encodes; we set the unencoded
	// path so URL.String() produces correctly-escaped wire bytes and the
	// signer sees the same form.
	u.Path = "/" + wirePath
	u.RawQuery = rawQuery
	return http.NewRequestWithContext(ctx, method, u.String(), body)
}

// sign applies SigV4 to req. body is the exact bytes that will be transmitted
// (nil/empty for GET/DELETE), used to compute X-Amz-Content-Sha256.
func (c *Client) sign(req *http.Request, body []byte) error {
	return awssig.SignRequest(req, body, "s3", c.Region, c.Creds, c.now())
}

// endpoint returns the URL of the S3 service: the Endpoint override if set,
// otherwise the virtual-hosted regional URL for Bucket/Region.
func (c *Client) endpoint() string {
	if c.Endpoint != "" {
		return strings.TrimRight(c.Endpoint, "/")
	}
	return "https://" + c.Bucket + ".s3." + c.Region + ".amazonaws.com"
}

// prefixedKey returns the wire-form key — the configured prefix joined onto
// key with exactly one '/' between them, and any leading '/' on Prefix
// stripped so the resulting URL is well-formed.
func (c *Client) prefixedKey(key string) string {
	return c.effectivePrefix() + key
}

// effectivePrefix is Prefix normalized: path.Clean collapses any duplicate
// slashes ("//a//b//" → "/a/b"), leading slashes are stripped, and exactly
// one trailing slash is appended when the result is non-empty. So
// Prefix="site", "/site/", "//site//", and "site/sub" all do the right thing,
// and Prefix="" yields "".
func (c *Client) effectivePrefix() string {
	if strings.TrimSpace(c.Prefix) == "" {
		return ""
	}
	p := path.Clean("/" + c.Prefix)
	p = strings.TrimLeft(p, "/")
	if p == "" || p == "." {
		return ""
	}
	return p + "/"
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// errorBody summarizes a non-2xx response for an error message: the HTTP
// status plus up to 1 KiB of the (typically XML) body — enough to capture
// AWS's <Error><Code>...</Code><Message>...</Message></Error> envelope
// without dumping multi-megabyte bodies.
func errorBody(resp *http.Response) string {
	const limit = 1024
	body, _ := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	s := string(body)
	if len(body) > limit {
		s = string(body[:limit]) + "…"
	}
	if s == "" {
		return resp.Status
	}
	return resp.Status + ": " + strings.TrimSpace(s)
}
