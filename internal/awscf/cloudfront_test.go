package awscf

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rBurgett/techgo/internal/awssig"
)

var testCreds = awssig.Credentials{
	AccessKeyID:     "AKIDEXAMPLE",
	SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
}

func fixedTime() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) }

// newTestServer wires up an httptest server and a Client targeting it.
func newTestServer(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	c := &Client{
		Creds:    testCreds,
		HTTP:     ts.Client(),
		Endpoint: ts.URL,
		Now:      fixedTime,
	}
	return c, ts
}

func TestCreateInvalidationHappyPath(t *testing.T) {
	var (
		gotURL         string
		gotMethod      string
		gotContentType string
		gotAuth        string
		gotXAmzDate    string
		gotBody        []byte
	)
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Path
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		gotXAmzDate = r.Header.Get("X-Amz-Date")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `<?xml version="1.0"?>
<Invalidation xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <Id>I2J0I21PCUYOIK</Id>
  <Status>InProgress</Status>
  <CreateTime>2026-05-14T12:00:00.000Z</CreateTime>
</Invalidation>`)
	})

	id, err := c.CreateInvalidation(context.Background(), "EDFDVBD632BHDS5", []string{"/*"})
	if err != nil {
		t.Fatalf("CreateInvalidation: %v", err)
	}
	if id != "I2J0I21PCUYOIK" {
		t.Errorf("id = %q, want I2J0I21PCUYOIK", id)
	}

	// URL: /<api-version>/distribution/<id>/invalidation, POST, text/xml,
	// signed (Authorization + X-Amz-Date headers present).
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotURL != "/2020-05-31/distribution/EDFDVBD632BHDS5/invalidation" {
		t.Errorf("URL = %q", gotURL)
	}
	if gotContentType != "text/xml" {
		t.Errorf("Content-Type = %q, want text/xml", gotContentType)
	}
	if !strings.HasPrefix(gotAuth,
		"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20260514/us-east-1/cloudfront/aws4_request,") {
		t.Errorf("Authorization scope wrong, got: %q", gotAuth)
	}
	if gotXAmzDate != "20260514T120000Z" {
		t.Errorf("X-Amz-Date = %q", gotXAmzDate)
	}

	// Body: well-formed XML with the right xmlns, Quantity matches Items,
	// CallerReference is non-empty and techgo-tagged.
	var parsed invalidationBatch
	if err := xml.Unmarshal(gotBody, &parsed); err != nil {
		t.Fatalf("request body not valid XML: %v\n%s", err, gotBody)
	}
	if parsed.XMLNS != "http://cloudfront.amazonaws.com/doc/2020-05-31/" {
		t.Errorf("xmlns = %q", parsed.XMLNS)
	}
	if !strings.HasPrefix(parsed.CallerReference, "techgo-") {
		t.Errorf("CallerReference = %q, want techgo-<nano>", parsed.CallerReference)
	}
	if parsed.Paths.Quantity != 1 || len(parsed.Paths.Items.Path) != 1 || parsed.Paths.Items.Path[0] != "/*" {
		t.Errorf("Paths = %+v, want Quantity=1 Items=[/*]", parsed.Paths)
	}
}

func TestCreateInvalidationNormalizesPaths(t *testing.T) {
	var gotBody []byte
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `<Invalidation><Id>X</Id></Invalidation>`)
	})

	// Mix of leading-slash / no-leading-slash / whitespace inputs.
	in := []string{"/index.html", "media/0001.mp3", "  feed.rss  ", "/episodes/*"}
	if _, err := c.CreateInvalidation(context.Background(), "EDFDVBD632BHDS5", in); err != nil {
		t.Fatalf("CreateInvalidation: %v", err)
	}
	var parsed invalidationBatch
	if err := xml.Unmarshal(gotBody, &parsed); err != nil {
		t.Fatalf("parsing body: %v", err)
	}
	want := []string{"/index.html", "/media/0001.mp3", "/feed.rss", "/episodes/*"}
	if len(parsed.Paths.Items.Path) != len(want) {
		t.Fatalf("got %d paths, want %d", len(parsed.Paths.Items.Path), len(want))
	}
	for i, p := range parsed.Paths.Items.Path {
		if p != want[i] {
			t.Errorf("path[%d] = %q, want %q", i, p, want[i])
		}
	}
	if parsed.Paths.Quantity != len(want) {
		t.Errorf("Quantity = %d, want %d (must equal len(Items))", parsed.Paths.Quantity, len(want))
	}
}

func TestCreateInvalidationCallerReferenceIsUnique(t *testing.T) {
	// Two calls one nanosecond apart should produce two different
	// CallerReferences (the Now field controls the time, so we tick it
	// between calls).
	var bodies [][]byte
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `<Invalidation><Id>X</Id></Invalidation>`)
	})

	tick := int64(0)
	c.Now = func() time.Time { tick++; return time.Date(2026, 5, 14, 12, 0, 0, int(tick), time.UTC) }

	for i := 0; i < 2; i++ {
		if _, err := c.CreateInvalidation(context.Background(), "EDFDVBD632BHDS5", []string{"/*"}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if len(bodies) != 2 {
		t.Fatalf("got %d bodies, want 2", len(bodies))
	}
	var a, b invalidationBatch
	_ = xml.Unmarshal(bodies[0], &a)
	_ = xml.Unmarshal(bodies[1], &b)
	if a.CallerReference == b.CallerReference {
		t.Errorf("CallerReference reused across calls: %q", a.CallerReference)
	}
}

func TestCreateInvalidationXMLEscapesPathContent(t *testing.T) {
	// A path containing '&' must arrive XML-escaped in the body — building
	// the body with encoding/xml gives us this for free.
	var gotBody []byte
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `<Invalidation><Id>X</Id></Invalidation>`)
	})

	if _, err := c.CreateInvalidation(context.Background(), "EDFDVBD632BHDS5",
		[]string{"/foo?a=1&b=2"}); err != nil {
		t.Fatalf("CreateInvalidation: %v", err)
	}
	if !strings.Contains(string(gotBody), "&amp;") {
		t.Errorf("'&' was not XML-escaped in the body:\n%s", gotBody)
	}
	// Round-trip: parsing the body recovers the literal '&'.
	var parsed invalidationBatch
	if err := xml.Unmarshal(gotBody, &parsed); err != nil {
		t.Fatalf("parsing body: %v", err)
	}
	if parsed.Paths.Items.Path[0] != "/foo?a=1&b=2" {
		t.Errorf("round-tripped path = %q", parsed.Paths.Items.Path[0])
	}
}

func TestCreateInvalidationNon2xxReturnsError(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `<ErrorResponse><Error><Code>InvalidArgument</Code></Error></ErrorResponse>`)
	})
	_, err := c.CreateInvalidation(context.Background(), "X", []string{"/*"})
	if err == nil {
		t.Fatal("CreateInvalidation should error on 400")
	}
	if !strings.Contains(err.Error(), "InvalidArgument") {
		t.Errorf("error should mention InvalidArgument, got: %v", err)
	}
}

func TestCreateInvalidationRejectsEmptyInput(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be hit on validation error")
	})
	if _, err := c.CreateInvalidation(context.Background(), "", []string{"/*"}); err == nil {
		t.Error("empty distributionID: nil error, want a rejection")
	}
	if _, err := c.CreateInvalidation(context.Background(), "X", nil); err == nil {
		t.Error("nil paths: nil error, want a rejection")
	}
	// All-blank input must fail safe (reject), NOT fall back to /* and
	// invalidate the whole distribution.
	if _, err := c.CreateInvalidation(context.Background(), "X", []string{"", "  ", "\t"}); err == nil {
		t.Error("all-blank paths: nil error, want a rejection (must not silently become /*)")
	}
}

func TestNormalizePaths(t *testing.T) {
	// Blank/whitespace-only entries are DROPPED (not turned into /*), so a
	// trailing comma in --paths can't accidentally invalidate everything.
	in := []string{"/a", "b", "  c  ", "", "  ", "/d/*"}
	want := []string{"/a", "/b", "/c", "/d/*"}
	got := NormalizePaths(in)
	if len(got) != len(want) {
		t.Fatalf("NormalizePaths(%v) = %v (len %d), want %v (len %d)", in, got, len(got), want, len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] %q, want %q", i, got[i], want[i])
		}
	}
	// A lone "/*" (the flag default) is preserved — explicit full
	// invalidation still works.
	if got := NormalizePaths([]string{"/*"}); len(got) != 1 || got[0] != "/*" {
		t.Errorf("NormalizePaths([/*]) = %v, want [/*]", got)
	}
}
