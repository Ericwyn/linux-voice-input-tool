package settings

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"linux-voice-input/config"
)

// newTestServer builds a Server with injected in-memory load/save so tests
// never touch the real config directory. It returns the server and a pointer to
// the last-saved config.
func newTestServer(t *testing.T) (*Server, **config.Config, *int) {
	t.Helper()
	stored := config.DefaultConfig()
	stored.CorrectionMode = config.CorrectionModeLocal
	var saved *config.Config
	savedCount := 0
	s := NewServer(Options{
		Load: func() (*config.Config, error) { return stored, nil },
		Save: func(c *config.Config) error { saved = c; return nil },
		OnSaved: func() { savedCount++ },
	})
	return s, &saved, &savedCount
}

func TestGetConfigRequiresToken(t *testing.T) {
	s, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.Host = "127.0.0.1:31959"
	rec := httptest.NewRecorder()
	s.handleConfig(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", rec.Code)
	}
}

func TestGetConfigWithToken(t *testing.T) {
	s, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.Host = "127.0.0.1:31959"
	req.Header.Set("X-Config-Token", s.token)
	rec := httptest.NewRecorder()
	s.handleConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var tree map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &tree); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if tree["correction_mode"] != "local" {
		t.Fatalf("correction_mode = %v, want local", tree["correction_mode"])
	}
}

func TestPostConfigSavesAndFiresCallback(t *testing.T) {
	s, saved, savedCount := newTestServer(t)

	// Fetch current, flip a value, post it back.
	getReq := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	getReq.Host = "127.0.0.1:31959"
	getReq.Header.Set("X-Config-Token", s.token)
	getRec := httptest.NewRecorder()
	s.handleConfig(getRec, getReq)

	var tree map[string]any
	if err := json.Unmarshal(getRec.Body.Bytes(), &tree); err != nil {
		t.Fatal(err)
	}
	tree["correction_mode"] = "combined"
	body, _ := json.Marshal(tree)

	postReq := httptest.NewRequest(http.MethodPost, "/api/config", bytes.NewReader(body))
	postReq.Host = "127.0.0.1:31959"
	postReq.Header.Set("X-Config-Token", s.token)
	postRec := httptest.NewRecorder()
	s.handleConfig(postRec, postReq)

	if postRec.Code != http.StatusOK {
		t.Fatalf("post status = %d, want 200 (body %q)", postRec.Code, postRec.Body.String())
	}
	if *saved == nil {
		t.Fatal("save was not called")
	}
	if (*saved).CorrectionMode != config.CorrectionModeCombined {
		t.Fatalf("saved correction_mode = %q, want combined", (*saved).CorrectionMode)
	}
	if *savedCount != 1 {
		t.Fatalf("OnSaved fired %d times, want 1", *savedCount)
	}
}

func TestPostConfigRejectsBadToken(t *testing.T) {
	s, saved, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/config", bytes.NewReader([]byte("{}")))
	req.Host = "127.0.0.1:31959"
	req.Header.Set("X-Config-Token", "wrong")
	rec := httptest.NewRecorder()
	s.handleConfig(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if *saved != nil {
		t.Fatal("bad token must not save")
	}
}

func TestHostAllowed(t *testing.T) {
	s := NewServer(Options{})
	cases := map[string]bool{
		"127.0.0.1:31959": true,
		"localhost:31959": true,
		"127.0.0.1:9999":  false, // wrong port
		"127.0.0.1":       false, // no port
		"evil.com:31959":  false, // non-loopback host
		"0.0.0.0:31959":   false,
	}
	for host, want := range cases {
		if got := s.hostAllowed(host); got != want {
			t.Errorf("hostAllowed(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestGuardBlocksBadHost(t *testing.T) {
	s := NewServer(Options{})
	handler := s.guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "evil.com:31959"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bad host: status = %d, want 403", rec.Code)
	}
}

func TestURLContainsToken(t *testing.T) {
	s := NewServer(Options{Port: 40000})
	url := s.URL()
	if url == "" || !bytes.Contains([]byte(url), []byte(s.token)) {
		t.Fatalf("URL %q missing token", url)
	}
	if !bytes.Contains([]byte(url), []byte("127.0.0.1:40000")) {
		t.Fatalf("URL %q missing loopback authority", url)
	}
}
