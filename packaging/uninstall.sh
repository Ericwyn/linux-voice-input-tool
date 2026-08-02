#!/usr/bin/env bash
#
# Uninstaller for linux-voice-input. It reverses install.sh, removing the
# application directory, the PATH symlink, the desktop entry, and the icon.
# It does NOT remove the user config/models under ~/.linux-voice-input.
#
#   ./uninstall.sh                           # 从 ~/.local 卸载
#   sudo ./uninstall.sh --prefix /usr/local  # 从系统级前缀卸载
#
# The prefix must match the one used at install time.

set -euo pipefail

APP_NAME="linux-voice-input"
DISPLAY_NAME="Linux Voice Input"

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
			echo "  --prefix DIR   与安装时相同的前缀，默认 ~/.local"
			exit 0 ;;
		*)
			echo "未知参数: $1" >&2
			echo "用法: $0 [--prefix DIR]" >&2
			exit 1 ;;
	esac
done

APP_DIR="$PREFIX/share/$APP_NAME"
SYMLINK="$PREFIX/bin/$APP_NAME"
ICON_DEST="$PREFIX/share/icons/hicolor/512x512/apps/$APP_NAME.png"
DESKTOP_FILE="$PREFIX/share/applications/$APP_NAME.desktop"

echo "==> 卸载 $DISPLAY_NAME（前缀: $PREFIX）"
rm -rf "$APP_DIR"
rm -f "$SYMLINK"
rm -f "$ICON_DEST"
rm -f "$DESKTOP_FILE"

command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "$PREFIX/share/applications" || true
command -v gtk-update-icon-cache >/dev/null 2>&1 && gtk-update-icon-cache -q -t -f "$PREFIX/share/icons/hicolor" || true

echo "已移除二进制、运行库、命令链接、图标与桌面项。"
echo "用户配置与模型（~/.linux-voice-input）保留未动，如需清理请手动删除。"
