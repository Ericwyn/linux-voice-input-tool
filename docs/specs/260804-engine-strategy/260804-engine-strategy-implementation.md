# 识别引擎策略化改造实现方案

本文承接 `260804-engine-strategy-overview.md`，给出具体接口、结构体和逐文件改造点。阅读前请先读 overview 的"核心设计"与"风险与待确认问题"。

## 1. 新增 `engine` 包

目录结构：

```text
engine/
  engine.go          // Engine / Session 接口 + 工厂 New
  sherpa_engine.go   // sherpaEngine + sherpaSession
  whisper_engine.go  // whisperEngine + whisperSession
  engine_test.go     // 工厂与 whisper 会话单测
```

### 1.1 接口定义（`engine/engine.go`）

```go
package engine

import (
	"linux-voice-input/audio"
	"linux-voice-input/config"
)

// Engine 是一个长生命周期的识别引擎实例，持有模型或远程客户端等重资源，
// 可被 daemon 预加载并跨多次录音会话复用。所有方法需并发安全。
type Engine interface {
	// Preload 预先加载引擎所需的重资源（如本地模型）。daemon 启动时调用一次。
	// 对无需预加载的引擎（如 whisper）是 no-op。加载部分失败但不致命时应返回
	// nil 并在内部记录日志，交由后续懒加载重试。
	Preload() error

	// NewSession 开启一次录音对应的识别会话。每次开始录音调用一次。
	NewSession() (Session, error)

	// SupportsPreview 表示该引擎是否支持录音过程中的实时预览文本。
	// sherpa 返回 true，whisper 返回 false。Controller 据此决定录音中是否
	// 轮询 PartialText、以及是否在 Finish 前发送"识别中"通知。
	SupportsPreview() bool

	// Close 释放引擎持有的所有重资源。可重复调用。
	Close()
}

// Session 表示一次录音对应的识别会话，持有该次录音的临时状态。
type Session interface {
	// FeedAudio 接收一帧音频（float32，16kHz，[-1,1]）。
	// 需要边录边识别的引擎在此喂给流式解码器；否则为 no-op。
	FeedAudio(samples []float32)

	// PartialText 返回当前的实时预览文本。不支持预览的引擎返回空串。
	PartialText() string

	// Finish 在录音结束后产出最终识别文本。recorder 提供完整音频
	// （整段 float32 采样或落盘 WAV），供离线/远程识别使用。
	Finish(recorder *audio.Recorder) (string, error)

	// Close 释放该次会话的临时资源。可重复调用。
	Close()
}

// New 按配置创建对应引擎。这是新增引擎的唯一接入点。
func New(cfg *config.Config) (Engine, error) {
	switch cfg.API.Engine {
	case "sherpa":
		return newSherpaEngine(cfg), nil
	case "whisper":
		return newWhisperEngine(cfg), nil
	default:
		return nil, fmt.Errorf("unknown engine: %q", cfg.API.Engine)
	}
}
```

设计说明：

1. `Finish` 接收 `*audio.Recorder`，因为 sherpa 需要 `GetSamplesFloat32()`、whisper 需要 `SaveWAV()`，两者都从 recorder 取整段音频。让引擎自己决定取哪种形式，`Controller` 不必预先判断。
2. `SupportsPreview` 让 `Controller` 统一处理"预览 vs 识别中通知"，无需针对引擎类型判断（对应 overview 已确认决策 2）。
3. 通知（`notify.*`）不进入引擎接口，仍由 `Controller` 负责，保持通知逻辑单一来源。

### 1.2 whisper 实现（`engine/whisper_engine.go`）

```go
type whisperEngine struct {
	cfg *config.Config
}

func newWhisperEngine(cfg *config.Config) *whisperEngine {
	return &whisperEngine{cfg: cfg}
}

func (e *whisperEngine) Preload() error       { return nil } // 远程 API 无需预加载
func (e *whisperEngine) SupportsPreview() bool { return false }
func (e *whisperEngine) Close()                {}

func (e *whisperEngine) NewSession() (engine.Session, error) {
	return &whisperSession{cfg: e.cfg}, nil
}

type whisperSession struct {
	cfg *config.Config
}

func (s *whisperSession) FeedAudio(samples []float32) {} // 不支持流式，忽略
func (s *whisperSession) PartialText() string          { return "" }
func (s *whisperSession) Close()                        {}

func (s *whisperSession) Finish(recorder *audio.Recorder) (string, error) {
	tmpFile := filepath.Join(os.TempDir(), "linux-voice-input.wav")
	if err := recorder.SaveWAV(tmpFile); err != nil {
		return "", fmt.Errorf("save wav: %w", err)
	}
	defer os.Remove(tmpFile)

	client := api.NewClient(s.cfg.API.BaseURL, s.cfg.API.Key, s.cfg.API.Model)
	return client.Transcribe(tmpFile)
}
```

对应迁移自当前 `Controller.transcribeWhisper`。注意：原方法里"保存失败 / 识别失败"会直接发通知（`notify.FinalIf`）。改造后 `Finish` 只返回 `error`，通知改由 `Controller` 依据返回的 error 统一发送（见第 3 节）。

### 1.3 sherpa 实现（`engine/sherpa_engine.go`）

`sherpaEngine` 持有预加载的三个模型，逻辑迁移自 `Controller.loadEngineLocked` / `closeEngineLocked`；`sherpaSession` 持有流式 stream，逻辑迁移自 `StartRecording` 的 sherpa 分支与 `transcribeSherpa`。

```go
type sherpaEngine struct {
	cfg *config.Config
	mu  sync.Mutex

	preStream  *api.SherpaStreamRecognizer
	preOffline *api.SherpaOfflineRecognizer
	prePunct   *api.SherpaPunctuation
}

func newSherpaEngine(cfg *config.Config) *sherpaEngine {
	return &sherpaEngine{cfg: cfg}
}

func (e *sherpaEngine) SupportsPreview() bool { return true }

// Preload 迁移自 Controller.loadEngineLocked：离线/标点加载失败仅记日志，不致命。
func (e *sherpaEngine) Preload() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	streamRecognizer, err := api.NewSherpaStreamRecognizer(e.cfg.Sherpa)
	if err != nil {
		return err
	}
	offlineRecognizer, err := api.NewSherpaOfflineRecognizer(e.cfg.Sherpa)
	if err != nil {
		log.Printf("Offline recognizer unavailable at preload: %v", err)
	}
	var punct *api.SherpaPunctuation
	if e.cfg.Sherpa.PunctModel != "" {
		if punct, err = api.NewSherpaPunctuation(e.cfg.Sherpa.PunctModel); err != nil {
			log.Printf("Punctuation model unavailable at preload: %v", err)
			punct = nil
		}
	}
	e.preStream, e.preOffline, e.prePunct = streamRecognizer, offlineRecognizer, punct
	log.Println("Sherpa engine preloaded for reuse")
	return nil
}
```

`NewSession` 需要复现当前"预加载则复用、否则懒建流式 recognizer 并在会话结束时关闭"的逻辑：

```go
func (e *sherpaEngine) NewSession() (engine.Session, error) {
	e.mu.Lock()
	preStream := e.preStream
	preOffline := e.preOffline
	prePunct := e.prePunct
	e.mu.Unlock()

	recognizer := preStream
	var ownedRecognizer *api.SherpaStreamRecognizer
	if recognizer == nil {
		var err error
		recognizer, err = api.NewSherpaStreamRecognizer(e.cfg.Sherpa)
		if err != nil {
			// 流式不可用：仍返回一个会话，靠 Finish 里的离线模型兜底（保留降级 1）。
			log.Printf("Streaming recognizer init failed: %v", err)
		} else {
			ownedRecognizer = recognizer // one-shot：会话结束需关闭
		}
	}

	var stream *api.SherpaStream
	if recognizer != nil {
		stream = recognizer.CreateStream()
	}

	return &sherpaSession{
		cfg:              e.cfg,
		stream:           stream,
		ownedRecognizer:  ownedRecognizer,
		preOffline:       preOffline,
		prePunct:         prePunct,
	}, nil
}
```

`sherpaSession.Finish` 完整迁移 `transcribeSherpa` 的三条降级（离线不可用回退流式、标点不可用返回无标点、离线懒加载并在本次关闭）：

```go
type sherpaSession struct {
	cfg             *config.Config
	stream          *api.SherpaStream          // 可能为 nil（流式初始化失败）
	ownedRecognizer *api.SherpaStreamRecognizer // 非 nil 时需在 Close 关闭
	preOffline      *api.SherpaOfflineRecognizer
	prePunct        *api.SherpaPunctuation
}

func (s *sherpaSession) FeedAudio(samples []float32) {
	if s.stream != nil {
		s.stream.FeedAudio(samples)
	}
}

func (s *sherpaSession) PartialText() string {
	if s.stream == nil {
		return ""
	}
	return s.stream.GetPartialResult()
}

func (s *sherpaSession) Finish(recorder *audio.Recorder) (string, error) {
	// 迁移自 transcribeSherpa：离线优先，失败回退流式，再加标点。

	// 流式累积结果作为 debug 对照打印（迁移自旧 runTestMode 的分阶段输出）。
	// 日志经 setupLogging 的 MultiWriter 同时进终端与 run.log，测试模式下
	// 也能在终端看到，正式运行时则落盘，替代原先的测试模式特判。
	if s.stream != nil {
		start := time.Now()
		streamingText := s.stream.Finish()
		log.Printf("Streaming result in %dms: %s", time.Since(start).Milliseconds(), streamingText)
	}

	offline := s.preOffline
	ownedOffline := false
	if offline == nil {
		var err error
		if offline, err = api.NewSherpaOfflineRecognizer(s.cfg.Sherpa); err != nil {
			log.Printf("Offline model unavailable, falling back to streaming result: %v", err)
			if s.stream != nil {
				return s.stream.Finish(), nil // 降级 2
			}
			return "", nil
		}
		ownedOffline = true
	}
	if ownedOffline {
		defer offline.Close()
	}

	start := time.Now()
	text, _ := offline.TranscribeAudioSamples(recorder.GetSamplesFloat32())
	log.Printf("Offline recognition done in %dms: %s", time.Since(start).Milliseconds(), text)

	if text == "" || s.cfg.Sherpa.PunctModel == "" {
		return text, nil
	}

	punct := s.prePunct
	ownedPunct := false
	if punct == nil {
		var err error
		if punct, err = api.NewSherpaPunctuation(s.cfg.Sherpa.PunctModel); err != nil {
			log.Printf("Punctuation model unavailable: %v", err)
			return text, nil // 降级 3
		}
		ownedPunct = true
	}
	if ownedPunct {
		defer punct.Close()
	}

	start = time.Now()
	text = punct.AddPunct(text)
	log.Printf("Punctuation done in %dms: %s", time.Since(start).Milliseconds(), text)
	return text, nil
}

func (s *sherpaSession) Close() {
	if s.stream != nil {
		s.stream.Close()
		s.stream = nil
	}
	if s.ownedRecognizer != nil {
		s.ownedRecognizer.Close()
		s.ownedRecognizer = nil
	}
}
```

`sherpaEngine.Close` 迁移 `closeEngineLocked`，依次关闭三个预加载模型并置 nil。

## 2. 重构 `session/session.go`

### 2.1 字段替换

删除：

```go
preStream  *api.SherpaStreamRecognizer
preOffline *api.SherpaOfflineRecognizer
prePunct   *api.SherpaPunctuation
sessionStream     *api.SherpaStream
sessionRecognizer *api.SherpaStreamRecognizer
```

新增：

```go
engine        engine.Engine  // 由 engine.New(cfg) 创建，跨会话复用
activeSession engine.Session // 当前录音会话，非录音时为 nil
feedDone      chan struct{}  // 保留：等待喂帧 goroutine 收尾
```

`New(cfg)` 里调用 `engine.New(cfg)` 初始化 `c.engine`。工厂对未知引擎会返回 error：`New(cfg)` 记日志但不 panic，`c.engine` 置 nil；后续 `Preload` / `StartRecording` 需先判空并给出明确提示（守护进程不至于因配置写错而崩溃）。

### 2.2 方法改造

| 方法 | 改造 |
| --- | --- |
| `Preload` | 删除 `engine != "sherpa"` 特判，直接 `c.engine.Preload()`（whisper 内部 no-op） |
| `loadEngineLocked` / `closeEngineLocked` | 删除，逻辑已下沉到引擎实现 |
| `StartRecording` | 删除 sherpa 特判；统一 `c.activeSession, _ = c.engine.NewSession()`；预览 goroutine 改为喂 `activeSession.FeedAudio`，且仅当 `c.engine.SupportsPreview()` 时轮询 `PartialText` 并发通知 |
| `StopAndTranscribe` | 用 `activeSession` 替代 `sessionStream`/`sessionRecognizer`；`text := c.transcribe(recorder)` |
| `transcribe` / `transcribeSherpa` / `transcribeWhisper` | 合并为一个：`text, err := c.activeSession.Finish(recorder)`，按 err 发失败通知；不支持预览的引擎在调用前发"识别中"通知 |
| `Reload` | 删除 sherpa 特判：`c.engine.Close(); c.engine, _ = engine.New(cfg)`；若原先已预加载则 `c.engine.Preload()` |
| `Close` | `c.engine.Close()` |

预览 goroutine 关键片段（替换现有 sherpa 内联逻辑）：

```go
session := c.activeSession
supportsPreview := c.engine.SupportsPreview()
audioStream := recorder.AudioStream
go func() {
	defer close(done)
	var lastPreview string
	var lastNotifyTime time.Time
	for samples := range audioStream {
		session.FeedAudio(samples)
		if !notificationsStart || !supportsPreview {
			continue
		}
		partial := session.PartialText()
		now := time.Now()
		if partial != "" && partial != lastPreview &&
			now.Sub(lastNotifyTime) >= previewNotifyInterval {
			notify.Persistent("语音输入", "录音中: "+partial)
			lastPreview = partial
			lastNotifyTime = now
		}
	}
}()
```

`transcribe` 合并后：

```go
func (c *Controller) transcribe(recorder *audio.Recorder) string {
	if !c.engine.SupportsPreview() {
		notify.If(c.cfg.Notifications.Start, "语音输入", "识别中", false)
	}
	text, err := c.activeSession.Finish(recorder)
	if err != nil {
		notify.FinalIf(c.cfg.Notifications.TranscribeFailed, "语音输入", "识别失败")
		log.Printf("Transcription failed: %v", err)
		return ""
	}
	return text
}
```

## 3. 通知归属调整

当前 whisper 的"保存录音失败 / 识别失败"通知在 `transcribeWhisper` 内部发。改造后 `whisperSession.Finish` 只返回 error，通知统一由 `Controller.transcribe` 依据 error 发送。

保留行为对照：

| 场景 | 当前 | 改造后 |
| --- | --- | --- |
| whisper 保存 WAV 失败 | `SaveFailed` 通知 | `Finish` 返回 err → 统一走 `TranscribeFailed` 通知 |
| whisper API 失败 | `TranscribeFailed` 通知 | 同上 |

注意（已确认）：改造后 whisper 保存失败与识别失败合并为同一条"识别失败"通知，`SaveFailed` 通知类型不再触发。`config.NotificationsConfig.SaveFailed` 字段保留以兼容旧配置，但成为死字段。

## 4. `main.go runTestMode` 改造（已确认走抽象）

测试模式统一改走引擎抽象，不再对 sherpa 特判。原先在 `runTestMode` 里分别打印的"流式 / 离线 / 标点"分阶段耗时，已下沉到 `sherpaSession.Finish` 内部以 `log.Printf` 输出（见 1.3）。由于 `setupLogging` 用 `io.MultiWriter(os.Stderr, file)`，这些日志在测试模式下仍会出现在终端，正式运行时落盘到 `run.log`，两种模式共享同一套分阶段可观测性。

改造后 `runTestMode` 的核心流程（引擎无关）：

```go
func runTestMode(cfg *config.Config) {
	const testDuration = 3 * time.Second

	if err := audio.Init(); err != nil {
		log.Fatalf("Failed to initialize audio: %v", err)
	}
	defer audio.Terminate()

	eng, err := engine.New(cfg)
	if err != nil {
		log.Fatalf("Failed to create engine: %v", err)
	}
	defer eng.Close()

	sess, err := eng.NewSession()
	if err != nil {
		log.Fatalf("Failed to create session: %v", err)
	}
	defer sess.Close()

	recorder := audio.NewRecorder(cfg.Audio.SampleRate)
	fmt.Println("[TEST] 开始录音，持续 3 秒...")
	if err := recorder.Start(); err != nil {
		log.Fatalf("Failed to start recording: %v", err)
	}

	feedDone := make(chan struct{})
	go func() {
		defer close(feedDone)
		for samples := range recorder.AudioStream {
			sess.FeedAudio(samples) // whisper 为 no-op
		}
	}()

	time.Sleep(testDuration)
	fmt.Println("[TEST] 录音结束")
	if err := recorder.Stop(); err != nil {
		log.Fatalf("Failed to stop recording: %v", err)
	}
	<-feedDone

	fmt.Println("[TEST] 开始识别...")
	start := time.Now()
	text, err := sess.Finish(recorder) // sherpa 内部打印分阶段耗时
	if err != nil {
		log.Fatalf("Transcription failed: %v", err)
	}
	fmt.Printf("[TEST] 识别结果 (总耗时 %dms): %s\n", time.Since(start).Milliseconds(), text)
}
```

说明：`test-temp.wav` 现由 whisper 引擎的 `Finish` 内部落到 `os.TempDir()`，测试模式不再单独存 WAV；若仍希望保留 `~/.linux-voice-input/test-temp.wav` 供人工回放，可在 `Finish` 之前额外 `recorder.SaveWAV(testWavPath)`（可选，非必需）。

## 5. 逐文件改动清单

| 文件 | 改动 |
| --- | --- |
| `engine/engine.go` | 新增：接口 + 工厂 |
| `engine/sherpa_engine.go` | 新增：sherpaEngine + sherpaSession |
| `engine/whisper_engine.go` | 新增：whisperEngine + whisperSession |
| `engine/engine_test.go` | 新增：工厂 + whisper 会话单测 |
| `session/session.go` | 重构：字段替换、方法改造、删除 sherpa 内联逻辑 |
| `main.go` | 视待确认项 1 结论，可能不改或小改 `runTestMode` |
| `api/sherpa.go`、`api/whisper.go` | 不改对外签名 |
| `config/config.go` | 不改 |

## 6. 测试细节

`engine/engine_test.go`：

1. `TestNewEngine`：`engine == "sherpa"` 返回 `*sherpaEngine`；`"whisper"` 返回 `*whisperEngine`；未知值返回 error。
2. `TestWhisperSessionNoPreview`：`SupportsPreview()==false`、`PartialText()==""`、`FeedAudio` 不 panic。
3. whisper `Finish` 的 API 调用可用 `httptest.Server` mock `api.Client` 的目标 URL 验证请求构造与响应解析（复用现有 `api.Client`）。

sherpa 的 `Finish` 依赖真实 onnx 模型与 CGO，不做纯单测，靠 overview 的手动验证覆盖。

## 7. 验证命令

```bash
go build -o linux-voice-input .
go test ./engine/...
```

编译需 CGO 与 sherpa 共享库（现有 `go.mod` 已引入）。手动验证 case 见 overview"测试计划"。
