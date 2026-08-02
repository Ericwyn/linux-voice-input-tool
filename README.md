# Linux Voice Input

面向 Ubuntu / X11 桌面的轻量语音输入工具。按一次快捷键开始录音，再按一次停止并识别，识别结果自动粘贴到当前光标所在的输入框。

- **本地优先**：默认使用本地 SenseVoice 模型，完全离线，识别 + 标点一步到位。
- **两种触发方式**：Ubuntu 自定义快捷键（无参数运行）或常驻守护进程（`--daemon`，自带全局热键 + 系统托盘 + 图形设置）。
- **三种识别引擎**：本地 SenseVoice（默认）、本地 sherpa 三模型 pipeline、在线 whisper API，可按需切换。

---

## 目录

- [特性](#特性)
- [识别引擎](#识别引擎)
- [快速开始](#快速开始)
- [使用方式](#使用方式)
  - [命令行](#命令行)
  - [图形界面（守护进程 + 托盘）](#图形界面守护进程--托盘)
  - [Ubuntu 快捷键](#ubuntu-快捷键)
- [配置](#配置)
- [排查问题](#排查问题)
- [其他](#其他)

---

## 特性

- 🎙️ **一键录音识别**：按一次开始、再按一次结束，结果直接粘贴到当前输入框。
- 🧠 **本地离线识别**：默认 SenseVoice 单模型，识别 + 标点一体，无需联网、不上传音频。
- ⚡ **实时预览**：录音过程中通过桌面通知实时显示识别文字。
- 🖥️ **系统托盘 + 图形设置**：守护进程模式下常驻后台，模型只加载一次，触发更快。
- 🔌 **可切换引擎**：本地 SenseVoice / 本地 sherpa / 在线 whisper，按需选择。
- 📦 **一键下载模型**：内置 `--download-models`，带下载进度和速度显示。
- 🩺 **配置自检**：`--check-config` 一键检查环境依赖与模型是否就绪。

---

## 识别引擎

| 引擎 | 类型 | 模型数 | 标点 | 联网 | 说明 |
| --- | --- | --- | --- | --- | --- |
| **sensevoice**（默认） | 本地 | 1 | 内置（ITN） | 否 | 单模型完成识别 + 标点，最省内存，支持中/粤/英/日/韩 |
| **sherpa** | 本地 | 3 | 独立模型 | 否 | 流式预览 + 离线精确识别 + 标点恢复三段式 |
| **whisper** | 在线 | — | 服务端 | 是 | 调用 SiliconFlow API，需要 API Key |

### SenseVoice（默认，推荐）

单个非自回归模型一次前向即输出带标点的识别文本。由于没有原生流式能力，录音过程中的实时预览通过「每隔一段时间（默认 1 秒）对已录音频重跑一次识别」实现，预览与最终结果来自同一模型，天然一致。

```text
录音中：定时重跑 SenseVoice → 通知显示实时预览
    ↓ 停止录音
对完整音频跑一次 SenseVoice（识别 + 标点）
    ↓
剪贴板 + Ctrl+V 粘贴
```

### sherpa（三模型 pipeline）

| 阶段 | 模型 | 作用 |
| --- | --- | --- |
| 实时预览 | Streaming Paraformer | 录音中实时显示部分识别结果 |
| 精确识别 | Paraformer-large（离线） | 停止后对完整音频做高准确率识别 |
| 标点恢复 | CT-Transformer | 对识别结果添加标点符号 |

### whisper（在线）

停止录音后把音频上传到 SiliconFlow API 识别。需要在配置中填写 API Key，需要联网。

---

## 快速开始

### 1. 安装系统依赖

Ubuntu / Debian：

```bash
sudo apt install -y \
  portaudio19-dev \
  xclip \
  xdotool \
  libnotify-bin \
  libglib2.0-bin
```

| 依赖 | 用途 |
| --- | --- |
| `portaudio19-dev` | 麦克风录音（编译时需要） |
| `xclip` | 写入 X11 剪贴板 |
| `xdotool` | 获取 / 激活窗口并模拟 `Ctrl+V` |
| `libnotify-bin` | 提供 `notify-send` 桌面通知 fallback |
| `libglib2.0-bin` | 提供 `gdbus`，用于更新和关闭同一条通知 |

> 当前优先支持 **X11**。Wayland 下 `xclip` / `xdotool` 可能不可用，建议使用 X11 会话。

### 2. 构建

需要 Go 1.22+ 与 CGO（sherpa-onnx 的 C 共享库通过 Go 模块自动引入）。

```bash
git clone <this-repo>
cd linux-voice-input
go build -o linux-voice-input .
```

### 3. 下载模型

本地引擎（sensevoice / sherpa）需要模型文件。程序内置下载命令，会把当前引擎所需的模型下载并解压到 `~/.linux-voice-input/models/`，并显示进度与速度：

```bash
./linux-voice-input --download-models
```

默认引擎为 sensevoice，只需下载一个模型（约 230 MB）。若切换到 sherpa 引擎，再次运行该命令会下载 sherpa 所需的三个模型。

> whisper 引擎无需本地模型，运行该命令会提示「无需下载」。

### 4. 验证与运行

检查环境依赖和模型是否就绪：

```bash
./linux-voice-input --check-config
```

录音 3 秒做一次识别，验证整条链路：

```bash
./linux-voice-input --test
```

一切正常后，即可通过 [Ubuntu 快捷键](#ubuntu-快捷键) 或 [守护进程](#图形界面守护进程--托盘) 使用。

---

## 使用方式

### 命令行

```text
linux-voice-input [选项]

选项:
  (无参数)            触发录音开关：有守护进程则转发给它，否则独立运行一次
  --daemon           以常驻守护进程运行（系统托盘 + 全局热键 + 设置窗口）
  --test             测试模式：录音 3 秒并识别，用于验证配置是否正确
  --download-models  下载当前引擎所需的模型（sherpa / sensevoice 引擎需要）
  --check-config     检查配置与环境依赖，并打印报告
  -h, --help         显示帮助信息
```

`--download-models` 输出示例：

```text
模型目录: /home/user/.linux-voice-input/models
开始下载: SenseVoice (识别+标点)
下载中: 45.3/230.1 MB (19.7%) 3.2 MB/s
完成: SenseVoice (识别+标点)
全部模型就绪
```

`--check-config` 输出示例：

```text
配置与环境检查:
  [✓] 显示服务器: X11
  [✓] xclip: /usr/bin/xclip
  [✓] xdotool: /usr/bin/xdotool
  [✓] 桌面通知: gdbus/notify-send 可用
  [✓] 配置文件: /home/user/.linux-voice-input/config.yaml
  [✓] SenseVoice 模型: 全部就绪（/home/user/.linux-voice-input/models）

全部检查通过。
```

### 图形界面（守护进程 + 托盘）

用 `--daemon` 启动常驻后台进程：

```bash
./linux-voice-input --daemon
```

启动后：

- 模型只加载一次并常驻复用，触发识别更快。
- 注册全局热键（默认 `alt+shift+g`），按一次开始录音、再按一次停止并输入。
- 系统托盘菜单：
  - **状态显示**（空闲 / 录音中…）
  - **启用热键监听**（勾选切换，立即生效并保存）
  - **打开设置…**（图形化配置引擎、模型目录、API Key、热键、通知开关等，保存即热重载）
  - **退出**
- 关闭设置窗口只是隐藏，进程继续在后台运行。

设置窗口内可以：切换识别引擎；填写 / 浏览模型目录并点「检查模型」「下载缺失模型」（带进度）；调整各引擎的专属参数（如 sensevoice 的实时预览间隔、sherpa 的断句阈值、whisper 的 API Key）；配置全局热键、采样率、剪贴板恢复与各类通知开关；以及运行「环境检查」。

> **GNOME 托盘图标不显示？** GNOME 默认不显示系统托盘，安装并启用 AppIndicator 扩展：
>
> ```bash
> sudo apt install -y gnome-shell-extension-appindicator
> ```
>
> 安装后在 GNOME「扩展」中启用 “AppIndicator and KStatusNotifierItem Support”，然后重新登录。

### Ubuntu 快捷键

打开 `Settings → Keyboard → Keyboard Shortcuts → Custom Shortcuts`，添加：

- **Name**：`Linux Voice Input`
- **Command**：`/path/to/linux-voice-input`
- **Shortcut**：例如 `Alt+Shift+G`

使用方式：

1. 把光标放到目标输入框。
2. 按一次快捷键开始录音。
3. 再按一次快捷键停止录音并自动输入。

无参数运行时，程序会先探测是否有守护进程在跑：

- **有**：把 toggle 转发给守护进程，复用已加载的模型（快），随即退出。
- **没有**：退化为独立的一次性进程，加载模型 → 录音 → 识别 → 粘贴 → 退出。

因此快捷键方式与守护进程方式可以并存：开了守护进程后，原有的 Ubuntu 快捷键会自动通过 socket 转发给它而变快，无需任何改动。

---

## 配置

首次运行会自动在下面路径创建配置文件（含所有默认值）：

```text
~/.linux-voice-input/config.yaml
```

顶层的 `engine` 字段选择识别引擎，下面每个引擎各有一个同名配置块。通常你只需要改 `engine` 一行，其余保持默认即可（本地引擎的模型路径会自动从模型目录推导）。

### 默认配置（sensevoice）

```yaml
engine: sensevoice          # 识别引擎：sensevoice / sherpa / whisper

sensevoice:
  model_dir: /home/<user>/.linux-voice-input/models
  language: auto            # auto / zh / en / yue / ja / ko
  use_itn: true             # 开启逆文本正则化（含标点）
  num_threads: 4
  preview_interval_ms: 1000 # 实时预览重跑间隔（毫秒）；填 0 关闭预览

audio:
  sample_rate: 16000
hotkey: alt+shift+g
hotkey_enabled: true
restore_clipboard: true
```

### 切换到 sherpa

```yaml
engine: sherpa

sherpa:
  model_dir: /home/<user>/.linux-voice-input/models
  num_threads: 4
  rule1_min_trailing_silence: 2.4  # 较长静音后结束一段话（秒）
  rule2_min_trailing_silence: 1.2  # 较短静音后结束一段话（秒）
```

### 切换到 whisper（在线）

```yaml
engine: whisper

whisper:
  key: sk-xxxxxxxx
  base_url: https://api.siliconflow.cn/v1
  model: FunAudioLLM/SenseVoiceSmall
```

### 主要配置字段

| 字段 | 说明 |
| --- | --- |
| `engine` | 识别引擎：`sensevoice`（默认）/ `sherpa` / `whisper` |
| `sensevoice.model_dir` | SenseVoice 模型所在目录（`model`/`tokens` 路径由它自动推导） |
| `sensevoice.language` | 识别语种：`auto` / `zh` / `en` / `yue` / `ja` / `ko` |
| `sensevoice.use_itn` | 是否开启逆文本正则化（含标点），默认 `true` |
| `sensevoice.num_threads` | 推理线程数，默认 4 |
| `sensevoice.preview_interval_ms` | 实时预览重跑间隔（毫秒）；`> 0` 开启，`0` 或负数关闭 |
| `sherpa.model_dir` | sherpa 三模型所在目录（各模型路径由它自动推导） |
| `sherpa.num_threads` | 推理线程数，默认 4 |
| `sherpa.rule1_min_trailing_silence` | 较长静音断句阈值（秒），默认 2.4 |
| `sherpa.rule2_min_trailing_silence` | 较短静音断句阈值（秒），默认 1.2 |
| `whisper.key` | SiliconFlow API Key，whisper 引擎必填 |
| `whisper.base_url` | API 地址 |
| `whisper.model` | 在线识别模型，默认 `FunAudioLLM/SenseVoiceSmall` |
| `audio.sample_rate` | 录音采样率，默认 16000 |
| `hotkey` | 守护进程全局热键，如 `alt+shift+g`（修饰键：ctrl/alt/shift/super） |
| `hotkey_enabled` | 守护进程启动时是否启用全局热键监听 |
| `restore_clipboard` | 粘贴后是否恢复原剪贴板内容 |

> **旧配置自动迁移**：如果你的 `config.yaml` 还是旧的 `api:` 块格式（`api.engine` / `api.key` 等），程序会在加载时自动迁移到新的 `engine` + `whisper` 布局并回写，无需手动改动。

---

## 排查问题

**没有自动输入 / 粘贴失败**

1. 确认当前是 X11 会话（`echo $XDG_SESSION_TYPE`）。
2. 确认已安装 `xclip` 和 `xdotool`。
3. 手动测试粘贴链路：
   ```bash
   echo -n "测试输入" | xclip -selection clipboard && xdotool key --clearmodifiers ctrl+v
   ```
4. 查看日志：`tail -f ~/.linux-voice-input/run.log`。

**识别结果为空**

1. 运行 `./linux-voice-input --check-config` 确认模型就绪。
2. 运行 `./linux-voice-input --test` 验证识别链路。
3. 缺失模型时运行 `./linux-voice-input --download-models`。

**误触发保护**：录音时长小于 1 秒会被忽略，不识别、不粘贴，并提示「录音过短，已忽略」。

---

## 其他

### 桌面通知

本地引擎录音过程中，通知会持续更新显示实时识别文字；最终结果通知会在几秒后自动关闭。常见状态：

- `录音中: <实时识别文字>`
- `已输入：<最终结果>`
- `录音过短，已忽略`
- `识别失败`

通知开关可在设置窗口中逐项配置。

### 日志

```bash
tail -f ~/.linux-voice-input/run.log
```

日志包含配置信息、各阶段处理耗时、识别结果等，自动保留最近 3 天。

### 开机自启（守护进程）

创建 `~/.config/autostart/linux-voice-input.desktop`：

```ini
[Desktop Entry]
Type=Application
Name=Linux Voice Input
Exec=/path/to/linux-voice-input --daemon
X-GNOME-Autostart-enabled=true
```

### 致谢

本地识别基于 [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) 与阿里 [FunASR](https://github.com/modelscope/FunASR) 的 SenseVoice / Paraformer / CT-Transformer 模型。
