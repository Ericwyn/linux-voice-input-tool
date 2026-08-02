# 本地 SenseVoice 引擎适配方案概述

## 背景

当前本地识别（`sherpa` 引擎）由三个 sherpa-onnx 模型串成一条 pipeline：

1. 流式 Paraformer（`streaming-paraformer-bilingual-zh-en`）：录音过程中做实时预览。
2. 离线 Paraformer（`paraformer-zh-2023-09-14`）：录音结束后做高精度重识别。
3. CT-Transformer 标点（`punct-ct-transformer`）：给离线识别的纯文本补标点。

这条链路能用，但有几个成本：

1. **三模型常驻内存**。daemon 模式预加载后，实测三者加载进内存约 227M（流式 int8）+ 233M（离线 int8）+ 281M（标点）≈ **741M**。
2. **预览与最终结果来自不同模型**。流式 Paraformer 出预览、离线 Paraformer 出最终文本，两者解码路径不同，预览到最终常有跳变，体验不一致。
3. **标点是独立一段**。必须在离线识别之后再跑一个模型，多一次加载与推理。
4. **维护面大**。三个模型、六个文件路径、多级降级逻辑都要维护。

SenseVoice Small 是 FunAudioLLM 开源的非自回归音频理解模型，一次前向即可同时输出 ASR（中/粤/英/日/韩）、语种、情感、声学事件；开启逆文本正则化（ITN）后**直接输出带标点的文本**。它能把上面「离线识别 + 标点」两段合并为一个模型，中文/粤语识别精度普遍不弱于 Paraformer，且推理延迟很低。

因此本次希望新增一个**本地 SenseVoice 引擎**，用单模型覆盖「识别 + 标点」，并探索用「定时重跑」模拟流式预览，从而用一个模型跑通完整 pipeline。

> 说明：现有 `whisper` 引擎的默认配置 `api.model` 已是 `FunAudioLLM/SenseVoiceSmall`，那是走 SiliconFlow **在线 API** 的服务端模型；本次新增的是**本地离线** SenseVoice 引擎，两者是不同链路，互不影响。

## 当前现状

### 引擎策略已就位

上一次重构（提交 `73d93a9`）已把识别引擎抽象为策略模式，核心在 `engine` 包：

```go
// engine/engine.go
type Engine interface {
    Preload() error
    NewSession() (Session, error)
    SupportsPreview() bool
    Close()
}

type Session interface {
    FeedAudio(samples []float32)
    PartialText() string
    Finish(recorder *audio.Recorder) (string, error)
    Close()
}

func New(cfg *config.Config) (Engine, error) // 按 cfg.API.Engine 分发
```

现有两个实现：

| 引擎 | 预加载 | 预览 | Finish 行为 |
| --- | --- | --- | --- |
| `sherpaEngine` | 三模型 | 支持（流式 stream） | 离线重识别 + 标点，多级降级 |
| `whisperEngine` | 无 | 不支持 | 存 WAV → 调远程 API |

`session.Controller` 已完全不含引擎判断，只依赖 `engine.Engine` / `engine.Session`，通过 `SupportsPreview()` 决定录音中是否轮询 `PartialText()`、以及是否在 `Finish` 前发「识别中」通知。

### 录音链路

```text
StartRecording
  -> recorder.Start()（PortAudio，持续把每帧 append 进内部 buffer）
  -> engine.NewSession()
  -> startFeedLoop：for frame := range recorder.AudioStream { session.FeedAudio(frame); 若 SupportsPreview 则节流轮询 PartialText() 发通知 }
StopAndTranscribe
  -> recorder.Stop()（关闭 AudioStream）
  -> 等 feedLoop 结束
  -> session.Finish(recorder)  // recorder.GetSamplesFloat32() 返回从头到尾的完整音频
  -> PasteText 粘贴
```

关键点：**`Recorder` 全程累积完整音频**，`GetSamplesFloat32()` 任意时刻都能拿到「从录音开始到现在」的全量采样。这为「定时对累积音频重跑」提供了现成的数据源。

### 底层绑定已支持 SenseVoice

项目依赖 `github.com/k2-fsa/sherpa-onnx-go-linux v1.13.4`，其绑定已内置 SenseVoice 配置（无需升级依赖）：

```go
// sherpa_onnx.go
type OfflineSenseVoiceModelConfig struct {
    Model                       string
    Language                    string // "auto"/"zh"/"en"/"yue"/"ja"/"ko"
    UseInverseTextNormalization int    // 1 开启 ITN（含标点）
}

type OfflineRecognizerResult struct {
    Text    string
    Lang    string
    Emotion string
    Event   string
    // ...
}
```

即识别文本（`Text`）与语种/情感/事件标签是**分开返回**的，拿到的 `Text` 是干净正文，无需手动清洗 `<|HAPPY|>` 一类标签。

### 模型管理现状

`models` 包目前**硬编码为 sherpa 的三模型**：

- `models.All`（3 个 `Model`）
- `models.Paths(baseDir) SherpaPaths`（推导六个 sherpa 文件路径）
- `models.Check(baseDir) []MissingFile`（只查 `All`）
- `models.DownloadMissing(baseDir, progress)`（只下 `All`）

调用点：

| 位置 | 用途 |
| --- | --- |
| `config.go DefaultConfig/ResolveModelPaths` | 由 `ModelDir` 推导 sherpa 六路径 |
| `main.go runDownloadModels` | `cfg.API.Engine != "sherpa"` 直接返回「无需下载」 |
| `main.go runCheckConfig` → `doctor.Run` | 检查配置与模型 |
| `doctor.go checkEngine` | sherpa 分支查模型、whisper 分支查 Key |
| `ui/window.go` | 引擎下拉仅 `["sherpa","whisper"]`；模型检查/下载按钮 |

即：模型下载/检查目前**只认 sherpa**，SenseVoice 引擎要接进来必须让这层「引擎感知」。

### 当前问题点

要接入本地 SenseVoice，缺三样东西：一个实现 `Engine`/`Session` 的 sensevoice 引擎；一段把「每秒重跑」做成预览的伪流式逻辑；以及把模型管理/配置/doctor/CLI/UI 从「只认 sherpa」改为「按引擎」。

## 目标

1. 新增本地 `sensevoice` 引擎，实现 `engine.Engine` / `engine.Session`，用单模型完成「识别 + 标点（ITN）」，作为 `engine.New` 工厂的第三个分支。
2. 用「定时（默认 1s）对累积音频重跑」实现类流式预览，使预览与最终结果来自**同一模型**、天然收敛。
3. 把 `models` 包改为**引擎感知**：按引擎名返回对应模型集，`Check`/`DownloadMissing`/路径推导都据此工作。
4. 让 `--check-config` 与 `--download-models` 两个 CLI 命令在 `sensevoice` 引擎下正确检查与下载 SenseVoice 模型。
5. `config` 增加 `SenseVoiceConfig`（模型路径、语言、ITN、线程数、预览间隔），并支持由单一 `ModelDir` 推导路径、旧配置零改动可用。
6. 完整保留现有 `sherpa` / `whisper` 两引擎的全部行为不变。

## 非目标

1. **不删除 sherpa / whisper 引擎**。本次是新增第三个引擎并存；虽把新用户默认切到 `sensevoice`（见「已确认决策」1），但已有配置的用户仍保留其原引擎，随时可切回。
2. **不暴露情感 / 事件 / 语种到粘贴文本**。首版只用 `Text` 正文；标签最多写日志，不进入粘贴内容，也不做 UI 展示。
3. **不做 VAD 增量解码 / 尾部切片等预览优化**。首版预览就是「对全量音频重跑 + 丢帧保护」，增量优化列为后续。
4. **不改动录音、粘贴、热键、托盘、通知、socket 协议、进程模型**。
5. **不新增在线 SenseVoice 路径**（在线已由 whisper 引擎覆盖）。
6. **不改 `sherpa` 的模型清单、路径与降级逻辑**。

## 核心设计

### 总体原则

- SenseVoice 是 **offline-only、非自回归**：没有原生流式解码，但单次前向很快（CPU 上短音频通常几十~一两百毫秒）。
- 因此预览不走「真流式」，而走 **「定时对 `Recorder` 已累积的全量音频重跑一次」**：结果只会随音频增长而收敛，不会像双模型那样在预览/最终之间跳变。
- 引擎自持一个离线 recognizer（可被 daemon 预加载复用），会话持有一次录音的累积音频与后台重跑循环。

### 伪流式预览：定时重跑 + 丢帧保护

`senseVoiceSession` 的运作：

```text
NewSession
  -> 启动后台 ticker（周期 = cfg.SenseVoice.PreviewInterval，默认 1s）

录音中（controller 的 feedLoop 驱动）：
  FeedAudio(frame) -> 追加到 session 内部 buffer（mutex 保护）
  PartialText()    -> 直接返回缓存的最近一次识别结果（廉价、非阻塞）

后台 ticker 每次触发：
  若「上一次推理仍在进行」-> 跳过本次（丢帧保护，防止长录音下推理堆积）
  否则 -> 快照当前 buffer -> 跑一次 SenseVoice -> 用 mutex 写入缓存结果

StopAndTranscribe -> Finish(recorder)
  -> 停止 ticker，并等待在跑的那次推理结束（推理互斥锁）
  -> 用 recorder.GetSamplesFloat32() 跑最后一次「权威」全量识别
  -> 返回最终文本
Close -> 停止 ticker、释放本会话自建的 recognizer（若有）
```

要点：

1. **`PartialText()` 必须廉价**。controller 的 feedLoop 每帧（~64ms）都会调 `PartialText()`（节流只作用于「发通知」）。因此推理绝不能放在 `PartialText()` 里，而是后台 ticker 异步跑、`PartialText()` 只读缓存。
2. **丢帧保护**。每次 tick 重跑的是**全量**音频；录音越长单次越慢。若单次推理耗时超过一个 tick 周期，则跳过下一次 tick，预览刷新率随录音增长优雅降级，不会堆积。日常语音输入多为数秒短句，1s 周期下预览顺滑。
3. **最终结果权威且一致**。`Finish` 用 `recorder.GetSamplesFloat32()`（与 sherpa/whisper 相同的数据源）重跑一次作为最终文本，保证与预览同模型、结果收敛。
4. **无需端点检测**。全量重跑天然把整句作为一个不断增长的结果，省掉 sherpa 流式的 Rule1/2/3 分段逻辑。

### 预览可关闭

`SupportsPreview()` 返回 `cfg.SenseVoice.PreviewInterval > 0`：

- `> 0`（默认 1.0s）：开启伪流式预览，行为类似 sherpa。
- `<= 0`：关闭预览，会话不起 ticker，`PartialText()` 恒空；controller 据 `SupportsPreview()==false` 在 `Finish` 前发「识别中」通知，行为类似 whisper。

这样把「要不要花额外 CPU 做预览」交给配置，且完全复用现有 controller 逻辑，无需改 controller。

### 模型管理改为引擎感知

引入「按引擎的模型集」：

```go
// models 包
func ForEngine(engineName string) []Model   // sherpa→3个, sensevoice→1个, 其他→nil
func Check(baseDir string, set []Model) []MissingFile
func DownloadMissing(baseDir string, set []Model, progress func(string)) error
```

调用方先 `set := models.ForEngine(cfg.API.Engine)` 再传入。whisper 等无本地模型的引返回 `nil`，`Check`/`DownloadMissing` 对 `nil` 是 no-op（无缺失、无需下载）。

SenseVoice 模型集（单模型，沿用同一 `models` 基目录）：

| 字段 | 值 |
| --- | --- |
| Label | `SenseVoice (识别+标点)` |
| Dir | `sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17` |
| URL | sherpa-onnx `asr-models` release 下同名 `.tar.bz2` |
| Files | `model.int8.onnx`, `tokens.txt` |

### 新旧链路对比

| 维度 | sherpa（现状） | 本地 sensevoice（本次） |
| --- | --- | --- |
| 模型数 | 3 | 1 |
| 预加载内存 | ≈ 741M | ≈ 230M（int8） |
| 预览来源 | 流式 Paraformer | 同一 SenseVoice（定时重跑） |
| 标点 | 独立 CT-Transformer | 模型内置（ITN） |
| 预览/最终一致性 | 不同模型，易跳变 | 同模型，收敛 |
| 预览粒度 | 逐帧（近逐字） | 每 ~1s 一跳 |
| 语言 | 中英 | 中/粤/英/日/韩 |

### 替代方案

| 方案 | 优点 | 缺点 | 结论 |
| --- | --- | --- | --- |
| A. 直接替换 sherpa 三模型为单 SenseVoice | 最省内存、维护最简 | 破坏性大、失去逐字预览、无法回退 | 不选（改为并存新引擎，用户自选） |
| B. 用 SenseVoice 只替换「离线+标点」，保留流式预览 | 预览逐字 | 仍需两模型；预览/最终仍是不同模型、仍跳变 | 不选（未解决一致性，收益有限） |
| C. 新增本地 sensevoice 引擎 + 定时重跑预览（本方案） | 单模型跑通全链路、预览/最终同源收敛、与现有策略模式契合、可回退 | 预览粒度变粗（每秒一跳）、长录音单次推理变重（丢帧保护缓解） | 采用 |

## 影响范围

1. 新增 `engine/sensevoice_engine.go`（引擎 + 伪流式会话）。
2. 新增 `api/sensevoice.go`（`SenseVoiceRecognizer` 封装 sherpa 的 SenseVoice offline recognizer）。
3. `engine/engine.go` 工厂加 `case "sensevoice"`；`engine/engine_test.go` 加用例与编译期断言。
4. `config/config.go` 新增 `SenseVoiceConfig` + 默认值 + `ResolveModelPaths` 推导 + 引擎基目录辅助。
5. `models/models.go` 改为引擎感知（`ForEngine` + `Check`/`DownloadMissing` 带模型集参数 + `SenseVoicePaths`）。
6. `main.go`：`runDownloadModels` / `runCheckConfig` 按引擎选模型集与基目录。
7. `doctor/doctor.go`：`checkEngine` 增加 `sensevoice` 分支。
8. `ui/window.go`：引擎下拉加 `sensevoice`；模型目录/检查/下载对本地引擎通用；sensevoice 专属项（语言、ITN、预览间隔）最小暴露。

保持不变：录音、粘贴、热键、托盘、通知、socket、进程模型，以及 sherpa / whisper 两引擎的全部现有行为。

## 兼容与降级

1. **配置兼容**：新增 `sensevoice` 配置块，旧配置无此块时用默认值填充；`api.engine` 仍是既有取值，旧配置零改动可用。未知引擎名仍由工厂返回明确错误。
2. **模型缺失降级**：`sensevoice` 引擎在 `Preload` / `NewSession` 时若模型加载失败，记录日志。`NewSession` 返回一个「空会话」（`Finish` 返回空文本并提示识别失败），行为与现有「会话初始化失败」一致，不 panic。
3. **预览失败不影响最终**：后台 ticker 的某次推理若报错，只跳过本次预览刷新（保留上一次缓存），不影响 `Finish` 的最终识别。
4. **推理并发安全**：sherpa `OfflineRecognizer` 的解码用**推理互斥锁**串行化，确保后台 ticker 与 `Finish` 不并发调用同一 recognizer。
5. **回滚**：纯新增引擎，不改数据/协议。回滚只需把 `api.engine` 切回 `sherpa`，或还原代码，无脏数据风险。

## 可观测性

1. 复用 `setupLogging` 的 `io.MultiWriter(os.Stderr, file)`：`--test` 模式下终端可见，正式运行写 `run.log`。
2. 关键日志：模型加载完成/失败；每次最终识别的耗时与文本（与 sherpa 的 `Offline recognition done in %dms` 风格一致）；预览推理耗时（debug 级，便于观察长录音下的丢帧）。
3. 可选：把识别出的 `Lang`/`Emotion`/`Event` 以 debug 日志打印，便于验证模型输出，但不进入粘贴文本。

## 测试计划

1. **单元测试**（`engine/engine_test.go`）：
   - 工厂表新增 `sensevoice` → 返回 `*engine.senseVoiceEngine`。
   - 编译期断言 `senseVoiceEngine`/`senseVoiceSession` 满足接口。
   - `SupportsPreview()`：`PreviewInterval>0` → true，`<=0` → false。
   - 不依赖真实模型：会话在 recognizer 为 nil（模型缺失）时，`FeedAudio` 不 panic、`PartialText` 空、`Finish` 返回空文本+错误或空。
2. **模型管理单测**：`ForEngine("sherpa")` 返回 3 个、`ForEngine("sensevoice")` 返回 1 个、`ForEngine("whisper")`/未知返回 nil；`Check(dir, nil)` 无缺失。
3. **CLI 手动验证**（用户重点）：
   - 配置 `api.engine: sensevoice`。
   - `--check-config`：模型缺失时列出 SenseVoice 缺失文件与路径；存在时报「全部就绪」。
   - `--download-models`：下载并解压 SenseVoice bundle，再次 `--check-config` 通过。
4. **端到端手动验证**：
   - `--test`：录音 3s → 输出识别文本（带标点）。
   - daemon：预加载 → 录音时每秒刷新预览 → 停止得最终文本 → 粘贴。
   - 预览关闭（`preview_interval: 0`）：录音中无预览、`Finish` 前有「识别中」通知。
   - 长录音（>30s）：确认预览刷新变慢但不卡死、最终结果正确（丢帧保护）。
5. **回归**：`sherpa` / `whisper` 两引擎的下载、检查、录音、粘贴、`Reload` 切换引擎均不受影响。

## 上线与回滚

1. 无需灰度/迁移，纯新增引擎。
2. 切换方式：改配置 `api.engine: sensevoice`（或设置窗口下拉），保存后 `Reload` 重建引擎生效。
3. 回滚：切回 `sherpa`/`whisper` 即可；代码回滚无脏数据。

## 建议实施顺序

1. `models` 引擎感知改造（`ForEngine` + `Check`/`DownloadMissing` 带参 + `SenseVoicePaths`），先跑通 CLI 的 check/download。
2. `config` 增加 `SenseVoiceConfig` + 默认值 + 路径推导 + 引擎基目录辅助。
3. `api/sensevoice.go` 封装 `SenseVoiceRecognizer`。
4. `engine/sensevoice_engine.go` 引擎 + 伪流式会话 + 工厂分支 + 单测。
5. `doctor` / `main.go` / `ui` 接入 sensevoice。
6. 编译 + 单测 + 按测试计划验证（CLI 优先）。

## 风险与待确认问题

先列风险，再列需向用户确认的问题。

### 风险

1. **模型 bundle URL 未能联网核验**。当前环境访问 GitHub release 超时，`sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17.tar.bz2` 的 URL 与内部文件名按 sherpa-onnx 官方 `asr-models` release 惯例填写，需在能联网时用 `--download-models` 实测确认。
2. **长录音单次推理变重**。全量重跑在长录音下单次可能超过 1s，预览刷新率下降（已用丢帧保护兜底，不影响最终结果）。
3. **`OfflineRecognizer` 线程安全未文档化**。以推理互斥锁串行化规避；若后续需要并发，再评估。

### 已确认决策

以下问题已与需求方确认，作为实现的既定前提：

1. **默认引擎切换为 `sensevoice`**。`DefaultConfig` 的 `api.engine` 由 `sherpa` 改为 `sensevoice`，新用户开箱即用单模型。已有 `config.yaml` 的用户不受影响（仍保留其原引擎）。
2. **预览默认开启，间隔 1000ms**。`PreviewIntervalMs` 默认 `1000`，配置可调；`<=0` 关闭预览。
3. **语种默认 `auto`**。自动判别中/粤/英/日/韩。
4. **ITN 默认开启**。直接输出带标点文本，替代原独立标点模型。
5. **情感/事件/语种标签仅写日志**。`Lang`/`Emotion`/`Event` 以 debug 日志打印，**不进入**粘贴文本、不做 UI 展示。
6. **UI 最小改动**。设置窗口引擎下拉新增 `sensevoice`，复用「模型目录 + 检查/下载」组，仅额外暴露「预览间隔」；语言/ITN 用默认值，更细的 UI 留到后续。

> 依据 `spec-write-doc.md` 第 6.6 节：以上决策已回填。逐文件改造点见 `260804-sensevoice-engine-implementation.md`。
