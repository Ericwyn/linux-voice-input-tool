package settings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func withToken(req *http.Request, s *Server) *http.Request {
	req.Host = "127.0.0.1:31959"
	req.Header.Set("X-Config-Token", s.token)
	return req
}

func TestHandleStats(t *testing.T) {
	s := NewServer(Options{
		Stats: func() (StatsResponse, error) {
			return StatsResponse{
				Enabled: true,
				Today:   StatsPeriod{Label: "今日", Days: 1, SuccessfulInputs: 3, Characters: 42},
			}, nil
		},
	})
	req := withToken(httptest.NewRequest(http.MethodGet, "/api/stats", nil), s)
	rec := httptest.NewRecorder()
	s.handleStats(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	var got StatsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.Today.SuccessfulInputs != 3 || got.Today.Characters != 42 {
		t.Fatalf("stats = %#v", got)
	}
}

func TestHandleStatsUnavailable(t *testing.T) {
	s := NewServer(Options{}) // no Stats provider
	req := withToken(httptest.NewRequest(http.MethodGet, "/api/stats", nil), s)
	rec := httptest.NewRecorder()
	s.handleStats(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
}

func TestHandleStatsRequiresToken(t *testing.T) {
	s := NewServer(Options{Stats: func() (StatsResponse, error) { return StatsResponse{}, nil }})
	req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	req.Host = "127.0.0.1:31959"
	rec := httptest.NewRecorder()
	s.handleStats(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleDoctor(t *testing.T) {
	s := NewServer(Options{
		Doctor: func() []DoctorItem {
			return []DoctorItem{
				{Name: "显示服务器", OK: true, Detail: "X11"},
				{Name: "xdotool", OK: false, Detail: "未安装"},
			}
		},
	})
	req := withToken(httptest.NewRequest(http.MethodGet, "/api/doctor", nil), s)
	rec := httptest.NewRecorder()
	s.handleDoctor(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var items []DoctorItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "显示服务器" || items[1].OK {
		t.Fatalf("doctor items = %#v", items)
	}
}

func TestHandleFeatures(t *testing.T) {
	s := NewServer(Options{
		Stats:    func() (StatsResponse, error) { return StatsResponse{}, nil },
		Download: func(context.Context, func(string)) error { return nil },
		// Doctor intentionally nil.
	})
	req := httptest.NewRequest(http.MethodGet, "/api/features", nil)
	req.Host = "127.0.0.1:31959"
	rec := httptest.NewRecorder()
	s.handleFeatures(rec, req)
	var f FeaturesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	if !f.Stats || f.Doctor || !f.Download {
		t.Fatalf("features = %#v, want stats+download, no doctor", f)
	}
}

func TestDownloadManagerLifecycle(t *testing.T) {
	released := make(chan struct{})
	m := NewDownloadManager(func(ctx context.Context, emit func(string)) error {
		emit("下载 模型A")
		<-released // hold until released so we can observe the running state
		emit("模型A 完成")
		return nil
	})

	m.Start()
	// Starting again while running must be a no-op (not launch a second run).
	m.Start()

	st := m.Status()
	if !st.Running {
		t.Fatalf("expected running, got %#v", st)
	}

	close(released)
	// Wait for completion.
	deadline := time.After(2 * time.Second)
	for {
		if st = m.Status(); st.Done {
			break
		}
		select {
		case <-deadline:
			t.Fatal("download did not finish")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if st.Running || st.Error != "" {
		t.Fatalf("final status = %#v", st)
	}
	// Log should contain the emitted lines plus the completion marker.
	joined := ""
	for _, l := range st.Log {
		joined += l + "\n"
	}
	if !contains(st.Log, "下载 模型A") || !contains(st.Log, "模型A 完成") {
		t.Fatalf("log missing emitted lines: %q", joined)
	}
}

func TestDownloadManagerError(t *testing.T) {
	m := NewDownloadManager(func(ctx context.Context, emit func(string)) error {
		return errors.New("boom")
	})
	m.Start()
	deadline := time.After(2 * time.Second)
	for {
		if st := m.Status(); st.Done {
			if st.Error != "boom" {
				t.Fatalf("error = %q, want boom", st.Error)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("download did not finish")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
