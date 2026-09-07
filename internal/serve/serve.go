// Package serve is the static file server behind `frznforge dev`.
//
// One server, two callers. Before 0.4.0 the built site was served two different ways — `astro
// preview` for a person and tests/e2e/serve.ts for Playwright — and the two could disagree
// about what they were handing back (they once did, over whether a `.ps1` note file was
// text/plain or a download). A dev server that is not the server the tests exercise can be
// wrong in exactly the places nobody looks, so both are this code now.
//
// What it has to get right is the output contract in internal/build's package comment, read
// from the serving side:
//
//	/                             → index.html
//	/repos/alpha/                 → repos/alpha/index.html   (a directory URL is its index)
//	/repos/a/raw/main/LICENSE     → that file, verbatim, typed as text
//	anything missing              → 404.html, with status 404
//
// Nothing here rebuilds, watches, minifies or rewrites: the bytes on disk are the bytes the
// browser gets. That is the whole promise `frznforge dev` makes, and the notice it prints
// (notice.go) exists so nobody has to discover it the hard way.
package serve

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Options configure one server.
type Options struct {
	// Dir is the directory to serve — the output of a build.
	Dir string
	// Base is the deploy base prefix, e.g. "/mysite". Empty serves at the root. When it is set,
	// requests outside the prefix 404 exactly as they would on a real sub-path deploy, which is
	// what makes a root-absolute link that escaped the build fail loudly here.
	Base string
	// Host is the interface to bind. Empty means localhost — a dev server is not a web server,
	// so it does not go on 0.0.0.0 unless it is asked to.
	Host string
	// Port is the TCP port. 0 lets the OS pick a free one; the caller then reads Server.URL,
	// which is how the e2e harness discovers where to point Playwright.
	Port int
}

// Server is a bound listener plus the handler over it. Listen binds so the URL is known before
// anything is printed, and Serve then blocks.
type Server struct {
	// URL is where the site is reachable, deploy base included and with a trailing slash.
	URL string
	// Dir is the absolute directory being served.
	Dir string

	ln  net.Listener
	srv *http.Server
}

// Listen binds the port and prepares the handler, without serving yet.
//
// Binding first is what makes port 0 usable: the chosen port has to be printable before the
// process disappears into Serve, or nothing can discover it.
func Listen(opts Options) (*Server, error) {
	h, err := newHandler(opts.Dir, opts.Base)
	if err != nil {
		return nil, err
	}
	host := opts.Host
	if host == "" {
		host = "localhost"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(opts.Port)))
	if err != nil {
		return nil, fmt.Errorf("cannot listen on %s port %d: %w\n  Something else is probably on that port — pass --port=<n>, or --port=0 to let the OS pick a free one",
			host, opts.Port, err)
	}
	port := opts.Port
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		port = tcp.Port
	}
	srv := &Server{
		URL: "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + h.base + "/",
		Dir: h.root,
		ln:  ln,
		srv: &http.Server{Handler: h},
	}
	slog.Debug("serve listening", "url", srv.URL, "dir", srv.Dir, "base", h.base)
	return srv, nil
}

// Serve accepts requests until Close is called. A closed server is a clean exit, not an error:
// Ctrl-C is the normal way this command ends.
func (s *Server) Serve() error {
	err := s.srv.Serve(s.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops the server and releases the port.
//
// Both, deliberately. http.Server.Close only closes listeners that Serve registered, and Listen
// creates ours before Serve is ever called — so on a Listen-then-bail path (a preflight failing
// after the bind, a caller that decides not to serve) srv.Close alone leaves the socket bound,
// and the next run fails with "only one usage of each socket address". Closing an already-closed
// listener returns an error rather than panicking, and that error is the uninteresting one.
func (s *Server) Close() error {
	slog.Debug("serve closing", "url", s.URL)
	err := s.srv.Close()
	if s.ln != nil {
		if lnErr := s.ln.Close(); err == nil && !errors.Is(lnErr, net.ErrClosed) {
			err = lnErr
		}
	}
	return err
}

/* ---- the handler --------------------------------------------------------- */

type handler struct {
	// root is absolute and cleaned; every resolved path is checked to be inside it.
	root string
	// base is "" or "/prefix" — normalised by NewHandler.
	base string
}

// NewHandler builds the file handler for dir, mounted under base ("" for a root deploy).
func NewHandler(dir, base string) (http.Handler, error) { return newHandler(dir, base) }

// newHandler is the same constructor typed concretely. Listen needs the resolved root and base
// back out of it to print the URL and the directory, and an http.Handler cannot be asked.
func newHandler(dir, base string) (*handler, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", dir, err)
	}
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base != "" && !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	return &handler{root: filepath.Clean(root), base: base}, nil
}

// ServeHTTP answers one request.
//
// Every request leaves one debug record carrying the status it was answered with. The status is
// threaded back out of the helpers rather than captured by wrapping the ResponseWriter: a
// wrapper would hide net/http's io.ReaderFrom from serveFile's io.Copy and turn every file into
// a userspace copy, and making a diagnostic change how bytes are served is exactly the trade
// this project does not make. "It shows a blank page" and "it 404s the CSS" look identical in a
// browser and are one grep apart here.
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	status := http.StatusOK
	defer func() {
		slog.Debug("serve", "method", r.Method, "path", r.URL.Path, "status", status,
			"ms", time.Since(started).Milliseconds())
	}()

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		status = http.StatusMethodNotAllowed
		http.Error(w, "frznforge dev serves files; it answers GET and HEAD only", status)
		return
	}

	// A rebuild changes every file under the same URLs. Letting the browser reuse a cached copy
	// is the one way this server can show stale content after a build, which is precisely the
	// confusion the notice exists to prevent — so it never offers to.
	w.Header().Set("Cache-Control", "no-store")

	upath := r.URL.Path
	if upath == "" {
		upath = "/"
	}
	if h.base != "" {
		rest, ok := underBase(upath, h.base)
		if !ok {
			// Not a 404.html: under a sub-path deploy this URL belongs to whatever else the host
			// serves, and saying so plainly is what makes a leaked root-absolute link obvious.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			status = http.StatusNotFound
			w.WriteHeader(status)
			fmt.Fprintf(w, "not under the deploy base %s\n", h.base)
			return
		}
		upath = rest
	}

	file, ok, err := h.resolve(upath)
	if err != nil {
		// The only way here is a path that climbed out of the served directory.
		status = http.StatusForbidden
		http.Error(w, "forbidden", status)
		return
	}
	if !ok {
		status = h.notFound(w, r)
		return
	}
	status = h.serveFile(w, r, file, http.StatusOK)
}

// underBase splits the deploy prefix off a request path. "/mysite" and "/mysite/x" are inside;
// "/mysiteX" is not, which is why this is not a plain HasPrefix.
func underBase(upath, base string) (string, bool) {
	if upath == base {
		return "/", true
	}
	if rest, ok := strings.CutPrefix(upath, base+"/"); ok {
		return "/" + rest, true
	}
	return "", false
}

// resolve maps a request path to a file on disk, or reports that nothing answers it.
//
// The order is the contract: an exact file wins first, because a raw route serves committed
// bytes under the committed name and `LICENSE` must not be mistaken for a directory or for
// `LICENSE.html`. Only then does a directory become its index.html, and only then does an
// extensionless path get the `.html` a page file was written under (`/404` → `404.html`).
func (h *handler) resolve(upath string) (file string, found bool, err error) {
	// Percent-encoding is already decoded in r.URL.Path, and the build wrote decoded names to
	// disk for the same reason (internal/build.writeAt), so the two meet without a second decode.
	clean := path.Clean("/" + strings.TrimPrefix(upath, "/"))
	full := filepath.Join(h.root, filepath.FromSlash(clean))
	if !h.inside(full) {
		// path.Clean cannot climb out on its own, but a decoded backslash can on Windows, where
		// filepath.Join treats it as a separator. This is the check that stops it.
		return "", false, fmt.Errorf("path %q escapes %s", upath, h.root)
	}

	// A trailing slash means a directory index and nothing else: /x.html/ is not /x.html.
	if strings.HasSuffix(upath, "/") {
		if index := filepath.Join(full, "index.html"); isFile(index) {
			return index, true, nil
		}
		return "", false, nil
	}
	if isFile(full) {
		return full, true, nil
	}
	if index := filepath.Join(full, "index.html"); isFile(index) {
		return index, true, nil
	}
	if page := full + ".html"; filepath.Ext(full) == "" && isFile(page) {
		return page, true, nil
	}
	return "", false, nil
}

// inside reports whether an already-joined path is still within the served directory.
func (h *handler) inside(full string) bool {
	return full == h.root || strings.HasPrefix(full, h.root+string(filepath.Separator))
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

// notFound serves the site's own 404 page with a 404 status — the same pair a static host
// gives, so a spec that asserts on both is asserting on production behaviour.
//
// It returns the status it wrote, for the request record in ServeHTTP.
func (h *handler) notFound(w http.ResponseWriter, r *http.Request) int {
	page := filepath.Join(h.root, "404.html")
	if isFile(page) {
		return h.serveFile(w, r, page, http.StatusNotFound)
	}
	// No 404.html means the directory is not a frznforge build (or the build was interrupted);
	// the preflight normally catches that before the server starts.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	fmt.Fprintln(w, "not found")
	return http.StatusNotFound
}

// serveFile writes one file with the type MimeFor gives it.
//
// http.ServeContent is deliberately not used: it would sniff or re-derive the content type from
// the OS's own tables, and this server's whole reason for existing is that it types files the
// way the built site does.
// It returns the status it wrote, for the request record in ServeHTTP.
func (h *handler) serveFile(w http.ResponseWriter, r *http.Request, file string, status int) int {
	f, err := os.Open(file)
	if err != nil {
		slog.Debug("serve: cannot open", "file", file, "err", err)
		http.Error(w, "cannot read "+filepath.Base(file), http.StatusInternalServerError)
		return http.StatusInternalServerError
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		slog.Debug("serve: cannot stat", "file", file, "err", err)
		http.Error(w, "cannot read "+filepath.Base(file), http.StatusInternalServerError)
		return http.StatusInternalServerError
	}

	w.Header().Set("Content-Type", MimeFor(file))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return status
	}
	io.Copy(w, f)
	return status
}
