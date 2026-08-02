#!/usr/bin/env bash
#
# Installer for linux-voice-input, shipped inside the self-contained bundle
# produced by `make bundle`. Run it on the target machine — no Go toolchain and
# no make are required, only a POSIX shell and coreutils:
#
#   ./install.sh                              # 每用户安装到 ~/.local（无需 sudo）
#   sudo ./install.sh --prefix /usr/local     # 系统级安装
#
# It copies the binary together with its sherpa-onnx shared libraries into a
# stable application directory, installs the icon and a desktop entry, and
# symlinks the binary into PATH. The binary locates its libraries through a
# baked-in $ORIGIN/lib rpath, which glibc resolves against the binary's *real*
# path; that is why the PATH symlink works without setting LD_LIBRARY_PATH.

set -euo pipefail

APP_NAME="linux-voice-input"
DISPLAY_NAME="Linux Voice Input"

# Resolve the bundle directory (where this script and its payload live) so the
# payload is found regardless of the caller's working directory.
BUNDLE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Default to a per-user prefix so no root is needed; --prefix overrides it.
PREFIX="${HOME}/.local"

while [ "$#" -gt 0 ]; do
	case "$1" in
		--prefix)
			[ "$#" -ge 2 ] || { echo "错误: --prefix 需要一个目录参数" >&2; exit 1; }
			PREFIX="$2"; shift 2 ;;
		--prefix=*)
			PREFIX="${1#*=}"; shift ;;
		-h|--help)
			echo "用法: $0 [--prefix DIR]"
			echo "  --prefix DIR   安装前缀，默认 ~/.local"
			echo "                 系统级安装可用: sudo $0 --prefix /usr/local"
			exit 0 ;;
		*)
			echo "未知参数: $1" >&2
			echo "用法: $0 [--prefix DIR]" >&2
			exit 1 ;;
	esac
done

# Sanity-check the bundle payload before touching the target filesystem.
if [ ! -f "$BUNDLE_DIR/$APP_NAME" ]; then
	echo "错误: 在 $BUNDLE_DIR 找不到二进制 $APP_NAME，请在解压后的分发目录内运行本脚本。" >&2
	exit 1
fi
if [ ! -d "$BUNDLE_DIR/lib" ]; then
	echo "错误: 在 $BUNDLE_DIR 找不到 lib/ 运行库目录，分发包可能不完整。" >&2
	exit 1
fi

BIN_DIR="$PREFIX/bin"
APP_DIR="$PREFIX/share/$APP_NAME"                 # holds the binary + lib/
DESKTOP_DIR="$PREFIX/share/applications"
ICON_DIR="$PREFIX/share/icons/hicolor/512x512/apps"

INSTALLED_BIN="$APP_DIR/$APP_NAME"
SYMLINK="$BIN_DIR/$APP_NAME"
ICON_DEST="$ICON_DIR/$APP_NAME.png"
DESKTOP_FILE="$DESKTOP_DIR/$APP_NAME.desktop"

echo "==> 安装 $DISPLAY_NAME 到 $APP_DIR"
mkdir -p "$APP_DIR" "$BIN_DIR" "$DESKTOP_DIR" "$ICON_DIR"

# Install the binary and its shared libraries as a unit. The lib/ dir is wiped
# first so a reinstall never leaves a stale .so behind.
install -m 0755 "$BUNDLE_DIR/$APP_NAME" "$INSTALLED_BIN"
rm -rf "$APP_DIR/lib"
mkdir -p "$APP_DIR/lib"
cp -a "$BUNDLE_DIR/lib/." "$APP_DIR/lib/"
chmod 0644 "$APP_DIR"/lib/*.so 2>/dev/null || true

# Icon is optional: install it only if the bundle carries one.
if [ -f "$BUNDLE_DIR/icon.png" ]; then
	install -m 0644 "$BUNDLE_DIR/icon.png" "$ICON_DEST"
fi

# Expose the binary on PATH via a symlink. Because $ORIGIN is resolved from the
# real path, the sibling lib/ under APP_DIR is still found through the symlink.
ln -sf "$INSTALLED_BIN" "$SYMLINK"

# Desktop entry launches the daemon using the absolute installed path, so it
# works regardless of PATH or the symlink.
cat > "$DESKTOP_FILE" <<EOF
[Desktop Entry]
Type=Application
Version=1.0
Name=$DISPLAY_NAME
Name[zh_CN]=语音输入
Comment=Lightweight voice input daemon (tray + global hotkey)
Comment[zh_CN]=轻量语音输入守护进程（托盘 + 全局热键）
Exec=$INSTALLED_BIN --daemon
Icon=$APP_NAME
Terminal=false
Categories=Utility;AudioVideo;
Keywords=voice;speech;input;asr;
StartupNotify=false
EOF
chmod 0644 "$DESKTOP_FILE"

# Refresh desktop/icon caches when the tools are available; ignore failures.
command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "$DESKTOP_DIR" || true
command -v gtk-update-icon-cache >/dev/null 2>&1 && gtk-update-icon-cache -q -t -f "$PREFIX/share/icons/hicolor" || true

echo ""
echo "已安装:"
echo "  应用目录: $APP_DIR （含二进制与 lib/）"
echo "  命令链接: $SYMLINK"
echo "  桌面项:   $DESKTOP_FILE"
echo ""
echo "在应用菜单搜索 “$DISPLAY_NAME” 即可以守护进程模式启动，"
echo "或在终端运行 $APP_NAME --daemon。首次使用请运行 $APP_NAME --download-models 下载模型。"
case ":$PATH:" in
	*":$BIN_DIR:"*) ;;
	*) echo "提示: $BIN_DIR 不在 PATH 中，命令行使用前请将其加入 PATH。" ;;
esac
