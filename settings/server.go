package settings

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"sync"

	"linux-voice-input/config"
)

// DefaultPort is the fixed loopback port the settings UI listens on. It is a
// high, uncommon port to make collisions unlikely; the service is loopback-only
// and token-guarded, and it is intentionally never auto-closed on idle so a
// left-open browser tab keeps working for the daemon's lifetime.
const DefaultPort = 31959

//go:embed web
var webFS embed.FS

// Options configures a Server. Load and Save are injectable so tests can avoid
// touching the real ~/.linux-voice-input config; both default to the config
// package functions. OnSaved runs after a successful save so the daemon can
// apply the new config (wired to the tray app's reload).
type Options struct {
	Port    int
	Load    func() (*config.Config, error)
	Save    func(*config.Config) error
	OnSaved func()

	// Stats, Doctor and Download are optional providers that back the extra
	// panels. They are injected by ui so this package does not import stats,
	// doctor or models. A nil provider makes its endpoint return 501 and the
	// frontend hides that panel.
	Stats    func() (StatsResponse, error)
	Doctor   func() []DoctorItem
	Download DownloadFunc
}

// Server is the on-demand local settings web server. It is started once and
// then stays up; Start is idempotent.
type Server struct {
	port    int
	load    func() (*config.Config, error)
	save    func(*config.Config) error
	onSaved func()

	stats    func() (StatsResponse, error)
	doctor   func() []DoctorItem
	download *DownloadManager

	token string

	mu      sync.Mutex
	started bool
	httpSrv *http.Server
}

// NewServer builds a server from options, filling defaults and generating the
// per-process access token.
func NewServer(opts Options) *Server {
	s := &Server{
		port:    opts.Port,
		load:    opts.Load,
		save:    opts.Save,
		onSaved: opts.OnSaved,
		stats:   opts.Stats,
		doctor:  opts.Doctor,
	}
	if s.port == 0 {
		s.port = DefaultPort
	}
	if s.load == nil {
		s.load = config.Load
	}
	if s.save == nil {
		s.save = config.Save
	}
	if opts.Download != nil {
		s.download = NewDownloadManager(opts.Download)
	}
	s.token = newToken()
	return s
}

// newToken returns a 32-hex-char random token, or a fixed fallback only if the
// system RNG fails (which should never happen on a normal desktop).
func newToken() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "insecure-fallback-token"
	}
	return hex.EncodeToString(buf)
}

// URL returns the loopback address a browser should open, including the token.
func (s *Server) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/?token=%s", s.port, s.token)
}

// Start begins listening if not already started. It is safe to call repeatedly
// (e.g. every time the user clicks the tray item); subsequent calls are no-ops
// that still return the current URL via URL(). Listening binds loopback only.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
	if err != nil {
		return fmt.Errorf("listen settings port %d: %w", s.port, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/doctor", s.handleDoctor)
	mux.HandleFunc("/api/download", s.handleDownload)
	mux.HandleFunc("/api/features", s.handleFeatures)
	mux.Handle("/", s.staticHandler())

	s.httpSrv = &http.Server{Handler: s.guard(mux)}
	s.started = true

	go func() {
		if err := s.httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("Settings server stopped: %v", err)
		}
	}()
	log.Printf("Settings UI listening on 127.0.0.1:%d", s.port)
	return nil
}

// staticHandler serves the embedded single-page app from the web/ subtree.
func (s *Server) staticHandler() http.Handler {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		// Should never happen: web/ is embedded at build time.
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "embedded assets unavailable", http.StatusInternalServerError)
		})
	}
	return http.FileServer(http.FS(sub))
}

// guard applies the loopback-safety middleware to every request: it pins the
// Host header to the expected loopback authority (blocking DNS-rebinding, where
// a malicious page resolves an attacker domain to 127.0.0.1 and talks to this
// server through the victim's browser). The token is checked per-endpoint, not
// here, so the static page can load and read its own token from the URL.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hostAllowed(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostAllowed accepts only the loopback authority on the configured port.
func (s *Server) hostAllowed(host string) bool {
	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}
	// Require the exact port to be present; a bare host (no port) is rejected.
	if _, _, err := net.SplitHostPort(host); err != nil {
		return false
	}
	_, port, _ := net.SplitHostPort(host)
	if port != fmt.Sprintf("%d", s.port) {
		return false
	}
	return hostname == "127.0.0.1" || hostname == "localhost"
}

// tokenValid compares the request's token (header preferred, query fallback)
// against the server token in constant time.
func (s *Server) tokenValid(r *http.Request) bool {
	got := r.Header.Get("X-Config-Token")
	if got == "" {
		got = r.URL.Query().Get("token")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

// handleConfig serves GET (read current config) and POST (save + apply). Both
// require a valid token.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if !s.tokenValid(r) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.getConfig(w)
	case http.MethodPost:
		s.postConfig(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) getConfig(w http.ResponseWriter) {
	cfg, err := s.load()
	if err != nil {
		http.Error(w, "load config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonBytes, err := ConfigToJSON(cfg)
	if err != nil {
		http.Error(w, "encode config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(jsonBytes)
}

func (s *Server) postConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	cfg, err := ConfigFromJSON(body)
	if err != nil {
		http.Error(w, "parse config: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.save(cfg); err != nil {
		http.Error(w, "save config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if s.onSaved != nil {
		s.onSaved()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"ok":true}`))
}
