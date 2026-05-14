package cmd

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"

	"github.com/spf13/cobra"
)

var serveAddr string

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve the built site locally for preview",
	Long: `serve runs a static file server over the build output directory (default
./public) so you can preview the site before deploying — run "techgo build"
first. Like CloudFront in production, "/" yields index.html and a missing path
renders 404.html with a 404 status; episode pages are linked with their .html
extension, so they resolve without any directory-index handling.`,
	Args: cobra.NoArgs,
	RunE: runServe,
}

func init() {
	serveCmd.Flags().StringVar(&serveAddr, "addr", ":8080",
		"address to listen on")
	rootCmd.AddCommand(serveCmd)
}

func runServe(cmd *cobra.Command, _ []string) error {
	outputDir, err := OutputDir()
	if err != nil {
		return err
	}
	info, err := os.Stat(outputDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s does not exist — run \"techgo build\" first", outputDir)
		}
		return fmt.Errorf("checking %s: %w", outputDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", outputDir)
	}

	ln, err := net.Listen("tcp", serveAddr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", serveAddr, err)
	}

	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "serving %s at %s  (Ctrl+C to stop)\n", outputDir, listenURL(ln.Addr()))
	return http.Serve(ln, siteHandler(outputDir))
}

// listenURL turns a listener's resolved address into the URL to open in a
// browser: an explicitly bound interface is shown as-is, while a wildcard bind
// (":8080", "0.0.0.0:…", "[::]:…") — which includes the loopback — is shown as
// localhost so the printed link is always reachable.
func listenURL(addr net.Addr) string {
	ta, ok := addr.(*net.TCPAddr)
	if !ok {
		return "http://" + addr.String() + "/"
	}
	host := "localhost"
	if len(ta.IP) > 0 && !ta.IP.IsUnspecified() {
		host = ta.IP.String()
		if ta.IP.To4() == nil {
			host = "[" + host + "]" // bracket IPv6 literals
		}
	}
	return fmt.Sprintf("http://%s:%d/", host, ta.Port)
}

// siteHandler serves the built site from dir as http.FileServer would, but with
// two production-matching tweaks: (a) a directory without an index.html (e.g.
// /media/, /episodes/, /css/) is treated as not found instead of rendering Go's
// built-in directory listing — CloudFront 403/404s those — and (b) any missing
// path falls through to dir/404.html with a 404 status, mirroring the
// CloudFront custom-error mapping used in production.
func siteHandler(dir string) http.Handler {
	files := http.FileServer(noListDir{http.Dir(dir)})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &notFoundCatcher{ResponseWriter: w}
		files.ServeHTTP(rec, r)
		if !rec.notFound {
			return
		}
		body, err := os.ReadFile(filepath.Join(dir, "404.html"))
		if err != nil { // build is incomplete — still answer with a plain 404
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "404 — not found\n")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write(body)
	})
}

// notFoundCatcher wraps an http.ResponseWriter and swallows a 404 response (its
// status line and body) so the caller can render a custom 404 page instead. Any
// other status — 200, a 301 redirect, 304 — passes straight through.
type notFoundCatcher struct {
	http.ResponseWriter
	notFound    bool
	wroteHeader bool
}

func (n *notFoundCatcher) WriteHeader(code int) {
	if n.wroteHeader {
		return
	}
	n.wroteHeader = true
	if code == http.StatusNotFound {
		n.notFound = true
		return
	}
	n.ResponseWriter.WriteHeader(code)
}

func (n *notFoundCatcher) Write(b []byte) (int, error) {
	if !n.wroteHeader {
		n.WriteHeader(http.StatusOK)
	}
	if n.notFound {
		return len(b), nil // discard the file server's default 404 body
	}
	return n.ResponseWriter.Write(b)
}

// noListDir wraps an http.FileSystem so http.FileServer never renders a
// directory listing: opening a directory that lacks index.html returns
// fs.ErrNotExist, which the file server translates into a 404 (in turn caught
// by notFoundCatcher and turned into 404.html). The site root still works
// because it does have index.html.
type noListDir struct{ fs http.FileSystem }

func (d noListDir) Open(name string) (http.File, error) {
	f, err := d.fs.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.IsDir() {
		idx, ierr := d.fs.Open(path.Join(name, "index.html"))
		if ierr != nil {
			f.Close()
			return nil, os.ErrNotExist
		}
		idx.Close()
	}
	return f, nil
}
