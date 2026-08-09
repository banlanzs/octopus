.PHONY: build build-frontend build-backend clean test help

APP_NAME    := octopus

# Windows 原生 GNU Make 不保证提供 sh.exe，因此按平台选择构建脚本。
ifeq ($(OS),Windows_NT)
BUILD_RUNNER := powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/make-build.ps1
else
BUILD_RUNNER := sh scripts/make-build.sh
endif

# 完整构建：前端 SSG → 嵌入 static/out → Go 编译
build:
	@$(BUILD_RUNNER) build

# 前端构建（SSG 静态导出）
build-frontend:
	@$(BUILD_RUNNER) build-frontend

# 仅后端编译（需要 static/out 已存在）
build-backend:
	@$(BUILD_RUNNER) build-backend

# 清理构建产物
clean:
	@$(BUILD_RUNNER) clean

# 运行测试
test:
	@$(BUILD_RUNNER) test

# 帮助信息
help:
	@$(BUILD_RUNNER) help
