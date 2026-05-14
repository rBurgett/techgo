package cmd

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// -----------------------------------------------------------------------------
// Unit tests for the pure helpers.
// -----------------------------------------------------------------------------

func TestContentTypeFor(t *testing.T) {
	cases := map[string]string{
		"index.html":       "text/html; charset=utf-8",
		"404.html":         "text/html; charset=utf-8",
		"css/style.css":    "text/css; charset=utf-8",
		"app.js":           "text/javascript",
		"feed.rss":         "application/rss+xml",
		"media/0001.mp3":   "audio/mpeg",
		"media/0001.mp4":   "video/mp4",
		"cover.png":        "image/png",
		"photo.jpg":        "image/jpeg",
		"photo.JPEG":       "image/jpeg",
		"favicon.ico":      "image/x-icon",
		"icon.svg":         "image/svg+xml",
		"data.json":        "application/json",
		"sitemap.xml":      "application/xml",
		"robots.txt":       "text/plain; charset=utf-8",
		"site.webmanifest": "application/manifest+json",
		"unknown.bin":      "application/octet-stream",
		"NO_EXTENSION":     "application/octet-stream",
	}
	for key, want := range cases {
		if got := contentTypeFor(key); got != want {
			t.Errorf("contentTypeFor(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestCacheControlFor(t *testing.T) {
	cases := map[string]string{
		"index.html":          "public, max-age=0, must-revalidate",
		"404.html":            "public, max-age=0, must-revalidate",
		"episodes/hello.html": "public, max-age=0, must-revalidate",
		"feed.rss":            "public, max-age=300, must-revalidate",
		"media/0001.mp3":      "public, max-age=86400, must-revalidate",
		"media/0001.mp4":      "public, max-age=86400, must-revalidate",
		"sitemap.xml":         "public, max-age=3600",
		"css/style.css":       "public, max-age=86400",
		"favicon.ico":         "public, max-age=86400",
		"og-image.png":        "public, max-age=86400",
		"cover.png":           "public, max-age=86400",
		"site.webmanifest":    "public, max-age=86400",
		"robots.txt":          "public, max-age=86400",
	}
	for key, want := range cases {
		if got := cacheControlFor(key); got != want {
			t.Errorf("cacheControlFor(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestCacheControlForMediaIsNotImmutable(t *testing.T) {
	// Regression: a re-transcoded episode keeps its number-keyed filename,
	// so an `immutable` Cache-Control would strand the old bytes in browsers
	// even after a CloudFront invalidation. Refuse to silently regress this.
	for _, k := range []string{"media/0001.mp3", "media/0042.mp4"} {
		if cc := cacheControlFor(k); strings.Contains(cc, "immutable") {
			t.Errorf("cacheControlFor(%q) = %q; media must not be immutable until filenames are content-hashed", k, cc)
		}
	}
}

func TestParseDeployEnv(t *testing.T) {
	good := map[string]string{
		"AWS_ACCESS_KEY_ID":           "AKID",
		"AWS_SECRET_ACCESS_KEY":       "SECRET",
		"AWS_REGION":                  "us-east-1",
		"S3_BUCKET":                   "bk",
		"CLOUDFRONT_DISTRIBUTION_ID":  "EDIST",
		"AWS_SESSION_TOKEN":           "TOK",
		"S3_PREFIX":                   "site",
		"AWS_ENDPOINT_URL_S3":         "https://s3.local",
		"AWS_ENDPOINT_URL_CLOUDFRONT": "https://cf.local",
	}
	cfg, err := parseDeployEnv(good)
	if err != nil {
		t.Fatalf("parseDeployEnv(good): %v", err)
	}
	if cfg.creds.AccessKeyID != "AKID" || cfg.creds.SecretAccessKey != "SECRET" || cfg.creds.SessionToken != "TOK" {
		t.Errorf("creds = %+v", cfg.creds)
	}
	if cfg.region != "us-east-1" || cfg.bucket != "bk" || cfg.prefix != "site" || cfg.distID != "EDIST" {
		t.Errorf("cfg = %+v", cfg)
	}
	if cfg.s3Endpoint != "https://s3.local" || cfg.cfEndpoint != "https://cf.local" {
		t.Errorf("endpoint overrides = (%q, %q)", cfg.s3Endpoint, cfg.cfEndpoint)
	}

	for _, missing := range []string{
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_REGION", "S3_BUCKET", "CLOUDFRONT_DISTRIBUTION_ID",
	} {
		broken := cloneMap(good)
		delete(broken, missing)
		if _, err := parseDeployEnv(broken); err == nil {
			t.Errorf("parseDeployEnv missing %s: nil error, want a rejection", missing)
		} else if !strings.Contains(err.Error(), missing) {
			t.Errorf("error should name %q, got: %v", missing, err)
		}
	}
}

func cloneMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func TestCollectDeployFilesSkipsBuildMarker(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "index.html"), "<h1>x</h1>")
	mustWriteFile(t, filepath.Join(dir, ".techgo-output"), "marker") // build-time marker
	mustWriteFile(t, filepath.Join(dir, "css", "style.css"), "body{}")
	mustWriteFile(t, filepath.Join(dir, "media", "0001.mp3"), "fake-mp3-bytes")

	files, err := collectDeployFiles(dir)
	if err != nil {
		t.Fatalf("collectDeployFiles: %v", err)
	}
	keys := make([]string, len(files))
	for i, f := range files {
		keys[i] = f.key
	}
	want := []string{"css/style.css", "index.html", "media/0001.mp3"}
	if fmt.Sprintf("%v", keys) != fmt.Sprintf("%v", want) {
		t.Errorf("keys = %v, want %v (.techgo-output skipped, sorted)", keys, want)
	}
}

// -----------------------------------------------------------------------------
// Integration: drive the full `techgo deploy` command against a fake S3 +
// CloudFront pair on httptest.Server.
// -----------------------------------------------------------------------------

// fakeS3 is a tiny in-memory S3 substitute scoped at the bucket root: PUT
// stores, GET reads, DELETE removes, GET ?list-type=2 returns the keys as a
// ListBucketResult. Just enough surface to drive runDeploy.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	reqs    []recordedReq
}

type recordedReq struct {
	method string
	path   string
	query  string
	body   []byte
	hdr    http.Header
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: map[string][]byte{}}
}

func (s *fakeS3) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, recordedReq{
			method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
			body: append([]byte(nil), body...), hdr: r.Header.Clone(),
		})
		s.mu.Unlock()

		key := strings.TrimPrefix(r.URL.Path, "/")
		switch r.Method {
		case http.MethodPut:
			s.mu.Lock()
			s.objects[key] = body
			s.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			if r.URL.Query().Get("list-type") == "2" {
				prefix := r.URL.Query().Get("prefix")
				s.mu.Lock()
				keys := make([]string, 0, len(s.objects))
				for k := range s.objects {
					if prefix == "" || strings.HasPrefix(k, prefix) {
						keys = append(keys, k)
					}
				}
				s.mu.Unlock()
				sort.Strings(keys)
				var b strings.Builder
				b.WriteString(`<?xml version="1.0"?><ListBucketResult><IsTruncated>false</IsTruncated>`)
				for _, k := range keys {
					fmt.Fprintf(&b, `<Contents><Key>%s</Key></Contents>`, k)
				}
				b.WriteString(`</ListBucketResult>`)
				w.Header().Set("Content-Type", "application/xml")
				_, _ = io.WriteString(w, b.String())
				return
			}
			s.mu.Lock()
			body, ok := s.objects[key]
			s.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(body)
		case http.MethodDelete:
			s.mu.Lock()
			delete(s.objects, key)
			s.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("fakeS3: unexpected method %s %s", r.Method, r.URL)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// fakeCF answers CreateInvalidation with a canned <Invalidation><Id>...</Id></Invalidation>
// and records every request so the test can assert on them.
type fakeCF struct {
	mu      sync.Mutex
	reqs    []recordedReq
	counter int
}

func newFakeCF() *fakeCF { return &fakeCF{} }

func (c *fakeCF) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.counter++
		id := fmt.Sprintf("I%07d", c.counter)
		c.reqs = append(c.reqs, recordedReq{
			method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
			body: append([]byte(nil), body...), hdr: r.Header.Clone(),
		})
		c.mu.Unlock()

		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/invalidation") {
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `<Invalidation><Id>%s</Id><Status>InProgress</Status></Invalidation>`, id)
			return
		}
		t.Errorf("fakeCF: unexpected %s %s", r.Method, r.URL)
		http.Error(w, "not implemented", http.StatusNotImplemented)
	}
}

// setupDeployProject creates a temp project with the minimum site.yml, a
// build output dir containing a representative file mix, and an .env that
// points at the fake S3/CF servers. Returns the project dir and the output
// (build) dir.
func setupDeployProject(t *testing.T, s3URL, cfURL string) (projectDir, outputDir string) {
	t.Helper()
	projectDir = t.TempDir()
	mustWriteFile(t, filepath.Join(projectDir, "site.yml"),
		"title: \"T\"\nauthor: \"a\"\nownerEmail: \"a@b\"\ncategory: \"Technology\"\n"+
			"baseURL: \"https://example.com\"\n")
	mustWriteFile(t, filepath.Join(projectDir, ".env"),
		"AWS_ACCESS_KEY_ID=AKIDEXAMPLE\n"+
			"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY\n"+
			"AWS_REGION=us-east-1\n"+
			"S3_BUCKET=techgo-site\n"+
			"CLOUDFRONT_DISTRIBUTION_ID=EDIST\n"+
			fmt.Sprintf("AWS_ENDPOINT_URL_S3=%s\n", s3URL)+
			fmt.Sprintf("AWS_ENDPOINT_URL_CLOUDFRONT=%s\n", cfURL))

	outputDir = filepath.Join(projectDir, "public")
	// A representative file mix exercising every Content-Type / Cache-Control
	// branch.
	mustWriteFile(t, filepath.Join(outputDir, ".techgo-output"), "marker") // must be skipped
	mustWriteFile(t, filepath.Join(outputDir, "index.html"), "<h1>home</h1>")
	mustWriteFile(t, filepath.Join(outputDir, "404.html"), "<h1>404</h1>")
	mustWriteFile(t, filepath.Join(outputDir, "about.html"), "<h1>about</h1>")
	mustWriteFile(t, filepath.Join(outputDir, "feed.rss"), "<rss/>")
	mustWriteFile(t, filepath.Join(outputDir, "sitemap.xml"), "<urlset/>")
	mustWriteFile(t, filepath.Join(outputDir, "robots.txt"), "User-agent: *\n")
	mustWriteFile(t, filepath.Join(outputDir, "css", "style.css"), "body{}")
	mustWriteFile(t, filepath.Join(outputDir, "media", "0001.mp3"), "fake-mp3-bytes")
	mustWriteFile(t, filepath.Join(outputDir, "media", "0001.mp4"), "fake-mp4-bytes")
	mustWriteFile(t, filepath.Join(outputDir, "episodes", "hello.html"), "<h1>ep</h1>")
	return projectDir, outputDir
}

func runDeployCmd(t *testing.T, projectDir, outputDir string, extra ...string) (string, error) {
	t.Helper()
	t.Cleanup(func() {
		projectFlag = "."
		outputFlag = "public"
		deployDryRun = false
		deployNoInvalidate = false
		deployDelete = true
		deployPathsFlag = "/*"
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	args := append([]string{"deploy", "--project", projectDir, "--output", outputDir}, extra...)
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	return buf.String(), err
}

func TestDeployCommandDryRunPrintsPlanWithoutWriting(t *testing.T) {
	s3 := newFakeS3()
	cf := newFakeCF()
	s3srv := httptest.NewServer(s3.handler(t))
	t.Cleanup(s3srv.Close)
	cfsrv := httptest.NewServer(cf.handler(t))
	t.Cleanup(cfsrv.Close)

	projectDir, outputDir := setupDeployProject(t, s3srv.URL, cfsrv.URL)

	out, err := runDeployCmd(t, projectDir, outputDir, "--dry-run")
	if err != nil {
		t.Fatalf("deploy --dry-run: %v\n%s", err, out)
	}

	// Dry-run output mentions every local key.
	for _, key := range []string{
		"index.html", "404.html", "about.html", "feed.rss", "sitemap.xml", "robots.txt",
		"css/style.css", "media/0001.mp3", "media/0001.mp4", "episodes/hello.html",
	} {
		if !strings.Contains(out, "PUT "+key+" ") {
			t.Errorf("dry-run output missing PUT for %q:\n%s", key, out)
		}
	}
	// The build-only marker must NOT show up as a PUT.
	if strings.Contains(out, "PUT .techgo-output") {
		t.Error("dry-run printed PUT for .techgo-output (should be skipped)")
	}
	// And the invalidation is announced but not actually issued.
	if !strings.Contains(out, "INVALIDATE distribution=EDIST paths=/*") {
		t.Errorf("dry-run output missing INVALIDATE summary:\n%s", out)
	}
	if !strings.Contains(out, "Deployed → https://example.com") {
		t.Errorf("missing final 'Deployed →' line:\n%s", out)
	}

	// CRITICAL: dry-run must not have touched any S3 write path or the CF API.
	s3.mu.Lock()
	defer s3.mu.Unlock()
	for _, req := range s3.reqs {
		if req.method == http.MethodPut || req.method == http.MethodDelete {
			t.Errorf("dry-run issued a write request: %s %s", req.method, req.path)
		}
	}
	cf.mu.Lock()
	defer cf.mu.Unlock()
	if len(cf.reqs) != 0 {
		t.Errorf("dry-run hit the CloudFront API (%d times); want 0", len(cf.reqs))
	}
}

func TestDeployCommandFullRunUploadsMarkerInvalidates(t *testing.T) {
	s3 := newFakeS3()
	cf := newFakeCF()
	s3srv := httptest.NewServer(s3.handler(t))
	t.Cleanup(s3srv.Close)
	cfsrv := httptest.NewServer(cf.handler(t))
	t.Cleanup(cfsrv.Close)

	projectDir, outputDir := setupDeployProject(t, s3srv.URL, cfsrv.URL)

	out, err := runDeployCmd(t, projectDir, outputDir)
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}

	s3.mu.Lock()
	defer s3.mu.Unlock()

	// Every local file landed in S3.
	for _, k := range []string{
		"index.html", "404.html", "about.html", "feed.rss", "sitemap.xml", "robots.txt",
		"css/style.css", "media/0001.mp3", "media/0001.mp4", "episodes/hello.html",
	} {
		if _, ok := s3.objects[k]; !ok {
			t.Errorf("object %q not uploaded", k)
		}
	}
	if _, ok := s3.objects[".techgo-output"]; ok {
		t.Error(".techgo-output should not have been uploaded")
	}
	// The deploy marker must exist with no-cache.
	if _, ok := s3.objects[".techgo-deploy"]; !ok {
		t.Error(".techgo-deploy marker not written")
	}

	// Spot-check that PUT requests carry the expected headers.
	headersByKey := map[string]http.Header{}
	for _, r := range s3.reqs {
		if r.method != http.MethodPut {
			continue
		}
		headersByKey[strings.TrimPrefix(r.path, "/")] = r.hdr
	}
	if ct := headersByKey["index.html"].Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("index.html Content-Type = %q", ct)
	}
	if cc := headersByKey["index.html"].Get("Cache-Control"); cc != "public, max-age=0, must-revalidate" {
		t.Errorf("index.html Cache-Control = %q", cc)
	}
	if ct := headersByKey["media/0001.mp3"].Get("Content-Type"); ct != "audio/mpeg" {
		t.Errorf("media/0001.mp3 Content-Type = %q", ct)
	}
	if cc := headersByKey["media/0001.mp3"].Get("Cache-Control"); cc != "public, max-age=86400, must-revalidate" {
		t.Errorf("media/0001.mp3 Cache-Control = %q", cc)
	}
	if cc := headersByKey[".techgo-deploy"].Get("Cache-Control"); cc != "no-cache" {
		t.Errorf(".techgo-deploy Cache-Control = %q, want no-cache", cc)
	}

	// CloudFront saw exactly one invalidation, and the deploy command
	// reported the returned Id.
	cf.mu.Lock()
	defer cf.mu.Unlock()
	if len(cf.reqs) != 1 {
		t.Fatalf("CloudFront calls = %d, want 1", len(cf.reqs))
	}
	if !strings.Contains(string(cf.reqs[0].body), "<Path>/*</Path>") {
		t.Errorf("invalidation body missing /* path: %s", cf.reqs[0].body)
	}
	if !strings.Contains(out, "CloudFront invalidation: I0000001 (paths=/*)") {
		t.Errorf("deploy didn't report the returned invalidation Id:\n%s", out)
	}
}

func TestDeployCommandPrunesStaleObjects(t *testing.T) {
	s3 := newFakeS3()
	cf := newFakeCF()
	s3srv := httptest.NewServer(s3.handler(t))
	t.Cleanup(s3srv.Close)
	cfsrv := httptest.NewServer(cf.handler(t))
	t.Cleanup(cfsrv.Close)

	// Seed the "bucket" with a prior-deploy marker plus a stale object that
	// no longer corresponds to anything local.
	s3.objects[".techgo-deploy"] = []byte("prior")
	s3.objects["episodes/old.html"] = []byte("old")
	s3.objects["index.html"] = []byte("old")

	projectDir, outputDir := setupDeployProject(t, s3srv.URL, cfsrv.URL)

	out, err := runDeployCmd(t, projectDir, outputDir)
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}

	// episodes/old.html should be gone; index.html should be the new bytes.
	s3.mu.Lock()
	defer s3.mu.Unlock()
	if _, ok := s3.objects["episodes/old.html"]; ok {
		t.Error("stale object episodes/old.html was not pruned")
	}
	if string(s3.objects["index.html"]) != "<h1>home</h1>" {
		t.Errorf("index.html bytes = %q, want the new build", s3.objects["index.html"])
	}
	if !strings.Contains(out, "DELETE episodes/old.html") {
		t.Errorf("deploy output missing DELETE line:\n%s", out)
	}
}

func TestDeployCommandRefusesPruneWithoutMarker(t *testing.T) {
	s3 := newFakeS3()
	cf := newFakeCF()
	s3srv := httptest.NewServer(s3.handler(t))
	t.Cleanup(s3srv.Close)
	cfsrv := httptest.NewServer(cf.handler(t))
	t.Cleanup(cfsrv.Close)

	// Bucket has stuff but no .techgo-deploy marker — refuse to prune.
	s3.objects["someone-elses-site/index.html"] = []byte("not ours")

	projectDir, outputDir := setupDeployProject(t, s3srv.URL, cfsrv.URL)

	out, err := runDeployCmd(t, projectDir, outputDir)
	if err == nil {
		t.Fatalf("expected an error, got nil. output:\n%s", out)
	}
	if !strings.Contains(err.Error(), "refusing to prune") || !strings.Contains(err.Error(), ".techgo-deploy") {
		t.Errorf("error should mention 'refusing to prune' and the marker, got: %v", err)
	}

	// No uploads should have happened — the guard runs BEFORE PUTs.
	s3.mu.Lock()
	defer s3.mu.Unlock()
	for _, r := range s3.reqs {
		if r.method == http.MethodPut {
			t.Errorf("guard failed to fail-fast: PUT %s happened anyway", r.path)
		}
	}
}

func TestDeployCommandAllowsFirstDeployToEmptyBucket(t *testing.T) {
	// No marker, no objects — the empty-prefix carve-out lets the first
	// deploy through.
	s3 := newFakeS3()
	cf := newFakeCF()
	s3srv := httptest.NewServer(s3.handler(t))
	t.Cleanup(s3srv.Close)
	cfsrv := httptest.NewServer(cf.handler(t))
	t.Cleanup(cfsrv.Close)

	projectDir, outputDir := setupDeployProject(t, s3srv.URL, cfsrv.URL)
	out, err := runDeployCmd(t, projectDir, outputDir)
	if err != nil {
		t.Fatalf("first deploy to empty bucket should succeed: %v\n%s", err, out)
	}
	if _, ok := s3.objects[".techgo-deploy"]; !ok {
		t.Error("first deploy didn't write the .techgo-deploy marker")
	}
}

func TestDeployCommandWithDeleteFalseSkipsGuardAndPrune(t *testing.T) {
	s3 := newFakeS3()
	cf := newFakeCF()
	s3srv := httptest.NewServer(s3.handler(t))
	t.Cleanup(s3srv.Close)
	cfsrv := httptest.NewServer(cf.handler(t))
	t.Cleanup(cfsrv.Close)

	// Bucket has unrelated stuff but no marker — would refuse with --delete
	// (default). With --delete=false the guard is skipped and uploads still
	// happen; nothing gets pruned.
	s3.objects["someone-elses-site/index.html"] = []byte("not ours")

	projectDir, outputDir := setupDeployProject(t, s3srv.URL, cfsrv.URL)
	out, err := runDeployCmd(t, projectDir, outputDir, "--delete=false")
	if err != nil {
		t.Fatalf("deploy --delete=false: %v\n%s", err, out)
	}
	if _, ok := s3.objects["someone-elses-site/index.html"]; !ok {
		t.Error("unrelated object got deleted even with --delete=false")
	}
	if _, ok := s3.objects["index.html"]; !ok {
		t.Error("uploads should still happen with --delete=false")
	}
	if strings.Contains(out, "DELETE ") {
		t.Errorf("--delete=false should not print any DELETE lines:\n%s", out)
	}
}

func TestDeployCommandNoInvalidateSkipsCloudFront(t *testing.T) {
	s3 := newFakeS3()
	cf := newFakeCF()
	s3srv := httptest.NewServer(s3.handler(t))
	t.Cleanup(s3srv.Close)
	cfsrv := httptest.NewServer(cf.handler(t))
	t.Cleanup(cfsrv.Close)

	projectDir, outputDir := setupDeployProject(t, s3srv.URL, cfsrv.URL)
	out, err := runDeployCmd(t, projectDir, outputDir, "--no-invalidate")
	if err != nil {
		t.Fatalf("deploy --no-invalidate: %v\n%s", err, out)
	}
	if !strings.Contains(out, "skipping CloudFront invalidation") {
		t.Errorf("missing 'skipping CloudFront invalidation' line:\n%s", out)
	}
	cf.mu.Lock()
	defer cf.mu.Unlock()
	if len(cf.reqs) != 0 {
		t.Errorf("CloudFront was called despite --no-invalidate (%d times)", len(cf.reqs))
	}
}

func TestDeployCommandWithPrefix(t *testing.T) {
	// Add an S3_PREFIX to .env: every PUT/GET/DELETE must arrive under that
	// prefix, and the marker check happens under the prefix too.
	s3 := newFakeS3()
	cf := newFakeCF()
	s3srv := httptest.NewServer(s3.handler(t))
	t.Cleanup(s3srv.Close)
	cfsrv := httptest.NewServer(cf.handler(t))
	t.Cleanup(cfsrv.Close)

	projectDir := t.TempDir()
	mustWriteFile(t, filepath.Join(projectDir, "site.yml"),
		"title: \"T\"\nauthor: \"a\"\nownerEmail: \"a@b\"\ncategory: \"Technology\"\n"+
			"baseURL: \"https://example.com\"\n")
	mustWriteFile(t, filepath.Join(projectDir, ".env"),
		"AWS_ACCESS_KEY_ID=AKID\nAWS_SECRET_ACCESS_KEY=SECRET\nAWS_REGION=us-east-1\n"+
			"S3_BUCKET=bk\nS3_PREFIX=site/sub\nCLOUDFRONT_DISTRIBUTION_ID=EDIST\n"+
			"AWS_ENDPOINT_URL_S3="+s3srv.URL+"\nAWS_ENDPOINT_URL_CLOUDFRONT="+cfsrv.URL+"\n")
	outputDir := filepath.Join(projectDir, "public")
	mustWriteFile(t, filepath.Join(outputDir, "index.html"), "<h1>x</h1>")

	if _, err := runDeployCmd(t, projectDir, outputDir); err != nil {
		t.Fatalf("deploy with prefix: %v", err)
	}
	if _, ok := s3.objects["site/sub/index.html"]; !ok {
		t.Errorf("upload missed prefix; objects = %v", keysOf(s3.objects))
	}
	if _, ok := s3.objects["site/sub/.techgo-deploy"]; !ok {
		t.Error("marker missed prefix")
	}
}

func TestDeployCommandRejectsMissingEnv(t *testing.T) {
	projectDir := t.TempDir()
	mustWriteFile(t, filepath.Join(projectDir, "site.yml"),
		"title: \"T\"\nauthor: \"a\"\nownerEmail: \"a@b\"\ncategory: \"Technology\"\n"+
			"baseURL: \"https://example.com\"\n")
	mustWriteFile(t, filepath.Join(projectDir, ".env"), "AWS_ACCESS_KEY_ID=AKID\n")
	outputDir := filepath.Join(projectDir, "public")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := runDeployCmd(t, projectDir, outputDir)
	if err == nil {
		t.Fatalf("expected an error, got nil. output:\n%s", out)
	}
	if !strings.Contains(err.Error(), "AWS_SECRET_ACCESS_KEY") {
		t.Errorf("error should name the missing env var, got: %v", err)
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
