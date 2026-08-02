# Makefile for linux-voice-input
#
# Common targets:
#   make build      编译二进制到当前目录
#   make install    编译并安装到用户目录（无需 sudo）：
#                     - 二进制  -> ~/.local/bin/
#                     - 图标    -> ~/.local/share/icons/hicolor/512x512/apps/
#                     - 桌面项  -> ~/.local/share/applications/（双击以 --daemon 启动）
#   make uninstall  移除上面安装的所有文件
#   make clean      删除本地编译产物

BINARY := linux-voice-input

# Install under the per-user XDG prefix by default (no root needed). Override
# with e.g. `make install PREFIX=/usr/local` for a system-wide install.
PREFIX ?= $(HOME)/.local
BINDIR := $(PREFIX)/bin
APPDIR := $(PREFIX)/share/applications
ICONDIR := $(PREFIX)/share/icons/hicolor/512x512/apps

# Source icon shipped in the repo and its installed name/path. The 512px PNG is
# installed under the 512x512 hicolor size so the desktop menu icon stays sharp.
SRC_ICON := assets/icon_512px.png
ICON_NAME := $(BINARY)
INSTALLED_BIN := $(BINDIR)/$(BINARY)
INSTALLED_ICON := $(ICONDIR)/$(ICON_NAME).png
DESKTOP_FILE := $(APPDIR)/$(BINARY).desktop

.PHONY: all build install uninstall clean

all: build

build:
	go build -o $(BINARY) .

# install builds the binary, then copies it, the icon, and a generated desktop
# entry into the user's XDG directories. The desktop entry launches the daemon
# mode and uses absolute paths so it works regardless of PATH.
install: build
	@mkdir -p "$(BINDIR)" "$(APPDIR)" "$(ICONDIR)"
	install -m 0755 "$(BINARY)" "$(INSTALLED_BIN)"
	install -m 0644 "$(SRC_ICON)" "$(INSTALLED_ICON)"
	@printf '%s\n' \
		'[Desktop Entry]' \
		'Type=Application' \
		'Version=1.0' \
		'Name=Linux Voice Input' \
		'Name[zh_CN]=语音输入' \
		'Comment=Lightweight voice input daemon (tray + global hotkey)' \
		'Comment[zh_CN]=轻量语音输入守护进程（托盘 + 全局热键）' \
		'Exec=$(INSTALLED_BIN) --daemon' \
		'Icon=$(INSTALLED_ICON)' \
		'Terminal=false' \
		'Categories=Utility;AudioVideo;' \
		'Keywords=voice;speech;input;asr;' \
		'StartupNotify=false' \
		> "$(DESKTOP_FILE)"
	@chmod 0644 "$(DESKTOP_FILE)"
	@command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "$(APPDIR)" || true
	@command -v gtk-update-icon-cache >/dev/null 2>&1 && gtk-update-icon-cache -q -t -f "$(PREFIX)/share/icons/hicolor" || true
	@echo "已安装:"
	@echo "  二进制: $(INSTALLED_BIN)"
	@echo "  图标:   $(INSTALLED_ICON)"
	@echo "  桌面项: $(DESKTOP_FILE)"
	@echo ""
	@echo "在应用菜单中搜索 “Linux Voice Input” 即可点击以守护进程模式启动。"
	@case ":$$PATH:" in *":$(BINDIR):"*) ;; *) echo "提示: $(BINDIR) 不在 PATH 中，命令行使用请将其加入 PATH。";; esac

uninstall:
	rm -f "$(INSTALLED_BIN)"
	rm -f "$(INSTALLED_ICON)"
	rm -f "$(DESKTOP_FILE)"
	@command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "$(APPDIR)" || true
	@command -v gtk-update-icon-cache >/dev/null 2>&1 && gtk-update-icon-cache -q -t -f "$(PREFIX)/share/icons/hicolor" || true
	@echo "已移除 $(BINARY) 的二进制、图标与桌面项。"

clean:
	rm -f "$(BINARY)"
