# 构建指南

## 环境要求

| 工具 | 版本要求 |
|------|---------|
| Go | 1.24+ |
| Node.js | 18+ |
| pnpm | 任意版本 |

---

## 构建 octopus-windows-amd64.exe

### 快速构建（推荐）

项目根目录提供 `Makefile`，一条命令完成完整构建：

```bash
make build
```

产物输出到 `build/bin/octopus`（Windows 平台为 `build/bin/octopus-windows-amd64.exe`）。

> **Windows 注意**：Makefile 会在 Windows 上调用 `scripts/make-build.ps1`，在
> macOS/Linux 上调用 `scripts/make-build.sh`，无需额外安装 Git Bash 或配置 `sh.exe`。

### 分步构建

```bash
# 1. 构建前端（SSG 静态导出）
make build-frontend

# 2. 编译 Go 二进制（带版本信息）
make build-backend
```

### 手动构建

```bash
# 1. 构建前端（SSG 静态导出）
cd web
pnpm install
pnpm run build
cd ..

# 2. 移动前端产物到 static/out（Go embed 会将其打包进二进制）
# 必须执行，否则 go build 报 "all:out: no matching files found"
mv web/out static/

# 3. 编译 Go 二进制（带版本信息）
CGO_ENABLED=0 go build -tags=jsoniter \
  -ldflags "-X 'github.com/bestruirui/octopus/internal/conf.Version=$(git describe --tags --abbrev=0 2>/dev/null || echo dev)' \
            -X 'github.com/bestruirui/octopus/internal/conf.BuildTime=$(date +'%F %T %z')' \
            -X 'github.com/bestruirui/octopus/internal/conf.Author=hureru' \
            -X 'github.com/bestruirui/octopus/internal/conf.Commit=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)' \
            -s -w" \
  -o octopus-windows-amd64.exe .
```

> 前端 SSG 产物通过 `static/static.go` 的 `//go:embed all:out` 嵌入到二进制中，因此 `static/out` 目录必须存在。

### 仅后端（无前端）

```bash
go build -tags=jsoniter -o octopus-windows-amd64.exe .
```

> 需要首先用 `go run main.go start` 生成默认配置文件，或用已有的配置文件运行。

---

## 跨平台发布

```bash
# 使用内置构建脚本
./scripts/build.sh build linux x86_64    # 构建 Linux amd64
./scripts/build.sh build windows x86_64  # 构建 Windows amd64
./scripts/build.sh build darwin arm64    # 构建 macOS arm64 (Apple Silicon)
./scripts/build.sh release               # 构建所有平台并打包 zip
```

构建产物输出到 `build/bin/`（可执行文件）和 `build/archives/`（zip 压缩包）。

---

## 开发模式

```bash
# 后端（Go）
go run main.go start

# 前端（热重载，需指定后端地址）
cd web
pnpm install
NEXT_PUBLIC_API_BASE_URL="http://127.0.0.1:8080" pnpm dev
```

前端开发服务器运行在 `localhost:3000`，API 请求代理到后端 `localhost:8080`。

---

## 常见问题

### `all:out: no matching files found`

`static/out` 目录不存在——前端产物未就位。执行 `mv web/out static/` 后再构建。

### 二进制体积较大

前端 SSG 产物（`.next/` 静态文件）被嵌入到二进制中。可以使用 `-s -w` 编译标志去除调试符号缩小体积（已包含在上面的完整构建命令中）。

### `go build` 后启动报错 "data/config.json not found"

首次运行 `./octopus-windows-amd64.exe` 会自动生成默认配置文件。如果需要自定义配置，先停止服务，编辑 `data/config.json` 后再启动。
