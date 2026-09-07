package main

// The browser half of the viewer: `frzndebugger --web`.
//
// It is net/http and one embedded HTML file, exactly as internal/wizard is, and for the same
// reasons — no framework, no CDN, no third-party host. The project serves everything verbatim
// and calls nobody; a debugging tool that phoned out to a CDN to render a stack trace would be
// the one place in the codebase that did.
//
// The security model, because a page on localhost is still a page:
//
//   - Bound to 127.0.0.1 only. Never 0.0.0.0: this reads a diagnostic file off the developer's
//     disk, and whatever network the machine is on has no business in it.
//   - A per-run session key in the URL, required on every request including the page. Loopback
//     is reachable by a fetch() from any site the user has open; the key is what makes that fail.
//   - Host and Origin pinning, which is what stops DNS rebinding: an attacker's name resolved to
//     127.0.0.1 still arrives carrying THEIR Host header.
//   - THE BROWSER NEVER NAMES A FILE. There is no path parameter anywhere in this API, so there
//     is nothing to traverse: the directory is fixed when the server binds, and the only two
//     files ever opened are logging.LogPath(dir) and timings.Path(dir). A `?run=` is compared
//     against run ids already in memory and never reaches the filesystem.
//   - GET and HEAD only. Nothing here writes anything, anywhere.
//
// Everything the page shows was computed by the same functions the terminal UI uses — the
// aggregation by internal/timings itself. The page sorts columns and opens rows; it does not do
// arithmetic, because a second implementation of "what does worst mean" is a second answer.

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"frznforge/internal/timings"
)

// pageHTML is the entire user interface: one self-contained document with inline CSS and inline
// vanilla JavaScript, no build step, no framework, no CDN.
//
// Nothing is ever interpolated into it — everything the page knows arrives over /api/data and
// goes onto the page through textContent — so it cannot carry an injection from a log line.
//
//go:embed page.html
var pageHTML string

// WebOptions configure one viewer server.
type WebOptions struct {
	// Dir is the ingest output directory holding the two files. Fixed here and nowhere else.
	Dir string
	// Port to listen on. 0 asks the OS for a free one.
	Port int
}

// WebServer is a bound listener plus the handler over it.
type WebServer struct {
	// URL is the page's address, session key included.
	URL string
	// Dir is the directory being read.
	Dir string

	key  string
	port int
	ln   net.Listener
	srv  *http.Server
}

// ListenWeb binds the port and prepares the handler, without serving yet.
func ListenWeb(opts WebOptions) (*WebServer, error) {
	// 24 bytes because the key is the only thing between this server and any page the user has
	// open; base64url so it survives a URL, a terminal and a copy-paste unharmed.
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("could not mint a session key: %w -- the viewer will not start without one", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(opts.Port))
	if err != nil {
		return nil, fmt.Errorf("cannot listen on 127.0.0.1 port %d: %w\n  Something else is probably on that port -- pass --port=<n>, or --port=0 to let the OS pick a free one",
			opts.Port, err)
	}
	port := opts.Port
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		port = tcp.Port
	}
	s := &WebServer{
		Dir:  opts.Dir,
		key:  base64.RawURLEncoding.EncodeToString(raw),
		port: port,
		ln:   ln,
	}
	s.URL = "http://127.0.0.1:" + strconv.Itoa(port) + "/?s=" + s.key
	s.srv = &http.Server{
		Handler: s,
		// The only client is a browser the user just pointed here, so a request that has not sent
		// its headers in ten seconds is not one of ours.
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s, nil
}

// Serve accepts requests until Close is called. A closed server is a clean exit, not an error:
// Ctrl-C is the normal way this command ends.
func (s *WebServer) Serve() error {
	err := s.srv.Serve(s.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops the server and releases the port.
//
// Both, deliberately: http.Server.Close only closes listeners Serve registered, and ListenWeb
// creates ours before Serve is ever called, so on a bind-then-bail path srv.Close alone leaves
// the socket bound and the next run fails with "only one usage of each socket address".
func (s *WebServer) Close() error {
	err := s.srv.Close()
	if s.ln != nil {
		if lnErr := s.ln.Close(); err == nil && !errors.Is(lnErr, net.ErrClosed) {
			err = lnErr
		}
	}
	return err
}

func (s *WebServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !hostAllowed(r.Host, s.port) {
		sendText(w, http.StatusForbidden, "forbidden: this server only answers on 127.0.0.1\n")
		return
	}
	presented := r.URL.Query().Get("s")
	if presented == "" {
		presented = r.Header.Get("X-Frznforge-Session")
	}
	if subtle.ConstantTimeCompare([]byte(presented), []byte(s.key)) != 1 {
		sendText(w, http.StatusForbidden, "forbidden: missing or wrong session key -- open the URL printed in the terminal\n")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		sendText(w, http.StatusMethodNotAllowed, "frzndebugger reads files; it answers GET and HEAD only\n")
		return
	}

	switch r.URL.Path {
	case "/", "/index.html":
		// The CSP is what makes "this page talks to nothing but its own server" a rule the browser
		// enforces rather than a claim the source makes: no fonts, no CDN, no image host, no form
		// post, and nothing may frame it.
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; connect-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; "+
				"form-action 'none'; base-uri 'none'; frame-ancestors 'none'")
		send(w, http.StatusOK, "text/html; charset=utf-8", pageHTML)
	case "/api/data":
		if !originAllowed(r.Header.Get("Origin"), s.port) {
			sendText(w, http.StatusForbidden, "forbidden: cross-origin request\n")
			return
		}
		body, err := json.Marshal(buildPayload(s.Dir, r.URL.Query().Get("run")))
		if err != nil {
			sendText(w, http.StatusInternalServerError, "the viewer could not encode its own answer: "+err.Error()+"\n")
			return
		}
		send(w, http.StatusOK, "application/json; charset=utf-8", string(body))
	default:
		sendText(w, http.StatusNotFound, "no such endpoint: "+r.URL.Path+"\n")
	}
}

// hostAllowed: the Host header must name the loopback interface we are actually listening on.
//
// This is the DNS-rebinding guard. `evil.example` re-resolved to 127.0.0.1 still sends
// `Host: evil.example`, so pinning the header keeps the viewer reachable only through a URL the
// user could have typed themselves.
func hostAllowed(header string, port int) bool {
	if header == "" {
		return false
	}
	p := strconv.Itoa(port)
	return header == "127.0.0.1:"+p || header == "localhost:"+p || header == "[::1]:"+p
}

// originAllowed: an absent Origin is fine (a typed-in URL has none); a foreign one never is.
func originAllowed(header string, port int) bool {
	if header == "" || header == "null" {
		return true
	}
	p := strconv.Itoa(port)
	return header == "http://127.0.0.1:"+p || header == "http://localhost:"+p || header == "http://[::1]:"+p
}

func send(w http.ResponseWriter, status int, contentType, body string) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	// net/http drops the body of a HEAD response itself, so there is no branch here and the
	// Content-Length above still describes the page.
	_, _ = io.WriteString(w, body)
}

func sendText(w http.ResponseWriter, status int, body string) {
	send(w, status, "text/plain; charset=utf-8", body)
}

/* ---- the payload --------------------------------------------------------- */

// The wire format the page reads. Durations travel as fractional milliseconds, matching the "ms"
// field of the timings file itself, so a number on the page can be found in the file by eye.
type payload struct {
	Dir          string `json:"dir"`
	LogPath      string `json:"logPath"`
	TimingsPath  string `json:"timingsPath"`
	LogError     string `json:"logError,omitempty"`
	TimingsError string `json:"timingsError,omitempty"`

	// Run is the run id actually used, which is not always the one asked for: an empty or
	// unknown id means the newest run, and the page shows what it got rather than what it wanted.
	Run                 string           `json:"run"`
	Runs                []runJSON        `json:"runs"`
	Unfinished          []unfinishedJSON `json:"unfinished"`
	UnfinishedTruncated int              `json:"unfinishedTruncated"`
	Aggregate           []statJSON       `json:"aggregate"`
	Tree                []nodeJSON       `json:"tree"`
	Log                 []LogRecord      `json:"log"`
}

type runJSON struct {
	ID      string  `json:"id"`
	Start   string  `json:"start"`
	End     string  `json:"end"`
	WallMS  float64 `json:"wallMs"`
	Records int     `json:"records"`
	Failed  int     `json:"failed"`
}

type statJSON struct {
	Kind    string         `json:"kind"`
	Name    string         `json:"name"`
	Count   int            `json:"count"`
	Failed  int            `json:"failed"`
	Runs    int            `json:"runs"`
	TotalMS float64        `json:"totalMs"`
	MeanMS  float64        `json:"meanMs"`
	BestMS  float64        `json:"bestMs"`
	WorstMS float64        `json:"worstMs"`
	Counts  timings.Counts `json:"counts,omitempty"`
}

type nodeJSON struct {
	statJSON
	Key      string     `json:"key"`
	Depth    int        `json:"depth"`
	Children []nodeJSON `json:"children,omitempty"`
}

type unfinishedJSON struct {
	Run string `json:"run"`
	ID  string `json:"id"`
	// Since and LastSeen are empty when nothing named this step as its parent, which is the case
	// where the hole in the id sequence is all that is known.
	Since     string           `json:"since,omitempty"`
	LastSeen  string           `json:"lastSeen,omitempty"`
	AtLeastMS float64          `json:"atLeastMs"`
	Children  []timings.Record `json:"children,omitempty"`
}

// buildPayload reads both files and answers one request. Read per request rather than cached, so
// the Reload button shows a build that is still running rather than the state at bind time.
func buildPayload(dir, run string) payload {
	data := Load(dir)
	scoped, chosen := Scope(data.Records, run)
	unfinished, truncated := UnfinishedsUntil(scoped, LastLogTime(data.Log, chosen))

	p := payload{
		Dir:                 data.Dir,
		LogPath:             data.LogPath,
		TimingsPath:         data.TimingsPath,
		Run:                 chosen,
		UnfinishedTruncated: truncated,
		Log:                 data.Log,
		Runs:                []runJSON{},
		Unfinished:          []unfinishedJSON{},
		Aggregate:           []statJSON{},
		Tree:                []nodeJSON{},
	}
	if data.LogErr != nil {
		p.LogError = data.LogErr.Error()
	}
	if data.TimingsErr != nil {
		p.TimingsError = data.TimingsErr.Error()
	}
	if p.Log == nil {
		p.Log = []LogRecord{}
	}

	for _, r := range timings.Runs(data.Records) {
		p.Runs = append(p.Runs, runJSON{
			ID: r.ID, Start: stampOf(r.Start), End: stampOf(r.End),
			WallMS: msOf(r.Wall), Records: r.Records, Failed: r.Failed,
		})
	}
	for _, u := range unfinished {
		p.Unfinished = append(p.Unfinished, unfinishedJSON{
			Run: u.Run, ID: u.ID,
			Since: stampOf(u.Since), LastSeen: stampOf(u.LastSeen),
			AtLeastMS: msOf(u.AtLeast()), Children: u.Children,
		})
	}
	for _, s := range timings.Aggregate(scoped) {
		p.Aggregate = append(p.Aggregate, statOf(s))
	}
	tree := BuildTree(scoped)
	SortNodes(tree, SortTotal, true)
	if roots := nodesOf(tree); roots != nil {
		// Only at the top: a leaf's children stay absent (omitempty) rather than becoming an empty
		// array on every one of them, but the page iterates the roots without a guard.
		p.Tree = roots
	}
	return p
}

func nodesOf(nodes []Node) []nodeJSON {
	if len(nodes) == 0 {
		return nil
	}
	out := make([]nodeJSON, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, nodeJSON{
			statJSON: statOf(n.Stat),
			Key:      n.Key,
			Depth:    n.Depth,
			Children: nodesOf(n.Children),
		})
	}
	return out
}

func statOf(s timings.Stat) statJSON {
	return statJSON{
		Kind: s.Kind, Name: s.Name, Count: s.Count, Failed: s.Failed, Runs: s.Runs,
		TotalMS: msOf(s.Total), MeanMS: msOf(s.Mean), BestMS: msOf(s.Best), WorstMS: msOf(s.Worst),
		Counts: s.Counts,
	}
}

// msOf converts to fractional milliseconds at microsecond resolution — the same precision the
// timings file itself carries, so nothing gains false digits on the way to the browser.
func msOf(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func stampOf(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}
