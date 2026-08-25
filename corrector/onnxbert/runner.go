package onnxbert

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	ort "github.com/yalue/onnxruntime_go"
)

var runtimeState struct {
	sync.Mutex
	path string
}

func ensureRuntime(path string) (string, error) {
	path = resolveRuntimeLibrary(path)
	runtimeState.Lock()
	defer runtimeState.Unlock()
	if ort.IsInitialized() {
		if runtimeState.path != "" && path != "" && runtimeState.path != path {
			return "", fmt.Errorf("ONNX Runtime already initialized from %s, cannot switch to %s", runtimeState.path, path)
		}
		if runtimeState.path == "" {
			runtimeState.path = path
		}
		return runtimeState.path, nil
	}
	if path == "" {
		return "", fmt.Errorf("ONNX Runtime shared library path is empty")
	}
	ort.SetSharedLibraryPath(path)
	if err := ort.InitializeEnvironment(); err != nil {
		return "", fmt.Errorf("initialize ONNX Runtime from %s: %w", path, err)
	}
	runtimeState.path = path
	return path, nil
}

// resolveRuntimeLibrary supports both packaged binaries (configured
// $ORIGIN/lib path) and plain development builds. The main app already links
// libonnxruntime through sherpa-onnx, so Linux /proc/self/maps can reveal the
// exact module-cache path when there is no sibling lib directory.
func resolveRuntimeLibrary(configured string) string {
	if isNonEmptyFile(configured) {
		return configured
	}
	if fromEnvironment := os.Getenv("ONNXRUNTIME_SHARED_LIBRARY_PATH"); isNonEmptyFile(fromEnvironment) {
		return fromEnvironment
	}
	if maps, err := os.Open("/proc/self/maps"); err == nil {
		fromMaps := runtimePathFromMaps(maps)
		_ = maps.Close()
		if isNonEmptyFile(fromMaps) {
			return fromMaps
		}
	}
	return configured
}

func isNonEmptyFile(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

func runtimePathFromMaps(reader io.Reader) string {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		path := fields[len(fields)-1]
		if strings.Contains(filepath.Base(path), "libonnxruntime.so") {
			return path
		}
	}
	return ""
}

type onnxRunner struct {
	session     *ort.DynamicAdvancedSession
	inputNames  []string
	outputNames []string
	mu          sync.Mutex
}

func newONNXRunner(config Config) (Runner, error) {
	started := time.Now()
	runtimePath, err := ensureRuntime(config.RuntimePath)
	if err != nil {
		return nil, err
	}

	options, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("create ONNX session options: %w", err)
	}
	defer options.Destroy()
	if err := options.SetGraphOptimizationLevel(ort.GraphOptimizationLevelEnableAll); err != nil {
		return nil, fmt.Errorf("set ONNX graph optimization: %w", err)
	}
	if err := options.SetIntraOpNumThreads(config.NumThreads); err != nil {
		return nil, fmt.Errorf("set ONNX intra-op threads: %w", err)
	}

	inputInfo, outputInfo, err := ort.GetInputOutputInfoWithOptions(config.ModelPath, options)
	if err != nil {
		return nil, fmt.Errorf("inspect correction model %s: %w", config.ModelPath, err)
	}
	inputNames := make([]string, len(inputInfo))
	for index, info := range inputInfo {
		switch info.Name {
		case "input_ids", "attention_mask", "token_type_ids":
			inputNames[index] = info.Name
		default:
			return nil, fmt.Errorf("unsupported BERT input %q", info.Name)
		}
	}
	if len(outputInfo) == 0 {
		return nil, fmt.Errorf("correction model has no output")
	}
	outputNames := []string{outputInfo[0].Name}

	session, err := ort.NewDynamicAdvancedSession(config.ModelPath, inputNames, outputNames, options)
	if err != nil {
		return nil, fmt.Errorf("load correction model %s: %w", config.ModelPath, err)
	}
	log.Printf("Correction model loaded in %dms: name=%s model=%s runtime=%s ort=%s",
		time.Since(started).Milliseconds(), config.Name, config.ModelPath, runtimePath, ort.GetVersion())
	return &onnxRunner{session: session, inputNames: inputNames, outputNames: outputNames}, nil
}

func (r *onnxRunner) Run(ctx context.Context, inputs Inputs) (Logits, error) {
	if err := ctx.Err(); err != nil {
		return Logits{}, err
	}
	if len(inputs.InputIDs) == 0 || len(inputs.AttentionMask) != len(inputs.InputIDs) || len(inputs.TokenTypeIDs) != len(inputs.InputIDs) {
		return Logits{}, fmt.Errorf("invalid BERT input tensor lengths")
	}

	shape := ort.NewShape(1, int64(len(inputs.InputIDs)))
	values := make([]ort.Value, len(r.inputNames))
	for index, name := range r.inputNames {
		var data []int64
		switch name {
		case "input_ids":
			data = inputs.InputIDs
		case "attention_mask":
			data = inputs.AttentionMask
		case "token_type_ids":
			data = inputs.TokenTypeIDs
		}
		tensor, err := ort.NewTensor(shape, data)
		if err != nil {
			for previous := 0; previous < index; previous++ {
				_ = values[previous].Destroy()
			}
			return Logits{}, fmt.Errorf("create %s tensor: %w", name, err)
		}
		values[index] = tensor
	}
	defer func() {
		for _, value := range values {
			if value != nil {
				_ = value.Destroy()
			}
		}
	}()

	outputs := []ort.Value{nil}
	r.mu.Lock()
	err := r.session.Run(values, outputs)
	r.mu.Unlock()
	if err != nil {
		return Logits{}, fmt.Errorf("run correction model: %w", err)
	}
	if outputs[0] == nil {
		return Logits{}, fmt.Errorf("correction model returned no logits")
	}
	defer outputs[0].Destroy()
	if err := ctx.Err(); err != nil {
		return Logits{}, err
	}

	tensor, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return Logits{}, fmt.Errorf("correction model output is %T, want float32 tensor", outputs[0])
	}
	outputShape := tensor.GetShape()
	if len(outputShape) != 3 || outputShape[0] != 1 {
		return Logits{}, fmt.Errorf("correction model output shape is %v, want [1, sequence, vocabulary]", outputShape)
	}
	data := append([]float32(nil), tensor.GetData()...)
	return Logits{Data: data, SequenceLength: int(outputShape[1]), VocabSize: int(outputShape[2])}, nil
}

func (r *onnxRunner) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session == nil {
		return nil
	}
	err := r.session.Destroy()
	r.session = nil
	return err
}
