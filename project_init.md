# Linux Voice Input - 项目设计文档

## 项目概述

一个 Linux 下的语音输入工具，通过全局快捷键触发录音，调用语音识别 API 将语音转为文字，再通过剪贴板模拟粘贴输入到当前焦点所在的输入框。

## 技术栈

- 语言：Go 1.21+
- UI 框架：Fyne v2
- 音频录制：PortAudio (通过 `github.com/gordonklaus/portaudio`)
- 语音识别：OpenAI Whisper API
- 文字输入：xclip + xdotool

## 整体流程

```
用户按下全局快捷键 (如 Ctrl+Alt+V)
        │
        ▼
弹出悬浮小窗口 (显示"录音中...")
        │
        ▼
开始录制麦克风音频 (PCM -> WAV)
        │
        ▼
用户松开快捷键 / 再次按下快捷键
        │
        ▼
停止录音，显示"识别中..."
        │
        ▼
将音频文件发送至 Whisper API
        │
        ▼
获取识别文本
        │
        ▼
xclip 写入剪贴板 + xdotool 模拟 Ctrl+V 粘贴
        │
        ▼
关闭窗口，完成输入
```

## 模块设计

### 1. 全局快捷键监听

**方案选择：** 使用 X11 的 XGrabKey 机制或读取 `/dev/input/event*` 设备。

考虑的几种方式：

| 方案 | 优点 | 缺点 |
|------|------|------|
| `github.com/BurntSushi/xgb` (X11 协议) | 原生 X11，稳定 | 仅支持 X11，不支持 Wayland |
| `/dev/input/event*` (evdev) | 不依赖显示服务 | 需要 root 或 input 组权限 |
| D-Bus + 桌面环境快捷键注册 | 系统级集成 | 各桌面环境 API 不同 |
| `keybind` 包 (`github.com/BurntSushi/xgbutil`) | 封装好，API 简单 | 仅 X11 |

**推荐方案：** 使用 `github.com/BurntSushi/xgbutil` 的 keybind 包，它对 XGrabKey 有良好的封装。如果需要 Wayland 支持，可后续切换到 evdev 方案。

**默认快捷键：** `Ctrl+Alt+V` (可配置)

### 2. UI 窗口 (Fyne)

使用 Fyne 创建一个小型悬浮窗口，显示当前状态：

- 窗口尺寸：约 200x100，始终置顶
- 状态显示：
  - 🎤 录音中... (红色/跳动动画)
  - ⏳ 识别中...
  - ✅ 输入完成 (短暂显示后自动关闭)
  - ❌ 识别失败 (显示错误信息)

### 3. 音频录制

**依赖：** `github.com/gordonklaus/portaudio`

**系统依赖：** 需要安装 `portaudio19-dev`

```bash
sudo apt install portaudio19-dev
```

**录制参数：**
- 采样率：16000 Hz (Whisper 推荐)
- 通道数：1 (单声道)
- 位深度：16-bit PCM
- 输出格式：WAV 文件 (临时文件)

**核心逻辑：**
```go
// 伪代码
stream, _ := portaudio.OpenDefaultStream(1, 0, 16000, frameSize, callback)
stream.Start()
// ... 录音中 ...
stream.Stop()
// 将 PCM buffer 编码为 WAV 写入临时文件
```

### 4. 语音识别 API

**方案：SiliconFlow API (OpenAI 兼容格式)**

- Endpoint: `POST https://api.siliconflow.cn/v1/audio/transcriptions`
- 模型：`FunAudioLLM/SenseVoiceSmall`
- 认证：Bearer Token
- 请求格式：multipart/form-data
- 支持格式：wav, mp3 等

**请求示例：**
```bash
curl --request POST \
  --url https://api.siliconflow.cn/v1/audio/transcriptions \
  -H "Authorization: Bearer <API_KEY>" \
  -F "file=@audio.wav" \
  -F "model=FunAudioLLM/SenseVoiceSmall"
```

**响应：**
```json
{
  "text": "识别出的文字内容"
}
```

**Go 实现要点：**
- 使用 `mime/multipart` 构建 form-data 请求
- 将录制的 WAV 文件作为 `file` 字段上传
- 解析 JSON 响应提取 `text` 字段

**配置项：**
- `api_key`：SiliconFlow API Key
- `api_base`：API 基础 URL，默认 `https://api.siliconflow.cn/v1`
- `model`：模型名称，默认 `FunAudioLLM/SenseVoiceSmall`

**扩展性：** 由于接口格式与 OpenAI Whisper 兼容，更换 `api_base` 和 `model` 即可切换到其他兼容服务。

### 5. 文字输入 (xclip + xdotool)

**系统依赖：**
```bash
sudo apt install xclip xdotool
```

**实现逻辑：**
```go
// 1. 保存当前剪贴板内容 (可选，用于恢复)
oldClip := exec.Command("xclip", "-selection", "clipboard", "-o").Output()

// 2. 将识别文本写入剪贴板
cmd := exec.Command("xclip", "-selection", "clipboard")
cmd.Stdin = strings.NewReader(recognizedText)
cmd.Run()

// 3. 短暂延迟，确保剪贴板就绪
time.Sleep(50 * time.Millisecond)

// 4. 模拟 Ctrl+V 粘贴
exec.Command("xdotool", "key", "--clearmodifiers", "ctrl+v").Run()

// 5. 恢复原剪贴板内容 (可选)
```

**为什么用 xclip+xdotool 而不是 xdotool type：**
- `xdotool type` 对中文支持差，依赖输入法状态
- 剪贴板方案绕过输入法，直接粘贴 UTF-8 文本，兼容性最好

### 6. 配置文件

路径：`~/.config/linux-voice-input/config.yaml`

```yaml
# API 配置
api:
  key: "sk-xxxxxxxx"
  base_url: "https://api.siliconflow.cn/v1"
  model: "FunAudioLLM/SenseVoiceSmall"

# 快捷键
hotkey: "ctrl+alt+v"

# 录音设置
audio:
  sample_rate: 16000
  max_duration: 60  # 最大录音时长(秒)

# 其他
restore_clipboard: true  # 输入完成后恢复原剪贴板内容
```

## 项目结构

```
linux-voice-input/
├── main.go              # 程序入口
├── go.mod
├── go.sum
├── config/
│   └── config.go        # 配置加载与管理
├── audio/
│   └── recorder.go      # 音频录制
├── api/
│   └── whisper.go       # Whisper API 调用
├── input/
│   └── paste.go         # xclip + xdotool 输入
├── hotkey/
│   └── hotkey.go        # 全局快捷键监听
├── ui/
│   └── window.go        # Fyne UI 窗口
└── project_init.md      # 本文档
```

## 依赖清单

| 包 | 用途 |
|----|------|
| `fyne.io/fyne/v2` | UI 框架 |
| `github.com/gordonklaus/portaudio` | 音频录制 |
| `github.com/BurntSushi/xgbutil` | X11 全局快捷键 |
| `gopkg.in/yaml.v3` | 配置文件解析 |

**系统依赖：**
```bash
sudo apt install portaudio19-dev xclip xdotool libx11-dev libxcursor-dev libxrandr-dev libxinerama-dev libxi-dev libgl1-mesa-dev libxxf86vm-dev
```

## 已确认方案

1. **快捷键触发模式：** 按住快捷键开始录音，松开结束录音（需要监听 KeyPress + KeyRelease 事件）
2. **显示服务：** 仅支持 X11，Wayland 后续再考虑
3. **API 后端：** 仅支持 SiliconFlow，接口格式兼容 OpenAI，后续扩展方便
4. **开机自启：** 不提供 systemd service，用户通过 Ubuntu 桌面自启动配置自行管理

## 下一步

确认以上方案后，我会按以下顺序实现：

1. 初始化项目 + 配置模块
2. 音频录制模块
3. Whisper API 调用
4. xclip/xdotool 输入模块
5. Fyne UI 窗口
6. 全局快捷键 + 串联所有模块
