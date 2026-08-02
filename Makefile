# Makefile for linux-voice-input
#
# Common targets:
#   make build      编译二进制到当前目录
#   make bundle     生成可分发的自包含目录 + 压缩包（二进制 + .so + 安装脚本）
#   make install    在本机安装为本地应用（复用分发包里的 install.sh）：
#                     - 应用目录（二进制 + lib/） -> ~/.local/share/linux-voice-input/
#                     - 命令链接 -> ~/.local/bin/
#                     - 图标/桌面项 -> ~/.local/share/...（双击以 --daemon 启动）
#   make uninstall  复用 install.sh 的配套脚本卸载上面安装的文件
#   make clean      删除本地编译产物
#
# install/uninstall 与分发包用的是同一套脚本（packaging/install.sh、
# packaging/uninstall.sh），因此本机安装与分发到其他机器的行为完全一致。

BINARY := linux-voice-input

# The sherpa-onnx native .so files are hard NEEDED dependencies of the binary,
# loaded by the dynamic linker at startup. On the build machine the CGO binding
# already bakes in an rpath into the Go module cache, so plain "make build" runs
# fine. To make a binary that also works when shipped to another machine, we add
# an ORIGIN/lib rpath so a lib/ directory placed next to the binary is searched
# first; the module-cache rpath remains as a fallback. In the recipes the flag
# is single-quoted and the make-level $$ collapses to one dollar, so the literal
# token reaches the linker, which resolves it at load time to the binary's dir.
BUILD_CGO_LDFLAGS := -Wl,-rpath,$$ORIGIN/lib

# Distribution layout: dist/<binary>/ holds the runnable bundle (binary + lib/
# + installer scripts), and dist/<binary>-linux-x64.tar.gz is its archived form.
DIST_DIR := dist
BUNDLE_DIR := $(DIST_DIR)/$(BINARY)
BUNDLE_TARBALL := $(DIST_DIR)/$(BINARY)-linux-x64.tar.gz

# Shared-library module and the arch-specific subdirectory that holds the .so
# files. The module directory is resolved at build time via "go list" so the
# version stays in lockstep with go.mod (single source of truth).
SHERPA_LIB_MODULE := github.com/k2-fsa/sherpa-onnx-go-linux
SHERPA_LIB_ARCH := x86_64-unknown-linux-gnu

# Icon shipped in the repo; the bundle carries it as icon.png and install.sh
# places it under the hicolor theme on the target machine.
SRC_ICON := assets/icon_512px.png

# Install prefix passed through to the packaging scripts. Override for a
# system-wide install, e.g. `sudo make install PREFIX=/usr/local`.
PREFIX ?= $(HOME)/.local

.PHONY: all build bundle-dir bundle install uninstall clean

all: build

# build produces a binary that carries an $ORIGIN/lib rpath in addition to the
# module-cache rpath, so the same binary runs both on this machine and inside a
# distributed bundle where the .so files sit in a sibling lib/ directory.
build:
	CGO_LDFLAGS='$(BUILD_CGO_LDFLAGS)' go build -o $(BINARY) .

# bundle-dir assembles the self-contained directory dist/<binary>/ holding the
# binary, the sherpa-onnx shared libraries under lib/, the install/uninstall
# scripts, and an icon. The .so files are copied from the Go module cache, so
# they are exactly the version the binary was linked against (kept in lockstep
# with go.mod) and need no network. Both `make bundle` (which archives it) and
# `make install` (which runs its install.sh) build on top of this target, so
# there is a single place that defines the bundle contents.
bundle-dir: build
	@echo "==> 解析 sherpa-onnx 运行库路径（来自 go module 缓存）"
	@libdir="$$(go list -m -f '{{.Dir}}' $(SHERPA_LIB_MODULE))/lib/$(SHERPA_LIB_ARCH)"; \
	if [ ! -d "$$libdir" ]; then \
		echo "错误: 找不到运行库目录: $$libdir"; \
		echo "请先运行 'go mod download $(SHERPA_LIB_MODULE)'。"; \
		exit 1; \
	fi; \
	echo "    运行库目录: $$libdir"; \
	rm -rf "$(BUNDLE_DIR)"; \
	mkdir -p "$(BUNDLE_DIR)/lib"; \
	install -m 0755 "$(BINARY)" "$(BUNDLE_DIR)/$(BINARY)"; \
	cp -a "$$libdir"/libonnxruntime.so "$$libdir"/libsherpa-onnx-c-api.so "$$libdir"/libsherpa-onnx-cxx-api.so "$(BUNDLE_DIR)/lib/"; \
	chmod 0644 "$(BUNDLE_DIR)"/lib/*.so; \
	install -m 0755 packaging/install.sh "$(BUNDLE_DIR)/install.sh"; \
	install -m 0755 packaging/uninstall.sh "$(BUNDLE_DIR)/uninstall.sh"; \
	install -m 0644 "$(SRC_ICON)" "$(BUNDLE_DIR)/icon.png"

# bundle archives the assembled directory into a .tar.gz for shipping. The
# recipient copies it to any x86_64 Linux machine and runs ./install.sh to
# install it as a local app (no Go and no make required).
bundle: bundle-dir
	@echo "==> 打包 $(BUNDLE_TARBALL)"
	@tar -C "$(DIST_DIR)" -czf "$(BUNDLE_TARBALL)" "$(BINARY)"
	@echo ""
	@echo "已生成自包含分发包:"
	@echo "  目录:   $(BUNDLE_DIR)/         （含二进制、lib/*.so、install.sh）"
	@echo "  压缩包: $(BUNDLE_TARBALL)"
	@echo ""
	@echo "分发给其他机器: 拷贝压缩包过去解压后，进入目录运行 ./install.sh 安装为本地应用（无需 Go / make）。"
	@echo "也可不安装、直接运行 ./$(BINARY)/$(BINARY) --daemon。"

# install assembles the bundle and then runs its install.sh against PREFIX, so
# installing on this machine goes through exactly the same script as a shipped
# bundle. This keeps a single source of truth for how the app is laid out on
# disk (application dir + PATH symlink + icon + desktop entry).
install: bundle-dir
	@echo "==> 运行分发包安装脚本（PREFIX=$(PREFIX)）"
	@"$(BUNDLE_DIR)/install.sh" --prefix "$(PREFIX)"

# uninstall reuses the packaging uninstaller directly (it only needs a prefix,
# not an assembled bundle), so it is symmetric with install and independent of
# any build artifacts.
uninstall:
	@packaging/uninstall.sh --prefix "$(PREFIX)"

clean:
	rm -f "$(BINARY)"
	rm -rf "$(DIST_DIR)"
