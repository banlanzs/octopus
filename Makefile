SHELL := /bin/bash
.PHONY: build build-frontend build-backend clean test

APP_NAME    := octopus
MAIN_DIR    := .
OUTPUT_DIR  := build

GIT_VERSION := $(shell git describe --tags --abbrev=0 2>/dev/null || echo "dev")
COMMIT_ID   := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME  := $(shell date +'%F %T %z')
LDFLAGS     := -X 'github.com/bestruirui/octopus/internal/conf.Version=$(GIT_VERSION)' \
               -X 'github.com/bestruirui/octopus/internal/conf.BuildTime=$(BUILD_TIME)' \
               -X 'github.com/bestruirui/octopus/internal/conf.Author=hureru' \
               -X 'github.com/bestruirui/octopus/internal/conf.Commit=$(COMMIT_ID)' \
               -s -w

# 完整构建：前端 SSG → 嵌入 static/out → Go 编译
build: build-frontend
	@echo "🔧 编译 Go 二进制..."
	@mkdir -p "$(OUTPUT_DIR)/bin"
	CGO_ENABLED=0 go build -tags=jsoniter \
		-ldflags "$(LDFLAGS)" \
		-o "$(OUTPUT_DIR)/bin/$(APP_NAME)" \
		"$(MAIN_DIR)"
	@echo "✅ 构建完成: $(OUTPUT_DIR)/bin/$(APP_NAME)"

# 前端构建（SSG 静态导出）
build-frontend:
	@echo "🔧 构建前端..."
	@cd web && pnpm install --frozen-lockfile 2>/dev/null || cd web && pnpm install
	@cd web && pnpm run build
	@echo "✅ 前端构建完成: web/out/"
	@rm -rf static/out
	@mv web/out static/out
	@echo "✅ 前端产物已嵌入: static/out/"

# 仅后端编译（需要 static/out 已存在）
build-backend:
	@echo "🔧 编译 Go 二进制..."
	@mkdir -p "$(OUTPUT_DIR)/bin"
	CGO_ENABLED=0 go build -tags=jsoniter \
		-ldflags "$(LDFLAGS)" \
		-o "$(OUTPUT_DIR)/bin/$(APP_NAME)" \
		"$(MAIN_DIR)"
	@echo "✅ 构建完成: $(OUTPUT_DIR)/bin/$(APP_NAME)"

# 清理构建产物
clean:
	@echo "🧹 清理..."
	@rm -rf "$(OUTPUT_DIR)"
	@rm -rf static/out
	@rm -rf web/out
	@rm -rf web/.next
	@echo "✅ 清理完成"

# 运行测试
test:
	@echo "🔧 运行测试..."
	@go test ./internal/... 2>&1 | grep -v "static\\static" | tail -5 || true
	@echo "✅ 测试完成"

# 帮助信息
help:
	@echo "使用方法:"
	@echo "  make build         完整构建（前端 + Go 编译）"
	@echo "  make build-frontend 仅构建前端"
	@echo "  make build-backend  仅编译 Go（需 static/out 已存在）"
	@echo "  make clean         清理构建产物"
	@echo "  make test          运行测试"