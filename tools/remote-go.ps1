# remote-go.ps1 -- build/test a Go tree via Docker on a Linux host with Docker reachable
# via an ssh alias (default: $env:N5FANGOV_BUILD_HOST, else "builder" - see .env.example). No Go toolchain is needed on the Windows side.
# Usage:
#   tools\remote-go.ps1 -Path <repo-or-worktree> -Id <unique-name> [-Host <ssh-alias>] [-Image <docker-image>] [-Cmd "go test ./..."] [-Fetch]
# Default Cmd: go mod tidy, go vet, go test, go build -> n5-fangov (linux/amd64, static).
# -Fetch copies the built binary back to <Path>\dist\n5-fangov.
# Run with pwsh (PowerShell 7+). Windows PowerShell 5.1 corrupts the binary tar pipe.
param(
    [Parameter(Mandatory=$true)][string]$Path,
    [Parameter(Mandatory=$true)][string]$Id,
    # -Host on the command line; the variable is $BuildHost because $Host is a
    # read-only automatic variable in PowerShell.
    [Alias("Host")][string]$BuildHost = $(if ($env:N5FANGOV_BUILD_HOST) { $env:N5FANGOV_BUILD_HOST } else { "builder" }),
    [string]$Cmd = "",
    # Builder image. The default is cgo-free (static builds); the race
    # detector needs cgo: -Image golang:1.25-bookworm -Cmd "go test -race -count=1 ./..."
    [string]$Image = "golang:1.25-alpine",
    [switch]$Fetch
)
$ErrorActionPreference = "Stop"
$Path = (Resolve-Path $Path).Path
if ($Id -notmatch '^[a-zA-Z0-9_-]+$') { throw "Id must be [a-zA-Z0-9_-]" }
if ($BuildHost -notmatch '^[a-zA-Z0-9_.@-]+$') { throw "Host must be an ssh alias or hostname" }
if ($Image -notmatch '^[a-zA-Z0-9_./:@-]+$') { throw "Image must be a plain image reference" }
# CGO_ENABLED=0 for the static release build; -race needs cgo.
$cgo = "0"
if ($Cmd -match '-race') { $cgo = "1" }
$remote = "gobuild/$Id"
if ($Cmd -eq "") {
    $Cmd = "go mod tidy && go vet ./... && go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o n5-fangov ./cmd/n5-fangov && ls -la n5-fangov"
}
# ship the tree (without .git and dist) as a tar stream
Push-Location $Path
try {
    ssh $BuildHost "rm -rf ~/$remote && mkdir -p ~/$remote"
    tar -cf - --exclude .git --exclude dist . | ssh $BuildHost "tar -xf - -C ~/$remote"
} finally { Pop-Location }
$escaped = $Cmd.Replace("'", "'\''")
$docker = "docker run --rm -v `$HOME/${remote}:/src -w /src -v n5fangov-gomod:/go/pkg/mod -v n5fangov-gocache:/root/.cache/go-build -e CGO_ENABLED=$cgo $Image sh -c '${escaped}'"
ssh $BuildHost $docker
$rc = $LASTEXITCODE
if ($Fetch -and $rc -eq 0) {
    New-Item -ItemType Directory -Force (Join-Path $Path "dist") | Out-Null
    scp -q "${BuildHost}:$remote/n5-fangov" (Join-Path $Path "dist\n5-fangov")
    Write-Host "fetched -> $(Join-Path $Path 'dist\n5-fangov')"
}
exit $rc
