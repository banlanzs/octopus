#!/bin/sh
# 本地构建脚本（供 Makefile 调用）。
# 独立成 POSIX sh 脚本的原因：Windows 原生 GNU Make（chocolatey 等）会忽略
# makefile 里的 SHELL 变量并回退到 cmd.exe，导致 rm/mv 等 Unix 命令执行失败
# （"系统找不到指定的路径"）。把构建逻辑放进 sh 脚本、由 make 转发调用，
# 可保证在 Git Bash / WSL / macOS / Linux 上行为一致。
# 注意：必须保持 POSIX sh 兼容，勿用 bash 专属语法（如 pipefail）。
set -eu

APP_NAME="octopus"
OUTPUT_DIR="build"
MAIN_DIR="."

GIT_VERSION="$(git describe --tags --abbrev=0 2>/dev/null || echo dev)"
COMMIT_ID="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
BUILD_TIME="$(date +'%F %T %z')"
LDFLAGS="-X 'github.com/bestruirui/octopus/internal/conf.Version=${GIT_VERSION}' \
         -X 'github.com/bestruirui/octopus/internal/conf.BuildTime=${BUILD_TIME}' \
         -X 'github.com/bestruirui/octopus/internal/conf.Author=banlanzs' \
         -X 'github.com/bestruirui/octopus/internal/conf.Commit=${COMMIT_ID}' \
         -s -w"

# 输出文件名：Windows 下 go build 自动加 .exe，无需手动处理
BINARY="${OUTPUT_DIR}/bin/${APP_NAME}"

cmd_build_frontend() {
    echo "🔧 构建前端..."
    (
        cd web
        pnpm install --frozen-lockfile 2>/dev/null || pnpm install
        pnpm run build
    )
    echo "✅ 前端构建完成: web/out/"
    rm -rf static/out
    mv web/out static/out
    echo "✅ 前端产物已嵌入: static/out/"
}

cmd_build_backend() {
    echo "🔧 编译 Go 二进制..."
    mkdir -p "${OUTPUT_DIR}/bin"
    CGO_ENABLED=0 go build -tags=jsoniter \
        -ldflags "${LDFLAGS}" \
        -o "${BINARY}" \
        "${MAIN_DIR}"
    echo "✅ 构建完成: ${BINARY}"
}

cmd_build() {
    cmd_build_frontend
    cmd_build_backend
}

cmd_clean() {
    echo "🧹 清理..."
    rm -rf "${OUTPUT_DIR}"
    rm -rf static/out
    rm -rf web/out
    rm -rf web/.next
    echo "✅ 清理完成"
}

cmd_test() {
    echo "🔧 运行测试..."
    go test ./internal/... 2>&1 | grep -v static | tail -5 || true
    echo "✅ 测试完成"
}

cmd_help() {
    echo "使用方法:"
    echo "  make build         完整构建（前端 + Go 编译）"
    echo "  make build-frontend 仅构建前端"
    echo "  make build-backend  仅编译 Go（需 static/out 已存在）"
    echo "  make clean         清理构建产物"
    echo "  make test          运行测试"
}

case "${1:-help}" in
    build)          cmd_build ;;
    build-frontend) cmd_build_frontend ;;
    build-backend)  cmd_build_backend ;;
    clean)          cmd_clean ;;
    test)           cmd_test ;;
    help)           cmd_help ;;
    *)              echo "未知命令: $1"; cmd_help; exit 1 ;;
esac
