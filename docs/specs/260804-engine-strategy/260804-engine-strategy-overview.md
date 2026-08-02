# 识别引擎策略化改造方案概述

## 背景

当前项目支持两种识别引擎：

1. `sherpa`：本地三模型 pipeline（流式 Paraformer 实时预览 + 离线 Paraformer-large 精确识别 + CT-Transformer 标点恢复），完全离线。
2. `whisper`：在线 API（SiliconFlow），上传整段音频后返回文本。

两种引擎的差异目前是通过散落在多处的 `if cfg.API.Engine == "sherpa"` 条件分支来处理的，并且 sherpa 引擎的私有状态被直接塞进了通用的 `session.Controller` 里。

这带来几个问题：

1. **引擎判断分散**：`Preload`、`StartRecording`、`transcribe`、`Reload`，以及 `main.go` 的 `runTestMode` 都各自判断一次引擎类型，新增引擎需要同时改动这些地方，容易漏改。
2. **通用控制器被污染**：`Controller` 结构体直接持有 sherpa 专属字段（`preStream`、`preOffline`、`prePunct`、`sessionStream`、`sessionRecognizer`、`feedDone`），whisper 完全用不到；控制器越来越难维护。
3. **扩展成本高**：想接入新引擎（例如另一个本地模型、另一个云 API）时，没有清晰的接入点，只能继续往控制器里堆 `if/else` 和新字段。

如果不做，随着引擎数量增加，`Controller` 会持续膨胀，引擎相关逻辑和录音生命周期逻辑会越缠越紧。

## 当前现状

### 主链路

录音与识别的核心控制器是 `session.Controller`（`session/session.go`），被守护进程和一次性进程共用。

```text
Toggle
  -> StartRecording
       -> 获取录音锁 / 启动 recorder
       -> [engine==sherpa] 创建流式 stream + 启动预览 goroutine（喂音频帧 + 节流通知）
  -> StopAndTranscribe
       -> 停止 recorder / 等待预览 goroutine 结束
       -> transcribe
            -> [engine==sherpa] transcribeSherpa：离线识别 + 标点
            -> [else]         transcribeWhisper：存 WAV + 调 API
       -> PasteText 粘贴
```

### 关键入口与判断点

引擎类型判断（`cfg.API.Engine == "sherpa"`）目前出现在以下位置：

| 位置 | 作用 |
| --- | --- |
| `Controller.Preload` | 仅 sherpa 需要预加载模型，whisper 直接返回 |
| `Controller.StartRecording` | 仅 sherpa 需要建流 + 起预览 goroutine |
| `Controller.transcribe` | 分流到 `transcribeSherpa` / `transcribeWhisper` |
| `Controller.Reload` / `loadEngineLocked` | 重建预加载的 sherpa 模型 |
| `main.go runTestMode` | 测试模式下 sherpa 分阶段识别并打印各阶段耗时 |

### 关键数据结构

`Controller` 中的引擎相关字段：

```go
// 预加载的 sherpa 组件，daemon 模式复用；one-shot 模式为 nil
preStream  *api.SherpaStreamRecognizer
preOffline *api.SherpaOfflineRecognizer
prePunct   *api.SherpaPunctuation

// 当前录音会话状态
sessionStream     *api.SherpaStream
sessionRecognizer *api.SherpaStreamRecognizer // one-shot 模式下需在会话结束后关闭
feedDone          chan struct{}
```

底层 recognizer 封装位于 `api/sherpa.go`（三个 sherpa 模型）和 `api/whisper.go`（在线 API 客户端），这两层本身封装是清晰的，问题只在于上层控制器直接依赖它们并硬编码分支。

### 当前 fallback / 异常处理

sherpa 引擎有多级降级，改造时必须完整保留：

1. `StartRecording` 时流式 recognizer 初始化失败 → 记录日志，继续录音（离线模型仍可能识别成功）。
2. `Finish` 时离线模型不可用 → 回退到流式识别的累积结果。
3. 标点模型不可用 → 返回不带标点的文本。
4. `Preload` 时离线 / 标点模型加载失败 → 记录日志但不视为致命，后续懒加载重试。

### 当前问题点

引擎逻辑与录音生命周期逻辑耦合，缺少统一抽象；新增引擎缺乏明确接入点。

## 目标

1. 抽象出统一的引擎接口（策略），把"如何识别"从 `Controller`（负责"何时录音/何时粘贴"）中剥离。
2. 让 `sherpa` 和 `whisper` 分别成为该接口的两个独立实现，各自封装自己的模型/客户端、私有状态和降级逻辑。
3. `Controller` 不再出现任何 `cfg.API.Engine == "..."` 判断，也不再持有任何引擎专属字段。
4. 提供一个引擎工厂（按配置创建引擎实例），成为新增引擎的唯一接入点。
5. 完整保留现有的所有对外行为：实时预览通知、多级降级、daemon 预加载复用、one-shot 懒加载、`Reload` 热重载、测试模式。

## 非目标

1. 不新增任何真实引擎（本次只做抽象，为后续接入铺路）。
2. 不改动底层 recognizer 封装 `api/sherpa.go`、`api/whisper.go` 的对外方法签名（引擎实现内部复用它们）。
3. 不改动配置文件格式（`api.engine` 字段语义不变，旧配置继续可用）。
4. 不改动录音、粘贴、热键、托盘、通知等模块的行为。
5. 不改动 socket 通信协议和进程模型（daemon / one-shot 保持不变）。

## 核心设计

### 总体原则

引入 `engine` 包，定义两层抽象：

1. `Engine`：一个长生命周期的引擎实例，负责持有/加载重资源（模型或客户端），并按需创建识别会话。
2. `Session`：一次录音对应的识别会话，负责接收音频帧、提供实时预览、产出最终文本。

`Controller` 只依赖这两个接口，通过工厂 `engine.New(cfg)` 拿到具体引擎，完全不感知底层是本地模型还是远程 API。

### 为什么拆成 Engine + Session 两层

两种引擎的差异集中在两点：**是否需要预加载重资源**、**是否需要边录边识别**。单层接口难以同时优雅表达"长期持有的模型"和"每次录音的临时状态"。拆成两层后：

- 重资源（sherpa 的三个模型、whisper 的 HTTP 客户端配置）由 `Engine` 持有，可被 daemon 预加载并跨会话复用。
- 每次录音的临时状态（sherpa 的流式 stream、累积文本）由 `Session` 持有，会话结束即释放。

这与当前 `Controller` 里"预加载字段 vs 会话字段"的划分天然对应，迁移直观。

### 音频流的抽象

sherpa 需要边录边喂帧（实时预览 + 端点分段），whisper 只在录音结束后处理整段音频。接口用统一方式表达：

- 录音过程中，`Controller` 把每一帧音频交给 `Session.FeedAudio`。whisper 的实现可以直接忽略（no-op），sherpa 的实现喂给流式 stream。
- 录音过程中，`Controller` 周期性调用 `Session.PartialText` 取实时预览文本。whisper 返回空串（无预览），sherpa 返回流式累积结果。
- 录音结束后，`Controller` 调用 `Session.Finish(recorder)` 拿最终文本。sherpa 用离线模型重识别整段音频（失败则回退流式结果）+ 标点；whisper 存 WAV 后调 API。

预览通知的节流逻辑（每 300ms 一次、文本变化才更新）仍留在 `Controller`，属于通知职责，不下沉到引擎。这样通知逻辑保持单一来源。

### 新旧方案对比

| 维度 | 当前（if/else） | 策略模式 |
| --- | --- | --- |
| 引擎判断 | 分散在 5 处 | 集中在 `engine.New` 工厂 1 处 |
| Controller 字段 | 6 个 sherpa 专属字段 | 2 个通用字段（`engine` / `session`） |
| 新增引擎 | 改控制器多处 + 加字段 | 新增一个文件实现接口 + 工厂加一行 |
| 引擎私有降级逻辑 | 混在控制器方法里 | 封装在各引擎实现内部 |

### 替代方案

| 方案 | 优点 | 缺点 | 结论 |
| --- | --- | --- | --- |
| A. 保持 if/else，仅抽公共函数 | 改动最小 | 没解决扩展性，Controller 仍持有引擎字段 | 不选 |
| B. 单层 Engine 接口（无 Session） | 接口更简单 | 预加载复用与每次会话状态难分离，sherpa 的流式 stream 无处安放 | 不选 |
| C. Engine + Session 两层（本方案） | 职责清晰，契合现有字段划分，扩展点单一 | 引入一个新包和两个接口 | 采用 |

## 影响范围

1. 新增 `engine` 包（接口 + 工厂 + sherpa/whisper 两个实现）。
2. 重构 `session/session.go`：删除引擎专属字段与分支，改为依赖 `engine.Engine` / `engine.Session`。
3. 调整 `main.go runTestMode` 统一走引擎接口（`NewSession` → 喂音频 → `Finish`），分阶段耗时改由 sherpa 引擎内部日志输出。
4. `api/sherpa.go`、`api/whisper.go` 不改对外签名，仅被 `engine` 包内部调用。

保持不变的行为：配置格式、socket 协议、录音/粘贴/热键/托盘/通知、daemon 与 one-shot 进程模型、sherpa 全部降级路径。

## 兼容与降级

1. **配置兼容**：`api.engine` 仍是 `"sherpa"` / `"whisper"`，旧配置零改动可用。未知引擎值由工厂返回明确错误。
2. **降级保留**：sherpa 的四条降级路径（见"当前 fallback"）在 sherpa 引擎实现内部原样保留。
3. **懒加载 / 预加载**：daemon 调用 `Engine.Preload` 一次性加载并复用；one-shot 不预加载，引擎内部懒加载并缓存，保持一次性进程的启动速度。
4. **回滚**：本次为纯内部重构，不涉及数据或协议变更。回滚只需还原代码，无脏数据风险。

## 可观测性

1. 保留现有日志：预加载完成、各模型加载失败告警、离线/标点耗时、识别结果。
2. 引擎工厂在创建时记录一条引擎类型日志（现有 `main.go` 已打印 `Engine: %s`，保持）。
3. 降级发生时（离线不可用回退流式、标点不可用）保留原有 warn 级日志。

## 测试计划

1. **单元测试**：为 `engine.New` 工厂加测试（sherpa / whisper / 未知值）；为 whisper 引擎的 `Session` 加测试（FeedAudio 为 no-op、PartialText 返回空、Finish 走 API mock）。
2. **手动验证**：
   - sherpa daemon 模式：预加载 → 录音实时预览 → 停止精确识别 + 标点 → 粘贴。
   - sherpa one-shot 模式：无预加载路径下完整跑通。
   - sherpa 降级：删除离线模型文件，确认回退流式结果；删除标点模型，确认返回无标点文本。
   - whisper 模式：配置 API Key，录音 → "识别中" → 粘贴。
   - `--test` 测试模式在两种引擎下均能输出结果。
   - `Reload`：设置窗口切换引擎后热重载生效。
3. **回归**：热键 debounce、录音过短忽略、剪贴板恢复等不受影响。

## 建议实施顺序

1. 定义 `engine` 包的接口与工厂骨架。
2. 实现 `whisperEngine`（较简单，无预加载/无流式）。
3. 实现 `sherpaEngine`（迁移预加载、流式、离线、标点、全部降级）。
4. 重构 `Controller` 依赖接口，删除引擎专属字段与分支。
5. 调整 `runTestMode`。
6. 编译 + 单测 + 按测试计划手动验证。

## 已确认决策

以下问题已与需求方确认，作为实现的既定前提：

1. **测试模式与分阶段耗时**：`runTestMode` 统一改走引擎抽象（`NewSession` → 喂音频 → `Finish`），不再对 sherpa 特判。分阶段耗时（流式预览结果、离线识别、标点）下沉到 **sherpa 引擎内部**，由引擎自己以日志形式打印。日志经现有 `setupLogging` 的 `io.MultiWriter(os.Stderr, file)` 同时输出到终端 stderr 与 `run.log`，因此测试模式仍能在终端看到分阶段细节，正式运行时也会记录，两种模式共享同一套可观测性。
2. **whisper "识别中"通知**：接口增加 `SupportsPreview() bool`。`Controller` 据此决定录音中是否轮询预览、以及是否在 `Finish` 前发"识别中"通知。通知逻辑集中在 `Controller`。
3. **包命名**：新包命名为 `engine`。
4. **whisper 失败通知合并**：接受把"保存录音失败"与"识别失败"合并为同一条"识别失败"通知。`Notifications.SaveFailed` 配置字段随之变为未使用（保留字段以兼容旧配置，不再触发）。

## 风险

1. `SaveFailed` 通知类型改造后不再触发，配置字段保留但失效。属可接受的行为细微变化。
2. sherpa 引擎的分阶段耗时依赖日志观测，无结构化 telemetry。当前够用；若后续需要指标化，可作为独立优化，不阻塞本次改造。

具体接口签名、结构体、逐文件改造点见 `260804-engine-strategy-implementation.md`。
