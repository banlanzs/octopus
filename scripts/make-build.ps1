param(
    [ValidateSet('build', 'build-frontend', 'build-backend', 'clean', 'test', 'help')]
    [string]$Command = 'help'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$AppName = 'octopus'
$OutputDir = 'build'
$Binary = Join-Path $OutputDir "bin/$AppName-windows-amd64.exe"

function Invoke-External {
    param(
        [Parameter(Mandatory)]
        [string]$FilePath,
        [Parameter(ValueFromRemainingArguments)]
        [string[]]$ArgumentList
    )

    & $FilePath @ArgumentList
    if ($LASTEXITCODE -ne 0) {
        throw "$FilePath failed with exit code $LASTEXITCODE"
    }
}

function Get-GitValue {
    param(
        [Parameter(Mandatory)]
        [string[]]$ArgumentList,
        [Parameter(Mandatory)]
        [string]$Fallback
    )

    $value = & git @ArgumentList 2>$null
    if ($LASTEXITCODE -ne 0 -or -not $value) {
        return $Fallback
    }
    return $value.Trim()
}

function Build-Frontend {
    Write-Host 'Building frontend...'
    Push-Location web
    try {
        & pnpm.cmd install --frozen-lockfile
        if ($LASTEXITCODE -ne 0) {
            Invoke-External -FilePath 'pnpm.cmd' -ArgumentList @('install')
        }
        Invoke-External -FilePath 'pnpm.cmd' -ArgumentList @('run', 'build')
    }
    finally {
        Pop-Location
    }

    Write-Host 'Frontend build complete: web/out/'
    if (Test-Path -LiteralPath 'static/out') {
        Remove-Item -LiteralPath 'static/out' -Recurse -Force
    }
    Move-Item -LiteralPath 'web/out' -Destination 'static/out'
    Write-Host 'Frontend assets embedded: static/out/'
}

function Build-Backend {
    $gitVersion = Get-GitValue -ArgumentList @('describe', '--tags', '--abbrev=0') -Fallback 'dev'
    $commitId = Get-GitValue -ArgumentList @('rev-parse', '--short', 'HEAD') -Fallback 'unknown'
    $buildTime = Get-Date -Format 'yyyy-MM-dd HH:mm:ss zzz'
    $ldflags = "-X 'github.com/bestruirui/octopus/internal/conf.Version=$gitVersion' " +
        "-X 'github.com/bestruirui/octopus/internal/conf.BuildTime=$buildTime' " +
        "-X 'github.com/bestruirui/octopus/internal/conf.Author=banlanzs' " +
        "-X 'github.com/bestruirui/octopus/internal/conf.Commit=$commitId' -s -w"

    Write-Host 'Building Go binary...'
    New-Item -ItemType Directory -Path (Split-Path $Binary) -Force | Out-Null
    $env:CGO_ENABLED = '0'
    Invoke-External -FilePath 'go' -ArgumentList @(
        'build', '-tags=jsoniter', '-ldflags', $ldflags, '-o', $Binary, '.'
    )
    Write-Host "Build complete: $Binary"
}

function Clear-BuildArtifacts {
    Write-Host 'Cleaning...'
    foreach ($path in @($OutputDir, 'static/out', 'web/out', 'web/.next')) {
        if (Test-Path -LiteralPath $path) {
            Remove-Item -LiteralPath $path -Recurse -Force
        }
    }
    Write-Host 'Clean complete'
}

function Invoke-Tests {
    Write-Host 'Running tests...'
    & go test ./internal/... 2>&1 |
        Where-Object { $_ -notmatch 'static' } |
        Select-Object -Last 5
    Write-Host 'Tests complete'
}

function Show-Help {
    Write-Host 'Usage:'
    Write-Host '  make build          Full build (frontend + Go binary)'
    Write-Host '  make build-frontend Build frontend only'
    Write-Host '  make build-backend  Build Go binary (requires static/out)'
    Write-Host '  make clean          Remove build artifacts'
    Write-Host '  make test           Run tests'
}

switch ($Command) {
    'build' {
        Build-Frontend
        Build-Backend
    }
    'build-frontend' { Build-Frontend }
    'build-backend' { Build-Backend }
    'clean' { Clear-BuildArtifacts }
    'test' { Invoke-Tests }
    'help' { Show-Help }
}
