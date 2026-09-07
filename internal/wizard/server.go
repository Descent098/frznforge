package wizard

// The server half: binding, the request guards, and the session's life from first request to
// Done. Ported from the `runWebInit` shell of scripts/lib/web-init.ts.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"frznforge/internal/ingest"
)

// inactivityTimeout: a forgotten tab must not leave a writable server running forever.
const inactivityTimeout = 15 * time.Minute

const (
	// maxBodyBytes refuses outright anything bigger — the real bodies are a few kilobytes.
	maxBodyBytes = 1 << 20
	// maxUploadBodyBytes is the ceiling for /api/upload alone: images travel as base64 inside the
	// ordinary JSON body, so 8 MiB of JSON is ~6 MiB of image — far more than an avatar needs and
	// still small enough that buffering it is harmless.
	maxUploadBodyBytes = 8 << 20
)

// Session is a bound listener plus the wizard over it. Listen binds so the URL is known before
// anything is printed; Serve then blocks until the visitor is done.
type Session struct {
	// URL is the page's address, session key included.
	URL string
	// ConfigPath is the file this session edits, "" when none was found. Every write goes here,
	// whatever the browser says.
	ConfigPath string

	opts Options
	now  func() time.Time
	key  string
	port int
	ln   net.Listener
	srv  *http.Server

	// mu serialises writes and guards every field below it.
	//
	// Nothing binds a session to one tab — the URL is printed and a browser may already have
	// opened one — so two tabs can both offer a working Save. Un-serialised, each request read
	// the file, spliced its own change and wrote: every one answered 200 while only the last
	// change survived. Holding this across the whole read-modify-write is what makes each save
	// see the one before it.
	mu sync.Mutex
	// writes counts saves that actually changed something on disk.
	writes int
	// backups maps a file to its session backup: one .bak per file per session, holding the
	// pre-wizard bytes. An empty value means "no backup" — either the copy failed or this
	// session created the file, which has no pre-wizard state to keep.
	backups map[string]string
	// listing is the last provider listing, which /api/preview and /api/write select against.
	listing *listing

	idleMu sync.Mutex
	idle   *time.Timer

	stop    sync.Once
	stopErr error
}

// listing is one loaded provider listing, keyed so a selection made against a different one is
// refused rather than silently resolved against this.
type listing struct {
	key   string
	repos []RemoteRepo
}

func listingKey(provider, host, account string) string {
	return provider + "|" + strings.ToLower(host) + "|" + strings.ToLower(account)
}

// Listen binds the port and prepares the wizard, without serving yet.
func Listen(opts Options) (*Session, error) {
	if opts.Root == "" {
		opts.Root = "."
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", opts.Root, err)
	}
	configPath := ""
	if opts.ConfigPath != "" {
		if configPath, err = filepath.Abs(filepath.Join(root, opts.ConfigPath)); err != nil {
			return nil, fmt.Errorf("resolve %s: %w", opts.ConfigPath, err)
		}
	} else {
		configPath = findConfigFile(root)
	}

	// 24 bytes because the key is the only thing between this server and any page the user has
	// open; base64url so it survives a URL, a terminal and a copy-paste unharmed.
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("could not mint a session key: %w — the wizard will not start without one", err)
	}

	// Loopback only. A server that can write files has no business on 0.0.0.0, whatever the
	// machine's other interfaces are doing.
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(opts.Port))
	if err != nil {
		return nil, fmt.Errorf("cannot listen on 127.0.0.1 port %d: %w\n  Something else is probably on that port — pass --port=<n>, or --port=0 to let the OS pick a free one",
			opts.Port, err)
	}
	port := opts.Port
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		port = tcp.Port
	}

	s := &Session{
		ConfigPath: configPath,
		opts:       opts,
		now:        opts.Now,
		key:        base64.RawURLEncoding.EncodeToString(raw),
		port:       port,
		ln:         ln,
		backups:    map[string]string{},
	}
	if s.now == nil {
		s.now = time.Now
	}
	s.URL = "http://127.0.0.1:" + strconv.Itoa(port) + "/?s=" + s.key
	s.srv = &http.Server{
		Handler: s,
		// The only client is a page this process just opened, so a request that has not sent
		// its headers in ten seconds is not one of ours.
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s, nil
}

// Serve accepts requests until the visitor presses Done or Cancel, the session goes idle, or
// Close is called.
func (s *Session) Serve() error {
	s.bump()
	err := s.srv.Serve(s.ln)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	s.stopIdle()
	if err == nil {
		err = s.stopErr
	}
	return err
}

// Close stops the session from outside — a Ctrl-C handler in the command, or a test tidying up.
func (s *Session) Close() error {
	s.finish("", nil)
	return nil
}

// Writes reports how many saves this session made. Safe to call once Serve has returned.
func (s *Session) Writes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes
}

func (s *Session) out() io.Writer {
	if s.opts.Out == nil {
		return os.Stdout
	}
	return s.opts.Out
}

func (s *Session) log(format string, args ...any) {
	fmt.Fprintf(s.out(), format+"\n", args...)
}

/* ------------------------------------------------------------------ lifecycle */

// bump restarts the inactivity clock. Called on every request, so the timer measures silence
// rather than session length.
func (s *Session) bump() {
	s.idleMu.Lock()
	defer s.idleMu.Unlock()
	if s.idle == nil {
		s.idle = time.AfterFunc(inactivityTimeout, s.timedOut)
		return
	}
	s.idle.Reset(inactivityTimeout)
}

func (s *Session) stopIdle() {
	s.idleMu.Lock()
	defer s.idleMu.Unlock()
	if s.idle != nil {
		s.idle.Stop()
	}
}

func (s *Session) timedOut() {
	s.finish("", fmt.Errorf("no activity for %d minutes — the init server has stopped. %s Anything typed into the page but not saved is gone with it",
		int(inactivityTimeout/time.Minute), s.wroteSummary()))
}

// wroteSummary is "Nothing was written." or how much was, for every way the session can end.
func (s *Session) wroteSummary() string {
	switch n := s.Writes(); n {
	case 0:
		return "Nothing was written."
	case 1:
		return "1 write was already saved."
	default:
		return strconv.Itoa(n) + " writes were already saved."
	}
}

// finish stops the server once, whatever ends the session.
//
// Shutdown runs in its own goroutine because a /api/done handler is a request Shutdown waits
// for: called inline it would wait for itself. Idle keep-alive sockets the page left behind go
// immediately, and anything still in flight gets a short grace period before the socket is cut.
func (s *Session) finish(message string, err error) {
	s.stop.Do(func() {
		s.stopIdle()
		s.stopErr = err
		if message != "" {
			s.log("%s", message)
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
			defer cancel()
			if shutdownErr := s.srv.Shutdown(ctx); shutdownErr != nil {
				_ = s.srv.Close()
			}
		}()
	})
}

/* ------------------------------------------------------------------ request guards */

// hostAllowed: the Host header must name the loopback interface we are actually listening on.
//
// This is the DNS-rebinding guard. `evil.example` re-resolved to 127.0.0.1 still sends
// `Host: evil.example`, so pinning the header keeps the wizard reachable only through a URL the
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

// tokenMatches compares in constant time, so a wrong guess leaks nothing through timing.
func tokenMatches(presented, expected string) bool {
	return subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1
}

/* ------------------------------------------------------------------ dispatch */

func (s *Session) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !hostAllowed(r.Host, s.port) {
		s.sendText(w, http.StatusForbidden, "forbidden: this server only answers on 127.0.0.1\n")
		return
	}
	presented := r.URL.Query().Get("s")
	if presented == "" {
		presented = r.Header.Get("X-Frznforge-Session")
	}
	if !tokenMatches(presented, s.key) {
		s.sendText(w, http.StatusForbidden, "forbidden: missing or wrong session key — open the URL printed in the terminal\n")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") && !originAllowed(r.Header.Get("Origin"), s.port) {
		s.sendText(w, http.StatusForbidden, "forbidden: cross-origin request\n")
		return
	}
	s.bump()

	if err := s.route(w, r); err != nil {
		status, message := errStatus(err)
		s.sendJSON(w, status, map[string]any{"error": s.scrub(message)}, false)
	}
}

// route answers one request. Every handler either writes a response or returns an error — never
// both, so the error path above can always be the one that sets the status.
func (s *Session) route(w http.ResponseWriter, r *http.Request) error {
	path := r.URL.Path
	switch path {
	case "/", "/index.html":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			return methodNotAllowed()
		}
		s.sendPage(w)
		return nil
	case "/api/context":
		return s.handleContext(w)
	case "/api/config":
		return s.handleConfig(w)
	case "/api/profile":
		return s.handleProfile(w)
	}

	if r.Method != http.MethodPost {
		return methodNotAllowed()
	}
	limit := int64(maxBodyBytes)
	if path == "/api/upload" {
		limit = maxUploadBodyBytes
	}
	body, err := readBody(r, limit)
	if err != nil {
		return err
	}

	switch path {
	case "/api/repos":
		return s.handleRepos(w, body)
	case "/api/preview":
		return s.handlePreview(w, body)
	case "/api/write":
		return s.handleWrite(w, body)
	case "/api/config/write":
		return s.handleConfigWrite(w, body)
	case "/api/profile/preview":
		return s.handleProfilePreview(w, body)
	case "/api/profile/write":
		return s.handleProfileWrite(w, body)
	case "/api/upload":
		return s.handleUpload(w, body)
	case "/api/done":
		return s.handleDone(w)
	case "/api/cancel":
		return s.handleCancel(w)
	}
	return &httpError{status: http.StatusNotFound, message: "no such endpoint: " + path}
}

/* ------------------------------------------------------------------ errors */

// The refusals, as the statuses the page distinguishes. httpError is shared with the provider
// listings (providers.go), where its status field is already load-bearing.
func badRequest(message string) *httpError {
	return &httpError{status: http.StatusBadRequest, message: message}
}

// conflict: the request was well-formed but the state of the files refuses it.
func conflict(message string) *httpError {
	return &httpError{status: http.StatusConflict, message: message}
}

func methodNotAllowed() *httpError {
	return &httpError{status: http.StatusMethodNotAllowed, message: "method not allowed"}
}

// errStatus maps an error to the status and text the browser gets. Anything that is not an
// httpError is a bug rather than a refusal, so it answers 500 with its own message — the page
// shows it, and a wizard that says "500" and nothing else is a wizard nobody can debug.
func errStatus(err error) (int, string) {
	var he *httpError
	if errors.As(err, &he) && he.status != 0 {
		return he.status, he.message
	}
	return http.StatusInternalServerError, err.Error()
}

// scrub keeps a provider token out of anything the browser is about to see.
//
// Every token in the environment is redacted, not just the one the failing call used: an error
// travels a long way from where it was made (a listing error, a filesystem path, a wrapped
// cause), and the cost of asking about four environment variables is nothing next to the cost
// of being wrong once.
func (s *Session) scrub(text string) string {
	for _, name := range providerNames {
		if token := statusFor(name, s.opts.Env).token; token != "" {
			text = ingest.RedactToken(text, token)
		}
	}
	return text
}

/* ------------------------------------------------------------------ responses */

// send writes one complete response with the wizard's standing headers.
//
// last closes the connection: /api/done and /api/cancel stop the server the moment the response
// leaves, and a browser holding a keep-alive socket to a server that is going away gets a
// connection error instead of the answer it already had.
func (s *Session) send(w http.ResponseWriter, status int, contentType, body string, last bool) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	if last {
		h.Set("Connection", "close")
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func (s *Session) sendText(w http.ResponseWriter, status int, body string) {
	s.send(w, status, "text/plain; charset=utf-8", body, false)
}

func (s *Session) sendJSON(w http.ResponseWriter, status int, body any, last bool) {
	encoded, err := json.Marshal(body)
	if err != nil {
		// Only reachable if a handler built something unserialisable, which is a programming
		// error; say so rather than sending a body the page will fail to parse.
		s.sendText(w, http.StatusInternalServerError, "the wizard could not encode its own answer: "+err.Error()+"\n")
		return
	}
	s.send(w, status, "application/json; charset=utf-8", string(encoded), last)
}

// sendPage serves the wizard's HTML.
//
// The CSP is what makes "this page talks to nothing but its own server" a rule the browser
// enforces rather than a claim the source makes: no fonts, no CDN, no image host, no form post,
// and nothing may frame it.
func (s *Session) sendPage(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; connect-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; "+
			"img-src data:; form-action 'none'; base-uri 'none'; frame-ancestors 'none'")
	// A HEAD needs no branch here: net/http drops the body of a HEAD response itself, and the
	// Content-Length below still describes the page.
	s.send(w, http.StatusOK, "text/html; charset=utf-8", pageHTML, false)
}

/* ------------------------------------------------------------------ request bodies */

// readBody reads a JSON object body, refusing anything larger than max.
func readBody(r *http.Request, max int64) (map[string]any, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil {
		return nil, badRequest("could not read the request body: " + err.Error())
	}
	if int64(len(raw)) > max {
		return nil, badRequest("request body is too large")
	}
	if strings.TrimSpace(string(raw)) == "" {
		return map[string]any{}, nil
	}
	var body any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, badRequest("body is not JSON")
	}
	return asRecord(body)
}

func asRecord(value any) (map[string]any, error) {
	record, ok := value.(map[string]any)
	if !ok {
		return nil, badRequest("expected a JSON object")
	}
	return record, nil
}

/* ------------------------------------------------------------------ ending the session */

func (s *Session) handleDone(w http.ResponseWriter) error {
	// Take the write lock before answering: a save still in flight must land before the process
	// says it is finished, or Done silently drops the edit the user just made.
	s.mu.Lock()
	writes := s.writes
	s.mu.Unlock()

	s.sendJSON(w, http.StatusOK, map[string]any{"done": true, "writes": writes}, true)
	message := "Done — nothing was changed."
	if writes > 0 {
		plural := "s"
		if writes == 1 {
			plural = ""
		}
		message = fmt.Sprintf("Done — %d write%s this session. Next: frznforge build", writes, plural)
	}
	s.finish(message, nil)
	return nil
}

func (s *Session) handleCancel(w http.ResponseWriter) error {
	s.mu.Lock()
	writes := s.writes
	s.mu.Unlock()

	s.sendJSON(w, http.StatusOK, map[string]any{"cancelled": true, "writes": writes}, true)
	message := "Cancelled — nothing was written."
	if writes > 0 {
		message = "Stopped. " + s.wroteSummary()
	}
	s.finish(message, nil)
	return nil
}
