# 本地 SenseVoice 引擎适配实现方案

本文是 `260804-sensevoice-engine-overview.md` 的实现细化，逐文件列出改造点、结构体、接口签名与并发细节。阅读前请先读 overview 的「核心设计」与「兼容与降级」。

## 改动总览

| 文件 | 类型 | 说明 |
| --- | --- | --- |
| `models/models.go` | 改 | 引擎感知：`ForEngine` + `Check`/`DownloadMissing` 带模型集参数 + `SenseVoicePaths` |
| `config/config.go` | 改 | 新增 `SenseVoiceConfig` + 默认值 + 路径推导 + `ModelSetForEngine` 辅助 |
| `api/sensevoice.go` | 新增 | `SenseVoiceRecognizer` 封装 sherpa SenseVoice offline recognizer |
| `engine/sensevoice_engine.go` | 新增 | `senseVoiceEngine` + `senseVoiceSession`（定时重跑伪流式） |
| `engine/engine.go` | 改 | 工厂加 `case "sensevoice"` |
| `engine/engine_test.go` | 改 | 新增用例与编译期断言 |
| `doctor/doctor.go` | 改 | `checkEngine` 加 `sensevoice` 分支 |
| `main.go` | 改 | `runDownloadModels`/`runCheckConfig` 按引擎选模型集；帮助文案补充 |
| `ui/window.go` | 改 | 引擎下拉加 `sensevoice`；模型检查/下载按引擎；专属项最小暴露 |

---

## 1. `models/models.go`（引擎感知）

### 1.1 新增 SenseVoice 模型集

保留现有 `var All`（sherpa 三模型）不动，新增：

```go
// senseVoiceModels lists the single bundle used by the local SenseVoice engine
// (ASR + punctuation via ITN in one model).
var senseVoiceModels = []Model{
    {
        Label: "SenseVoice (识别+标点)",
        Dir:   "sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17",
        URL:   "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17.tar.bz2",
        Files: []string{"model.int8.onnx", "tokens.txt"},
    },
}

// ForEngine returns the model bundles a given engine needs under the base
// directory. Engines with no local models (e.g. whisper) return nil.
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
```

> URL/文件名按 sherpa-onnx `asr-models` release 惯例填写，需联网用 `--download-models` 实测核验（见 overview 风险 1）。

### 1.2 `Check` / `DownloadMissing` 改为带模型集参数

签名变更（这是本次唯一的破坏性签名改动，所有调用方一并更新）：

```go
// 旧: func Check(baseDir string) []MissingFile
func Check(baseDir string, set []Model) []MissingFile

// 旧: func DownloadMissing(baseDir string, progress func(string)) error
func DownloadMissing(baseDir string, set []Model, progress func(string)) error
```

两者内部把原先遍历 `All` 改为遍历传入的 `set`。`set` 为 `nil`/空时：`Check` 返回 nil（无缺失），`DownloadMissing` 直接 report「全部模型就绪」并返回 nil（no-op）。`modelPresent`、`downloadAndExtract`、`writeFile` 不变。

### 1.3 SenseVoice 路径推导

保留 `SherpaPaths` 与 `Paths(baseDir)` 不变，新增：

```go
// SenseVoicePaths holds the concrete file paths the SenseVoice recognizer needs.
type SenseVoicePaths struct {
    Model  string // model.int8.onnx
    Tokens string // tokens.txt
}

func SenseVoicePathsFor(baseDir string) SenseVoicePaths {
    dir := filepath.Join(baseDir, senseVoiceModels[0].Dir)
    return SenseVoicePaths{
        Model:  filepath.Join(dir, "model.int8.onnx"),
        Tokens: filepath.Join(dir, "tokens.txt"),
    }
}
```

### 1.4 调用方更新一览

| 文件:函数 | 旧调用 | 新调用 |
| --- | --- | --- |
| `main.go runDownloadModels` | `DownloadMissing(baseDir, cb)` | `DownloadMissing(baseDir, models.ForEngine(cfg.API.Engine), cb)` |
| `doctor.go checkEngine` | `Check(baseDir)` | `Check(baseDir, models.ForEngine(cfg.API.Engine))` |
| `ui/window.go checkModels` | `Check(baseDir)` | 传入按当前所选引擎的 set（见 §8） |
| `ui/window.go downloadModels` | `DownloadMissing(baseDir, cb)` | 同上带 set |

---

## 2. `config/config.go`

### 2.1 新增 `SenseVoiceConfig`

```go
type SenseVoiceConfig struct {
    // ModelDir 复用与 sherpa 相同的基目录（~/.linux-voice-input/models）。
    // 为独立可配置保留字段；为空时回退到 Sherpa.ModelDir / DefaultModelDir。
    ModelDir string `yaml:"model_dir"`
    Model    string `yaml:"model"`   // model.int8.onnx 绝对路径（由 ModelDir 推导）
    Tokens   string `yaml:"tokens"`  // tokens.txt 绝对路径（由 ModelDir 推导）

    Language   string `yaml:"language"`    // auto/zh/en/yue/ja/ko，默认 auto
    UseITN     bool   `yaml:"use_itn"`     // 默认 true（输出含标点）
    NumThreads int    `yaml:"num_threads"` // 默认 4

    // PreviewIntervalMs 预览重跑周期（毫秒）。>0 开启伪流式预览，<=0 关闭。默认 1000。
    PreviewIntervalMs int `yaml:"preview_interval_ms"`
}
```

加入 `Config`：

```go
type Config struct {
    API           APIConfig           `yaml:"api"`
    Sherpa        SherpaConfig        `yaml:"sherpa"`
    SenseVoice    SenseVoiceConfig    `yaml:"sensevoice"`   // 新增
    Audio         AudioConfig         `yaml:"audio"`
    // ...其余不变
}
```

### 2.2 `DefaultConfig` 默认值

在返回的 `Config` 里补 `SenseVoice` 块（`api.engine` 默认**保持不变**，见待确认问题 1）：

```go
sensePaths := models.SenseVoicePathsFor(baseDir)
// ...
SenseVoice: SenseVoiceConfig{
    ModelDir:          baseDir,
    Model:             sensePaths.Model,
    Tokens:            sensePaths.Tokens,
    Language:          "auto",
    UseITN:            true,
    NumThreads:        4,
    PreviewIntervalMs: 1000,
},
```

### 2.3 `ResolveModelPaths` 推导 SenseVoice 路径

现有该方法在 `ModelDir` 非空时推导 sherpa 六路径。追加：SenseVoice 用 `SenseVoice.ModelDir`（为空则回退 `Sherpa.ModelDir`）推导：

```go
func (c *Config) ResolveModelPaths() {
    if c.Sherpa.ModelDir != "" {
        p := models.Paths(c.Sherpa.ModelDir)
        c.Sherpa.Encoder, c.Sherpa.Decoder, c.Sherpa.Tokens = p.Encoder, p.Decoder, p.Tokens
        c.Sherpa.OfflineModel, c.Sherpa.OfflineTokens, c.Sherpa.PunctModel = p.OfflineModel, p.OfflineTokens, p.PunctModel
    }
    svDir := c.SenseVoice.ModelDir
    if svDir == "" {
        svDir = c.Sherpa.ModelDir
    }
    if svDir != "" {
        sp := models.SenseVoicePathsFor(svDir)
        c.SenseVoice.ModelDir = svDir
        c.SenseVoice.Model, c.SenseVoice.Tokens = sp.Model, sp.Tokens
    }
}
```

旧配置无 `sensevoice` 块时，`yaml.Unmarshal` 到 `DefaultConfig` 之上会保留默认值，`ResolveModelPaths` 再据基目录补全路径，因此**零改动可用**。

### 2.4 引擎基目录辅助

为 CLI/doctor/UI 统一获取「当前引擎的模型基目录」：

```go
// ModelBaseDir returns the model base directory for the configured engine.
func (c *Config) ModelBaseDir() string {
    switch c.API.Engine {
    case "sensevoice":
        if c.SenseVoice.ModelDir != "" {
            return c.SenseVoice.ModelDir
        }
    }
    if c.Sherpa.ModelDir != "" {
        return c.Sherpa.ModelDir
    }
    return DefaultModelDir()
}
```

---

## 3. `api/sensevoice.go`（新增）

封装风格对齐现有 `api/sherpa.go` 的 `SherpaOfflineRecognizer`。

```go
package api

import (
    "fmt"
    "log"
    "strings"
    "sync"

    sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

    "linux-voice-input/config"
)

// SenseVoiceRecognizer wraps a sherpa-onnx OfflineRecognizer configured for the
// SenseVoice model. A single recognizer is reused across many transcriptions;
// its Decode is serialized by mu so background preview re-runs and the final
// pass never touch the underlying C recognizer concurrently.
type SenseVoiceRecognizer struct {
    recognizer *sherpa.OfflineRecognizer
    mu         sync.Mutex
}

func NewSenseVoiceRecognizer(cfg config.SenseVoiceConfig) (*SenseVoiceRecognizer, error) {
    if cfg.Model == "" {
        return nil, fmt.Errorf("sensevoice model path is not configured")
    }
    tokensTxtPath, err := EnsureTokensTxt(cfg.Tokens) // 复用现有 tokens.json→txt 兜底
    if err != nil {
        return nil, fmt.Errorf("prepare sensevoice tokens: %w", err)
    }
    log.Printf("Using SenseVoice tokens file: %s", tokensTxtPath)

    numThreads := cfg.NumThreads
    if numThreads <= 0 {
        numThreads = 4
    }
    language := cfg.Language
    if language == "" {
        language = "auto"
    }
    useItn := 0
    if cfg.UseITN {
        useItn = 1
    }

    c := &sherpa.OfflineRecognizerConfig{}
    c.FeatConfig = sherpa.FeatureConfig{SampleRate: 16000, FeatureDim: 80}
    c.ModelConfig.SenseVoice.Model = cfg.Model
    c.ModelConfig.SenseVoice.Language = language
    c.ModelConfig.SenseVoice.UseInverseTextNormalization = useItn
    c.ModelConfig.Tokens = tokensTxtPath
    c.ModelConfig.NumThreads = numThreads
    c.ModelConfig.Provider = "cpu"
    c.ModelConfig.ModelType = "sense_voice"
    c.DecodingMethod = "greedy_search"

    recognizer := sherpa.NewOfflineRecognizer(c)
    if recognizer == nil {
        return nil, fmt.Errorf("failed to create sensevoice recognizer")
    }
    return &SenseVoiceRecognizer{recognizer: recognizer}, nil
}

// Transcribe runs one offline recognition over the given samples and returns
// the clean text. Language/emotion/event tags are logged (debug) but not part
// of the returned text. Safe for concurrent callers (serialized internally).
func (r *SenseVoiceRecognizer) Transcribe(samples []float32) (string, error) {
    if len(samples) == 0 {
        return "", nil
    }
    r.mu.Lock()
    defer r.mu.Unlock()

    stream := sherpa.NewOfflineStream(r.recognizer)
    defer sherpa.DeleteOfflineStream(stream)

    stream.AcceptWaveform(16000, samples)
    r.recognizer.Decode(stream)
    result := stream.GetResult()

    // 可选调试：result.Lang / result.Emotion / result.Event 仅日志，不进入文本。
    return strings.TrimSpace(result.Text), nil
}

func (r *SenseVoiceRecognizer) Close() {
    r.mu.Lock()
    defer r.mu.Unlock()
    if r.recognizer != nil {
        sherpa.DeleteOfflineRecognizer(r.recognizer)
        r.recognizer = nil
    }
}
```

> 依据已核实的 `v1.13.4` 绑定：`OfflineModelConfig.SenseVoice`（`Model`/`Language`/`UseInverseTextNormalization`）与 `OfflineRecognizerResult.{Text,Lang,Emotion,Event}` 均存在。

---

## 4. `engine/sensevoice_engine.go`（新增）

### 4.1 Engine

```go
type senseVoiceEngine struct {
    cfg *config.Config
    mu  sync.Mutex
    pre *api.SenseVoiceRecognizer // 预加载复用（daemon）；one-shot 为 nil
}

func newSenseVoiceEngine(cfg *config.Config) *senseVoiceEngine {
    return &senseVoiceEngine{cfg: cfg}
}

// SupportsPreview 由预览间隔决定：>0 开启伪流式预览。
func (e *senseVoiceEngine) SupportsPreview() bool {
    return e.cfg.SenseVoice.PreviewIntervalMs > 0
}

func (e *senseVoiceEngine) Preload() error {
    e.mu.Lock()
    defer e.mu.Unlock()
    rec, err := api.NewSenseVoiceRecognizer(e.cfg.SenseVoice)
    if err != nil {
        // 与 sherpa 的 best-effort 预加载一致：记录日志，不视为致命，会话懒加载重试。
        log.Printf("SenseVoice recognizer unavailable at preload: %v", err)
        return nil
    }
    e.pre = rec
    log.Println("SenseVoice engine preloaded for reuse")
    return nil
}

func (e *senseVoiceEngine) NewSession() (Session, error) {
    e.mu.Lock()
    pre := e.pre
    e.mu.Unlock()

    rec := pre
    var owned *api.SenseVoiceRecognizer
    if rec == nil {
        built, err := api.NewSenseVoiceRecognizer(e.cfg.SenseVoice)
        if err != nil {
            // 返回空会话：Finish 返回空文本；不 panic。
            log.Printf("SenseVoice recognizer init failed: %v", err)
            return &senseVoiceSession{}, nil
        }
        rec = built
        owned = built
    }

    s := &senseVoiceSession{
        recognizer:      rec,
        ownedRecognizer: owned,
        previewInterval: time.Duration(e.cfg.SenseVoice.PreviewIntervalMs) * time.Millisecond,
    }
    s.start()
    return s, nil
}

func (e *senseVoiceEngine) Close() {
    e.mu.Lock()
    defer e.mu.Unlock()
    if e.pre != nil {
        e.pre.Close()
        e.pre = nil
    }
}
```

### 4.2 Session（定时重跑 + 丢帧保护）

```go
type senseVoiceSession struct {
    // recognizer 为 nil 表示模型不可用：FeedAudio no-op、PartialText 空、Finish 返回空文本。
    recognizer      *api.SenseVoiceRecognizer
    ownedRecognizer *api.SenseVoiceRecognizer // 非 nil 仅 one-shot，Close 需释放
    previewInterval time.Duration

    mu       sync.Mutex
    buffer   []float32 // 录音累积（FeedAudio 追加）
    preview  string    // 最近一次预览结果缓存
    stopCh   chan struct{}
    doneCh   chan struct{}
    inflight bool // 预览推理是否在进行（丢帧保护）
}

func (s *senseVoiceSession) start() {
    if s.recognizer == nil || s.previewInterval <= 0 {
        return // 无预览：不起 ticker
    }
    s.stopCh = make(chan struct{})
    s.doneCh = make(chan struct{})
    go s.previewLoop()
}

func (s *senseVoiceSession) FeedAudio(samples []float32) {
    if s.recognizer == nil {
        return
    }
    s.mu.Lock()
    s.buffer = append(s.buffer, samples...)
    s.mu.Unlock()
}

// PartialText 只读缓存，绝不触发推理（controller 每帧都会调用）。
func (s *senseVoiceSession) PartialText() string {
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.preview
}

func (s *senseVoiceSession) previewLoop() {
    defer close(s.doneCh)
    ticker := time.NewTicker(s.previewInterval)
    defer ticker.Stop()
    for {
        select {
        case <-s.stopCh:
            return
        case <-ticker.C:
            s.mu.Lock()
            if s.inflight { // 上次还没跑完，跳过（丢帧保护）
                s.mu.Unlock()
                continue
            }
            snapshot := make([]float32, len(s.buffer))
            copy(snapshot, s.buffer)
            s.inflight = true
            s.mu.Unlock()

            start := time.Now()
            text, err := s.recognizer.Transcribe(snapshot) // 内部有锁，与 Finish 串行
            s.mu.Lock()
            s.inflight = false
            if err == nil && text != "" {
                s.preview = text
            }
            s.mu.Unlock()
            if err != nil {
                log.Printf("SenseVoice preview error: %v", err)
            } else {
                log.Printf("SenseVoice preview in %dms: %s", time.Since(start).Milliseconds(), text)
            }
        }
    }
}

func (s *senseVoiceSession) Finish(recorder *audio.Recorder) (string, error) {
    if s.recognizer == nil {
        return "", nil
    }
    // 停预览循环，等其退出（保证不再触碰 recognizer）。
    if s.stopCh != nil {
        close(s.stopCh)
        <-s.doneCh
        s.stopCh = nil
    }
    start := time.Now()
    text, err := s.recognizer.Transcribe(recorder.GetSamplesFloat32())
    log.Printf("SenseVoice recognition done in %dms: %s", time.Since(start).Milliseconds(), text)
    return text, err
}

func (s *senseVoiceSession) Close() {
    // Finish 未被调用时也要停 loop（幂等）。
    if s.stopCh != nil {
        close(s.stopCh)
        <-s.doneCh
        s.stopCh = nil
    }
    if s.ownedRecognizer != nil {
        s.ownedRecognizer.Close()
        s.ownedRecognizer = nil
    }
}
```

并发要点：

1. `PartialText()` 只加轻锁读 `preview`，不推理，满足「每帧调用必须廉价」。
2. `inflight` 做丢帧保护：长录音单次推理 > 周期时自动跳过后续 tick。
3. `recognizer.Transcribe` 内部 `mu` 串行化 Decode，保证预览 loop 与 `Finish` 不并发调用同一 C recognizer。
4. `Finish`/`Close` 都会停 loop 并等 `doneCh`，避免会话释放后仍有 goroutine 访问已关闭的 recognizer。`stopCh` 置 nil 保证幂等。

---

## 5. `engine/engine.go`（工厂）

```go
func New(cfg *config.Config) (Engine, error) {
    switch cfg.API.Engine {
    case "sherpa":
        return newSherpaEngine(cfg), nil
    case "sensevoice":
        return newSenseVoiceEngine(cfg), nil // 新增
    case "whisper":
        return newWhisperEngine(cfg), nil
    default:
        return nil, fmt.Errorf("unknown engine: %q", cfg.API.Engine)
    }
}
```

---

## 6. `engine/engine_test.go`

1. 编译期断言：`_ Engine = (*senseVoiceEngine)(nil)`、`_ Session = (*senseVoiceSession)(nil)`。
2. `TestNewEngine` 表加一行：`{name: "sensevoice", engineName: "sensevoice", wantType: "*engine.senseVoiceEngine"}`。
3. 新增 `TestSenseVoicePreviewToggle`：`PreviewIntervalMs=1000`→`SupportsPreview()==true`；`=0`→`false`。
4. 新增 `TestSenseVoiceSessionNoModel`：构造模型路径为空的 cfg，`NewSession` 返回空会话；`FeedAudio` 不 panic、`PartialText()==""`、`Finish(空 recorder)` 返回空文本无 panic。（不依赖真实模型，可在 CI 跑。）

---

## 7. `doctor/doctor.go`

`checkEngine` 增加分支（whisper 分支不变）：

```go
func checkEngine(cfg *config.Config) []Result {
    switch cfg.API.Engine {
    case "whisper":
        // ...原样
    case "sensevoice":
        baseDir := cfg.ModelBaseDir()
        missing := models.Check(baseDir, models.ForEngine("sensevoice"))
        if len(missing) == 0 {
            return []Result{{Name: "SenseVoice 模型", OK: true,
                Detail: fmt.Sprintf("全部就绪（%s）", baseDir)}}
        }
        // ...列出缺失（复用现有格式），提示运行 --download-models
    default: // sherpa
        baseDir := cfg.ModelBaseDir()
        missing := models.Check(baseDir, models.ForEngine("sherpa"))
        // ...原样
    }
}
```

---

## 8. `main.go`

### 8.1 `runDownloadModels`

```go
func runDownloadModels(cfg *config.Config) {
    set := models.ForEngine(cfg.API.Engine)
    if len(set) == 0 {
        fmt.Printf("当前引擎为 %q，无需下载本地模型。\n", cfg.API.Engine)
        return
    }
    baseDir := cfg.ModelBaseDir()
    fmt.Printf("模型目录: %s\n", baseDir)
    err := models.DownloadMissing(baseDir, set, func(msg string) { fmt.Println(msg) })
    if err != nil {
        fmt.Printf("下载失败: %v\n", err)
        os.Exit(1)
    }
}
```

### 8.2 `runCheckConfig`

无需改动（走 `doctor.Run` → `checkEngine`，§7 已覆盖 sensevoice）。

### 8.3 帮助文案与启动日志

- `--download-models`/`--check-config` 帮助文案由「仅 sherpa 引擎需要」改为「sherpa / sensevoice 引擎需要」。
- `main()` 里 `cfg.API.Engine == "sherpa"` 的启动日志分支扩展为「本地引擎」（sherpa 或 sensevoice 都打印模型目录/线程），whisper 仍打印 API 信息并做 Key 门禁。

---

## 9. `ui/window.go`

最小改动（首版）：

1. 引擎下拉：`[]string{"sherpa", "sensevoice", "whisper"}`。
2. `updateEngineVisibility`：`sherpa` 与 `sensevoice` 都显示「模型目录 + 检查/下载」组；`whisper` 显示 API 组。
3. `checkModels`/`downloadModels` 传入 `models.ForEngine(form.engineSelect.Selected)`（据当前所选引擎，而非已保存 cfg）。
4. sensevoice 专属项最小暴露：语言（下拉 auto/zh/en/yue/ja/ko）、ITN 开关、预览间隔（毫秒 Entry）。可先仅暴露「预览间隔」，其余用默认值，视 UI 复杂度权衡（见待确认问题 6）。
5. `saveSettings`：收集 sensevoice 字段写入 `newCfg.SenseVoice`，随后 `newCfg.ResolveModelPaths()` 已能推导路径。

---

## 10. 验证顺序（与 overview 测试计划对应）

1. `go build ./...` + `go test ./engine/... ./models/...`（不依赖真实模型）。
2. 配置 `api.engine: sensevoice`，`--check-config` 应报模型缺失并列出路径。
3. `--download-models` 下载 SenseVoice bundle；再 `--check-config` 应全部就绪。**（此步核验 §1.1 的 URL/文件名）**
4. `--test` 录音 3s 出带标点文本。
5. daemon 录音：每秒预览刷新 → 停止得最终文本 → 粘贴。
6. `preview_interval_ms: 0`：无预览 + 「识别中」通知。
7. 回归 sherpa/whisper 的下载、检查、录音、`Reload` 切换。

## 11. 未决依赖

- §1.1 URL/文件名以联网 `--download-models` 实测为准，若官方命名不同，仅需改 `senseVoiceModels` 一处。
- overview「待确认问题」1–6 确认后回填本文的默认值（§2.2）与 UI 范围（§9）。
