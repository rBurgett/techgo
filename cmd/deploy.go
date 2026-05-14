package cmd

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rBurgett/techgo/internal/awscf"
	"github.com/rBurgett/techgo/internal/awss3"
	"github.com/rBurgett/techgo/internal/awssig"
	"github.com/rBurgett/techgo/internal/config"
	"github.com/spf13/cobra"
)

// deployMarker is the small text object written at the root of the deploy
// target after every successful upload. The prune guard refuses to delete
// from any S3 prefix that isn't either empty or already carries this marker,
// so pointing --output at someone else's bucket can't wipe their stuff.
const deployMarker = ".techgo-deploy"

var (
	deployDryRun       bool
	deployNoInvalidate bool
	deployDelete       bool
	deployPathsFlag    string
)

var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Upload the built site to S3 and invalidate the CloudFront distribution",
	Long: `deploy mirrors the build output (default ./public) into the configured S3
bucket and then issues a CloudFront invalidation for the paths that changed.

It is a clean-mirror deploy by default: every local file is PUT (with the right
Content-Type and Cache-Control headers), and any object in the S3 prefix that
isn't part of the local build is removed. Before pruning, deploy checks the
target carries a .techgo-deploy marker (or is empty); if it doesn't, deploy
refuses — pointing --output at the wrong bucket can't wipe unrelated objects.

Reads AWS credentials and target info from <projectDir>/.env (AWS_ACCESS_KEY_ID,
AWS_SECRET_ACCESS_KEY, AWS_REGION, S3_BUCKET, CLOUDFRONT_DISTRIBUTION_ID, plus
optional AWS_SESSION_TOKEN, S3_PREFIX, AWS_ENDPOINT_URL_S3,
AWS_ENDPOINT_URL_CLOUDFRONT). Use --dry-run to see the PUT/DELETE plan without
hitting any write endpoints.`,
	Args: cobra.NoArgs,
	RunE: runDeploy,
}

func init() {
	deployCmd.Flags().BoolVar(&deployDryRun, "dry-run", false,
		"print the PUT/DELETE/invalidation plan without performing the writes")
	deployCmd.Flags().BoolVar(&deployNoInvalidate, "no-invalidate", false,
		"skip the CloudFront invalidation step")
	deployCmd.Flags().BoolVar(&deployDelete, "delete", true,
		"remove S3 objects that aren't in the local build (mirror)")
	deployCmd.Flags().StringVar(&deployPathsFlag, "paths", "/*",
		"comma-separated CloudFront invalidation paths")
	rootCmd.AddCommand(deployCmd)
}

func runDeploy(cmd *cobra.Command, _ []string) error {
	projectDir, err := ProjectDir()
	if err != nil {
		return err
	}
	outputDir, err := OutputDir()
	if err != nil {
		return err
	}

	site, err := config.LoadSite(projectDir)
	if err != nil {
		return err
	}
	rawEnv, err := config.ParseDotEnv(filepath.Join(projectDir, ".env"))
	if err != nil {
		return err
	}
	depCfg, err := parseDeployEnv(rawEnv)
	if err != nil {
		return err
	}

	files, err := collectDeployFiles(outputDir)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("nothing to deploy: %s is empty — run \"techgo build\" first", outputDir)
	}

	httpClient := &http.Client{Timeout: 5 * time.Minute}
	s3 := &awss3.Client{
		Bucket:   depCfg.bucket,
		Region:   depCfg.region,
		Prefix:   depCfg.prefix,
		Creds:    depCfg.creds,
		HTTP:     httpClient,
		Endpoint: depCfg.s3Endpoint,
	}
	cf := &awscf.Client{
		Creds:    depCfg.creds,
		HTTP:     httpClient,
		Endpoint: depCfg.cfEndpoint,
	}

	ctx := context.Background()
	w := cmd.OutOrStdout()

	// Prune guard: only relevant when --delete is on. Run it BEFORE any
	// uploads so a bad target fails fast. The guard's read-only S3 calls
	// happen even in dry-run, so the dry-run accurately reflects whether
	// the real deploy would be allowed to prune.
	pruneAllowed := false
	if deployDelete {
		ok, err := checkDeployPruneGuard(ctx, s3)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf(
				"refusing to prune s3://%s/%s — no %s marker and the target isn't empty; re-run with --delete=false, or remove unrelated objects first",
				depCfg.bucket, depCfg.prefix, deployMarker)
		}
		pruneAllowed = true
	}

	localKeys := make(map[string]bool, len(files))
	for _, f := range files {
		localKeys[f.key] = true
	}

	// Uploads.
	for _, f := range files {
		ct := contentTypeFor(f.key)
		cc := cacheControlFor(f.key)
		fmt.Fprintf(w, "PUT %s (%s, %s, %s)\n", f.key, humanBytes(f.size), ct, cc)
		if deployDryRun {
			continue
		}
		body, err := os.ReadFile(f.absPath)
		if err != nil {
			return fmt.Errorf("reading %s: %w", f.absPath, err)
		}
		if err := s3.PutObject(ctx, f.key, body, ct, cc); err != nil {
			return err
		}
	}

	// Write/refresh the deploy marker so prune is allowed on subsequent runs.
	if !deployDryRun {
		stamp := fmt.Sprintf("techgo deploy %s\n", time.Now().UTC().Format(time.RFC3339))
		if err := s3.PutObject(ctx, deployMarker, []byte(stamp), "text/plain; charset=utf-8", "no-cache"); err != nil {
			return fmt.Errorf("writing %s: %w", deployMarker, err)
		}
	}

	// Prune stale objects.
	if pruneAllowed {
		toDelete, err := stalePruneKeys(ctx, s3, localKeys)
		if err != nil {
			return err
		}
		for _, k := range toDelete {
			fmt.Fprintf(w, "DELETE %s\n", k)
			if deployDryRun {
				continue
			}
			if err := s3.DeleteObject(ctx, k); err != nil {
				return err
			}
		}
	}

	// CloudFront invalidation.
	switch {
	case deployNoInvalidate:
		fmt.Fprintln(w, "skipping CloudFront invalidation (--no-invalidate)")
	case deployDryRun:
		ps := awscf.NormalizePaths(strings.Split(deployPathsFlag, ","))
		fmt.Fprintf(w, "INVALIDATE distribution=%s paths=%s\n", depCfg.distID, strings.Join(ps, ","))
	default:
		ps := awscf.NormalizePaths(strings.Split(deployPathsFlag, ","))
		id, err := cf.CreateInvalidation(ctx, depCfg.distID, ps)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "CloudFront invalidation: %s (paths=%s)\n", id, strings.Join(ps, ","))
	}

	fmt.Fprintf(w, "Deployed → %s\n", site.BaseURL)
	return nil
}

// deployEnv is the parsed/validated deployment-time config from .env.
type deployEnv struct {
	creds      awssig.Credentials
	region     string
	bucket     string
	prefix     string // optional, defaults to ""
	distID     string
	s3Endpoint string // optional, for LocalStack / dev / tests
	cfEndpoint string // optional, for LocalStack / dev / tests
}

// parseDeployEnv extracts the deploy-time config from a parsed .env map. It
// errors with a precise message for any required key that's missing or blank,
// since the alternative would be a cryptic AWS-side error later on.
func parseDeployEnv(env map[string]string) (*deployEnv, error) {
	required := []string{
		"AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_REGION",
		"S3_BUCKET",
		"CLOUDFRONT_DISTRIBUTION_ID",
	}
	for _, k := range required {
		if strings.TrimSpace(env[k]) == "" {
			return nil, fmt.Errorf("missing %s in .env (required by `techgo deploy`)", k)
		}
	}
	return &deployEnv{
		creds: awssig.Credentials{
			AccessKeyID:     env["AWS_ACCESS_KEY_ID"],
			SecretAccessKey: env["AWS_SECRET_ACCESS_KEY"],
			SessionToken:    env["AWS_SESSION_TOKEN"],
		},
		region:     env["AWS_REGION"],
		bucket:     env["S3_BUCKET"],
		prefix:     env["S3_PREFIX"],
		distID:     env["CLOUDFRONT_DISTRIBUTION_ID"],
		s3Endpoint: env["AWS_ENDPOINT_URL_S3"],
		cfEndpoint: env["AWS_ENDPOINT_URL_CLOUDFRONT"],
	}, nil
}

// deployFile is one file we'll upload: the S3 key (forward-slash, relative to
// outputDir), the absolute filesystem path, and the file size.
type deployFile struct {
	key     string
	absPath string
	size    int64
}

// collectDeployFiles walks outputDir and returns every regular file as a
// deployFile, sorted by key for deterministic output. The Phase-5 .techgo-output
// build marker is skipped — it's local-only.
func collectDeployFiles(outputDir string) ([]deployFile, error) {
	var files []deployFile
	err := filepath.WalkDir(outputDir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(outputDir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ".techgo-output" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, deployFile{key: rel, absPath: p, size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].key < files[j].key })
	return files, nil
}

// checkDeployPruneGuard returns true iff it's safe to prune objects under the
// client's prefix: either the .techgo-deploy marker is present, or the prefix
// is empty (first deploy, nothing to prune). Any other state — non-empty
// without a marker — is refused.
func checkDeployPruneGuard(ctx context.Context, s3 *awss3.Client) (bool, error) {
	_, found, err := s3.GetObject(ctx, deployMarker)
	if err != nil {
		return false, fmt.Errorf("checking %s marker: %w", deployMarker, err)
	}
	if found {
		return true, nil
	}
	keys, err := s3.ListObjects(ctx)
	if err != nil {
		return false, fmt.Errorf("listing target prefix: %w", err)
	}
	return len(keys) == 0, nil
}

// stalePruneKeys lists the deploy target and returns every key that is in
// S3 but not in localKeys — the set we'd delete to mirror local into S3.
// The deploy marker itself is always preserved.
func stalePruneKeys(ctx context.Context, s3 *awss3.Client, localKeys map[string]bool) ([]string, error) {
	existing, err := s3.ListObjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing target prefix: %w", err)
	}
	var out []string
	for _, k := range existing {
		if k == deployMarker || localKeys[k] {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// contentTypeFor returns the Content-Type to set on the S3 PUT for a given
// build-relative key, using an explicit mapping for the extensions techgo
// produces. This is more reliable than mime.TypeByExtension alone — the
// stdlib map varies by platform (e.g. .rss isn't always there, .webmanifest
// isn't there at all).
func contentTypeFor(key string) string {
	switch strings.ToLower(filepath.Ext(key)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "text/javascript"
	case ".rss":
		return "application/rss+xml"
	case ".mp3":
		return "audio/mpeg"
	case ".mp4":
		return "video/mp4"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".ico":
		return "image/x-icon"
	case ".svg":
		return "image/svg+xml"
	case ".json":
		return "application/json"
	case ".xml":
		return "application/xml"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".webmanifest":
		return "application/manifest+json"
	}
	return "application/octet-stream"
}

// cacheControlFor returns the Cache-Control to set on the S3 PUT for a given
// build-relative key. Tuned to make CloudFront's edge bear the load while
// keeping browsers honest:
//
//   - HTML pages and the not-found page: max-age=0 + must-revalidate so
//     content updates land immediately after the post-deploy invalidation.
//   - feed.rss: 5 minutes — long enough to amortize a podcast app's polls,
//     short enough that a published episode shows up promptly.
//   - media/*.mp3, media/*.mp4: 1 day + must-revalidate, NOT immutable. An
//     episode can be re-transcoded after publishing; its filename stays
//     number-keyed, so immutable would strand the old bytes in browsers even
//     after a CloudFront /* invalidation. Switch to immutable only after
//     content-hashing media filenames.
//   - sitemap.xml: 1 hour — moderate, since crawlers cache it.
//   - everything else (css, favicons, og-image, cover.png, webmanifest,
//     robots.txt): 1 day. The post-deploy /* invalidation purges the edge
//     regardless.
func cacheControlFor(key string) string {
	switch {
	case strings.HasSuffix(key, ".html"):
		return "public, max-age=0, must-revalidate"
	case key == "feed.rss":
		return "public, max-age=300, must-revalidate"
	case strings.HasPrefix(key, "media/") &&
		(strings.HasSuffix(key, ".mp3") || strings.HasSuffix(key, ".mp4")):
		return "public, max-age=86400, must-revalidate"
	case key == "sitemap.xml":
		return "public, max-age=3600"
	default:
		return "public, max-age=86400"
	}
}
