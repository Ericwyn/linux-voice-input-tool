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

// MissingFile describes a model file that is expected but not found on disk.
type MissingFile struct {
	Model string
	Path  string
}

// Check reports which model files are missing under baseDir. A nil/empty
// result means every model is present.
func Check(baseDir string) []MissingFile {
	var missing []MissingFile
	for _, model := range All {
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

// DownloadMissing downloads and extracts any models whose files are missing
// under baseDir. progress, if non-nil, is called with human-readable status
// messages. Models already present are skipped.
func DownloadMissing(baseDir string, progress func(string)) error {
	report := func(format string, args ...interface{}) {
		if progress != nil {
			progress(fmt.Sprintf(format, args...))
		}
	}

	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return fmt.Errorf("create model dir: %w", err)
	}

	for _, model := range All {
		if modelPresent(baseDir, model) {
			report("已存在，跳过: %s", model.Label)
			continue
		}
		report("开始下载: %s", model.Label)
		if err := downloadAndExtract(model.URL, baseDir, report); err != nil {
			return fmt.Errorf("download %s: %w", model.Label, err)
		}
		report("完成: %s", model.Label)
	}
	report("全部模型就绪")
	return nil
}

// downloadAndExtract streams a .tar.bz2 from url and extracts it into destDir.
func downloadAndExtract(url, destDir string, report func(string, ...interface{})) error {
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}

	tarReader := tar.NewReader(bzip2.NewReader(resp.Body))
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
