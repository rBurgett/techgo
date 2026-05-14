// Package awscf is a minimal CloudFront client — just one operation,
// CreateInvalidation, which `techgo deploy` calls at the end of a deploy to
// purge edge caches for the paths that just changed.
//
// The CloudFront API is signed in us-east-1 / "cloudfront" regardless of
// where the distribution actually lives. SigV4 handles the signing via
// internal/awssig; this package only constructs the request envelope.
package awscf

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rBurgett/techgo/internal/awssig"
)

// cloudFrontAPIVersion is the date-stamped CloudFront API namespace. Used
// both in the request URL prefix and in the InvalidationBatch xmlns.
const cloudFrontAPIVersion = "2020-05-31"

// Client signs and sends CloudFront API requests.
type Client struct {
	Creds awssig.Credentials
	// HTTP is the http client used for transport; defaults to
	// http.DefaultClient when nil.
	HTTP *http.Client
	// Endpoint overrides the AWS CloudFront API endpoint. Empty (the
	// default) uses https://cloudfront.amazonaws.com. Set it to point at
	// LocalStack or an httptest.Server.
	Endpoint string
	// Now is the time source used for SigV4 dates and CallerReference;
	// defaults to time.Now. Tests pin it to a fixed instant to keep
	// canonical requests stable.
	Now func() time.Time
}

// invalidationBatch is the request body for CreateInvalidation. We build it
// with encoding/xml rather than a text template so the path strings inside
// <Path> get properly XML-escaped — a stray '&' in a query-string-bearing
// path would otherwise corrupt the document.
type invalidationBatch struct {
	XMLName         xml.Name `xml:"InvalidationBatch"`
	XMLNS           string   `xml:"xmlns,attr"`
	CallerReference string   `xml:"CallerReference"`
	Paths           paths    `xml:"Paths"`
}

type paths struct {
	Quantity int      `xml:"Quantity"`
	Items    pathList `xml:"Items"`
}

type pathList struct {
	Path []string `xml:"Path"`
}

// invalidationResponse is the response body of CreateInvalidation. The
// returned <Invalidation> element holds the Id we surface to callers (and a
// Status / CreateTime that we ignore for now).
type invalidationResponse struct {
	XMLName xml.Name `xml:"Invalidation"`
	ID      string   `xml:"Id"`
	Status  string   `xml:"Status"`
}

// CreateInvalidation submits an invalidation batch for distributionID and
// returns the new invalidation's Id on success. Paths are normalized so each
// starts with "/" (a leading slash is required by the CloudFront API; we
// prepend one when callers forget). A unique-enough CallerReference is
// generated from the current time so re-submitting the same paths picks up
// the new batch instead of getting deduplicated.
func (c *Client) CreateInvalidation(ctx context.Context, distributionID string, pathsIn []string) (string, error) {
	if strings.TrimSpace(distributionID) == "" {
		return "", fmt.Errorf("awscf: distributionID is required")
	}
	if len(pathsIn) == 0 {
		return "", fmt.Errorf("awscf: at least one path is required")
	}

	normalized := make([]string, len(pathsIn))
	for i, p := range pathsIn {
		normalized[i] = normalizePath(p)
	}

	now := c.now()
	batch := invalidationBatch{
		XMLNS:           "http://cloudfront.amazonaws.com/doc/" + cloudFrontAPIVersion + "/",
		CallerReference: fmt.Sprintf("techgo-%d", now.UnixNano()),
		Paths: paths{
			Quantity: len(normalized),
			Items:    pathList{Path: normalized},
		},
	}
	xmlBody, err := marshalRequestXML(batch)
	if err != nil {
		return "", fmt.Errorf("awscf: encoding invalidation batch: %w", err)
	}

	url := strings.TrimRight(c.endpoint(), "/") + "/" + cloudFrontAPIVersion +
		"/distribution/" + distributionID + "/invalidation"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(xmlBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "text/xml")
	req.ContentLength = int64(len(xmlBody))
	// CloudFront API is always us-east-1 / "cloudfront", regardless of
	// where the distribution lives. SignRequest only sets the S3-only
	// X-Amz-Content-Sha256 header for service=="s3", so this is correct
	// even though the body is non-empty.
	if err := awssig.SignRequest(req, xmlBody, "cloudfront", "us-east-1", c.Creds, now); err != nil {
		return "", err
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("awscf CreateInvalidation: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("awscf CreateInvalidation: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var parsed invalidationResponse
	if err := xml.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("awscf CreateInvalidation: parsing response: %w", err)
	}
	if parsed.ID == "" {
		return "", fmt.Errorf("awscf CreateInvalidation: response has no <Id>: %s", string(body))
	}
	return parsed.ID, nil
}

// marshalRequestXML returns the canonical XML form of a CloudFront API
// request body: the standard <?xml ...?> declaration on its own line, then
// the indented document.
func marshalRequestXML(v any) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// normalizePath returns p with exactly one leading '/', so callers can pass
// "/foo", "foo", or even "  foo  " interchangeably. Empty input falls back
// to "/*" — the most common (and CloudFront-cheapest) invalidation path,
// kept for callers that pass a stray empty entry from a CSV split.
func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/*"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// NormalizePaths is the exported batch form of normalizePath, intended for
// the deploy command which receives a comma-separated --paths flag.
func NormalizePaths(in []string) []string {
	out := make([]string, 0, len(in))
	for _, p := range in {
		out = append(out, normalizePath(p))
	}
	return out
}

func (c *Client) endpoint() string {
	if c.Endpoint != "" {
		return c.Endpoint
	}
	return "https://cloudfront.amazonaws.com"
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
