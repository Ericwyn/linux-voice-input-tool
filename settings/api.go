package settings

import (
	"encoding/json"
	"net/http"
)

// StatsResponse is the read-only statistics payload the settings page shows. It
// is a settings-local shape so this package stays decoupled from the stats
// package; ui adapts stats.Aggregate into it via the injected provider.
type StatsResponse struct {
	Enabled bool         `json:"enabled"`
	Today   StatsPeriod  `json:"today"`
	Recent  StatsPeriod  `json:"recent"` // rolling 30 days
	All     StatsPeriod  `json:"all"`
}

// StatsPeriod is one aggregation window rendered as already-formatted,
// display-ready fields so the frontend needs no unit logic.
type StatsPeriod struct {
	Label              string `json:"label"`
	Days               int    `json:"days"`
	SuccessfulInputs   uint64 `json:"successful_inputs"`
	Characters         uint64 `json:"characters"`
	HanCharacters      uint64 `json:"han_characters"`
	CorrectedInputs    uint64 `json:"corrected_inputs"`
	RecordingDuration  string `json:"recording_duration"`
	InputDuration      string `json:"input_duration"`
}

// DoctorItem is one environment-check row.
type DoctorItem struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// DownloadStatus is the polled state of a model download. Log holds cumulative
// human-readable lines; the frontend appends them to a console view.
type DownloadStatus struct {
	Running bool     `json:"running"`
	Done    bool     `json:"done"`
	Error   string   `json:"error,omitempty"`
	Log     []string `json:"log"`
}

// handleStats serves the read-only statistics panel (GET).
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if !s.tokenValid(r) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	if s.stats == nil {
		http.Error(w, "statistics unavailable", http.StatusNotImplemented)
		return
	}
	view, err := s.stats()
	if err != nil {
		http.Error(w, "load stats: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, view)
}

// handleDoctor runs environment checks on demand (GET).
func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	if !s.tokenValid(r) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	if s.doctor == nil {
		http.Error(w, "doctor unavailable", http.StatusNotImplemented)
		return
	}
	writeJSON(w, s.doctor())
}

// handleDownload starts a download (POST) or returns its status (GET). Starting
// while one is running is a no-op; the frontend polls GET to stream the log.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if !s.tokenValid(r) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	if s.download == nil {
		http.Error(w, "download unavailable", http.StatusNotImplemented)
		return
	}
	switch r.Method {
	case http.MethodPost:
		s.download.Start()
		writeJSON(w, s.download.Status())
	case http.MethodGet:
		writeJSON(w, s.download.Status())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// FeaturesResponse tells the frontend which optional panels are backed by a
// provider, so it can hide panels the daemon did not wire up (e.g. when the
// page is served by a build without stats/doctor/download providers).
type FeaturesResponse struct {
	Stats    bool `json:"stats"`
	Doctor   bool `json:"doctor"`
	Download bool `json:"download"`
}

// handleFeatures reports provider availability (GET). It needs no token: it
// leaks no config, only booleans, and the page fetches it before anything else.
func (s *Server) handleFeatures(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, FeaturesResponse{
		Stats:    s.stats != nil,
		Doctor:   s.doctor != nil,
		Download: s.download != nil,
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
