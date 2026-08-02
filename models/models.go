// Package models describes the sherpa-onnx model bundles the app depends on,
// derives their on-disk paths from a single base directory, and can check for
// and download/extract missing models.
package models

import (
	"archive/tar"
	"compress/bzip2"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Model describes one downloadable sherpa-onnx bundle. Dir is the top-level
// directory contained in the tarball (extraction into the base directory
// yields baseDir/Dir/...). Files lists the files that must exist for the model
// to be considered present, relative to Dir.
type Model struct {
	Label string
	Dir   string
	URL   string
	Files []string
}

// All lists the three models used by the sherpa engine: streaming ASR (live
// preview), offline ASR (accurate final pass), and punctuation restoration.
var All = []Model{
	{
		Label: "流式 ASR (实时预览)",
		Dir:   "sherpa-onnx-streaming-paraformer-bilingual-zh-en",
		URL:   "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-streaming-paraformer-bilingual-zh-en.tar.bz2",
		Files: []string{"encoder.int8.onnx", "decoder.int8.onnx", "tokens.txt"},
	},
	{
		Label: "离线 ASR (精确识别)",
		Dir:   "sherpa-onnx-paraformer-zh-2023-09-14",
		URL:   "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-paraformer-zh-2023-09-14.tar.bz2",
		Files: []string{"model.int8.onnx", "tokens.txt"},
	},
	{
		Label: "标点恢复",
		Dir:   "sherpa-onnx-punct-ct-transformer-zh-en-vocab272727-2024-04-12",
		URL:   "https://github.com/k2-fsa/sherpa-onnx/releases/download/punctuation-models/sherpa-onnx-punct-ct-transformer-zh-en-vocab272727-2024-04-12.tar.bz2",
		Files: []string{"model.onnx"},
	},
}

// senseVoiceModels lists the single bundle used by the local SenseVoice engine.
// One model covers ASR plus punctuation (via inverse text normalization), so it
// replaces the sherpa engine's separate offline-ASR and punctuation models.
var senseVoiceModels = []Model{
	{
		Label: "SenseVoice (识别+标点)",
		Dir:   "sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17",
		// Slimmed repackage of the official k2-fsa bundle: only the int8 model
		// and tokens this engine actually loads, dropping the unused 895 MB
		// fp32 model.onnx and test files, cutting the download from ~700 MB to
		// ~155 MB. Same archive format and inner directory as upstream, so the
		// extractor and SenseVoicePathsFor are unaffected.
		URL:   "https://github.com/Ericwyn/linux-voice-input-tool/releases/download/sensevoice-int8-min/sensevoice-int8-min.tar.bz2",
		Files: []string{"model.int8.onnx", "tokens.txt"},
	},
}

// ForEngine returns the model bundles a given engine needs under the base
// directory. Engines that use no local models (e.g. the remote whisper API, or
// an unknown engine name) return nil, for which Check and DownloadMissing are
// no-ops.
func ForEngine(engineName string) []Model {
	switch engineName {
	case "sherpa":
		return All
	case "sensevoice":
		return senseVoiceModels
	default:
		return nil
	}
}

// SherpaPaths holds the six concrete model file paths the recognizer needs.
type SherpaPaths struct {
	Encoder       string
	Decoder       string
	Tokens        string
	OfflineModel  string
	OfflineTokens string
	PunctModel    string
}

// Paths derives the six model file paths from a single base directory.
func Paths(baseDir string) SherpaPaths {
	streaming := filepath.Join(baseDir, All[0].Dir)
	offline := filepath.Join(baseDir, All[1].Dir)
	punct := filepath.Join(baseDir, All[2].Dir)
	return SherpaPaths{
		Encoder:       filepath.Join(streaming, "encoder.int8.onnx"),
		Decoder:       filepath.Join(streaming, "decoder.int8.onnx"),
		Tokens:        filepath.Join(streaming, "tokens.txt"),
		OfflineModel:  filepath.Join(offline, "model.int8.onnx"),
		OfflineTokens: filepath.Join(offline, "tokens.txt"),
		PunctModel:    filepath.Join(punct, "model.onnx"),
	}
}

// SenseVoicePaths holds the two concrete file paths the SenseVoice recognizer
// needs: the quantized model and its tokens table.
type SenseVoicePaths struct {
	Model  string
	Tokens string
}

// SenseVoicePathsFor derives the SenseVoice model file paths from a single base
// directory.
func SenseVoicePathsFor(baseDir string) SenseVoicePaths {
	dir := filepath.Join(baseDir, senseVoiceModels[0].Dir)
	return SenseVoicePaths{
		Model:  filepath.Join(dir, "model.int8.onnx"),
		Tokens: filepath.Join(dir, "tokens.txt"),
	}
}

// MissingFile describes a model file that is expected but not found on disk.
type MissingFile struct {
	Model string
	Path  string
}

// Check reports which files in the given model set are missing under baseDir. A
// nil/empty result means every model in the set is present. A nil/empty set
// (e.g. an engine with no local models) reports nothing missing.
func Check(baseDir string, set []Model) []MissingFile {
	var missing []MissingFile
	for _, model := range set {
		for _, file := range model.Files {
			path := filepath.Join(baseDir, model.Dir, file)
			if info, err := os.Stat(path); err != nil || info.Size() == 0 {
				missing = append(missing, MissingFile{Model: model.Label, Path: path})
			}
		}
	}
	return missing
}

// modelPresent reports whether all of a model's files already exist.
func modelPresent(baseDir string, model Model) bool {
	for _, file := range model.Files {
		path := filepath.Join(baseDir, model.Dir, file)
		if info, err := os.Stat(path); err != nil || info.Size() == 0 {
			return false
		}
	}
	return true
}

// DownloadProgress reports byte-level download progress for one model bundle.
// It tracks the compressed download stream (the actual network transfer), so
// Downloaded/Total are compressed bytes. Total is -1 when the server sends no
// Content-Length.
type DownloadProgress struct {
	Model       string
	Downloaded  int64
	Total       int64
	BytesPerSec float64
}

// String renders progress for display. With a known total it shows a
// percentage; otherwise just the downloaded amount. Both include speed.
func (p DownloadProgress) String() string {
	const mb = 1024.0 * 1024.0
	downloadedMB := float64(p.Downloaded) / mb
	speedMB := p.BytesPerSec / mb
	if p.Total > 0 {
		totalMB := float64(p.Total) / mb
		percent := float64(p.Downloaded) / float64(p.Total) * 100
		return fmt.Sprintf("下载中: %.1f/%.1f MB (%.1f%%) %.1f MB/s",
			downloadedMB, totalMB, percent, speedMB)
	}
	return fmt.Sprintf("下载中: %.1f MB %.1f MB/s", downloadedMB, speedMB)
}

// progressReader wraps an io.Reader, counting bytes read and periodically
// invoking onProgress with the running total and a recent-window speed. Reports
// are throttled to progressReportInterval to avoid flooding the caller.
type progressReader struct {
	reader     io.Reader
	total      int64
	model      string
	onProgress func(DownloadProgress)

	downloaded      int64
	start           time.Time
	lastReportTime  time.Time
	lastReportBytes int64
}

// progressReportInterval is the minimum gap between progress callbacks.
const progressReportInterval = 300 * time.Millisecond

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.downloaded += int64(n)
	}

	now := time.Now()
	if r.onProgress != nil && now.Sub(r.lastReportTime) >= progressReportInterval {
		window := now.Sub(r.lastReportTime).Seconds()
		var speed float64
		if window > 0 {
			speed = float64(r.downloaded-r.lastReportBytes) / window
		}
		r.onProgress(DownloadProgress{
			Model:       r.model,
			Downloaded:  r.downloaded,
			Total:       r.total,
			BytesPerSec: speed,
		})
		r.lastReportTime = now
		r.lastReportBytes = r.downloaded
	}
	return n, err
}

// DownloadMissing downloads and extracts any models in the given set whose
// files are missing under baseDir. status, if non-nil, is called with
// human-readable milestone messages ("开始下载", "完成", ...). progress, if
// non-nil, is called with byte-level download progress for the CLI/UI to render
// speed and percentage. Models already present are skipped. A nil/empty set
// (e.g. an engine with no local models) is a no-op.
func DownloadMissing(baseDir string, set []Model, status func(string), progress func(DownloadProgress)) error {
	report := func(format string, args ...interface{}) {
		if status != nil {
			status(fmt.Sprintf(format, args...))
		}
	}

	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return fmt.Errorf("create model dir: %w", err)
	}

	for _, model := range set {
		if modelPresent(baseDir, model) {
			report("已存在，跳过: %s", model.Label)
			continue
		}
		report("开始下载: %s", model.Label)
		if err := downloadAndExtract(model.URL, baseDir, model.Label, progress); err != nil {
			return fmt.Errorf("download %s: %w", model.Label, err)
		}
		report("完成: %s", model.Label)
	}
	report("全部模型就绪")
	return nil
}

// downloadAndExtract streams a .tar.bz2 from url and extracts it into destDir.
// When progress is non-nil, the compressed download stream is wrapped so speed
// and percentage can be reported as bytes arrive, plus a final 100% report.
func downloadAndExtract(url, destDir, label string, progress func(DownloadProgress)) error {
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}

	var source io.Reader = resp.Body
	var tracker *progressReader
	if progress != nil {
		now := time.Now()
		tracker = &progressReader{
			reader:         resp.Body,
			total:          resp.ContentLength,
			model:          label,
			onProgress:     progress,
			start:          now,
			lastReportTime: now,
		}
		source = tracker
	}

	tarReader := tar.NewReader(bzip2.NewReader(source))
	cleanDest := filepath.Clean(destDir)

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}

		// Guard against path traversal (Zip Slip) from a malicious archive.
		target := filepath.Join(cleanDest, header.Name)
		if target != cleanDest && !strings.HasPrefix(target, cleanDest+string(os.PathSeparator)) {
			return fmt.Errorf("illegal path in archive: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			if err := writeFile(target, tarReader, os.FileMode(header.Mode)); err != nil {
				return err
			}
		}
	}

	// Emit a final report so the display ends at the full downloaded size (and
	// 100%), which the throttled per-read reports may have skipped.
	if tracker != nil {
		elapsed := time.Since(tracker.start).Seconds()
		var avgSpeed float64
		if elapsed > 0 {
			avgSpeed = float64(tracker.downloaded) / elapsed
		}
		progress(DownloadProgress{
			Model:       label,
			Downloaded:  tracker.downloaded,
			Total:       tracker.total,
			BytesPerSec: avgSpeed,
		})
	}
	return nil
}

func writeFile(path string, src io.Reader, mode os.FileMode) error {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode|0600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, src); err != nil {
		return err
	}
	return nil
}
