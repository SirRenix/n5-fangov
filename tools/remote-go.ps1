# remote-go.ps1 -- build/test a Go tree via Docker on Builder (no Go on Windows/n5host).
# Usage:
#   tools\remote-go.ps1 -Path <repo-or-worktree> -Id <unique-name> [-Cmd "go test ./..."] [-Fetch]
# Default Cmd: go mod tidy, go vet, go test, go build -> pvefand (linux/amd64, static).
# -Fetch copies the built binary back to <Path>\dist\pvefand.
# Run with pwsh (PowerShell 7+). Windows PowerShell 5.1 corrupts the binary tar pipe.
param(
    [Parameter(Mandatory=$true)][string]$Path,
    [Parameter(Mandatory=$true)][string]$Id,
    [string]$Cmd = "",
    [switch]$Fetch
)
$ErrorActionPreference = "Stop"
$Path = (Resolve-Path $Path).Path
if ($Id -notmatch '^[a-zA-Z0-9_-]+$') { throw "Id must be [a-zA-Z0-9_-]" }
$remote = "gobuild/$Id"
if ($Cmd -eq "") {
    $Cmd = "go mod tidy && go vet ./... && go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o pvefand ./cmd/pvefand && ls -la pvefand"
}
# ship the tree (without .git and dist) as a tar stream
Push-Location $Path
try {
    ssh builder "rm -rf ~/$remote && mkdir -p ~/$remote"
    tar -cf - --exclude .git --exclude dist . | ssh builder "tar -xf - -C ~/$remote"
} finally { Pop-Location }
$escaped = $Cmd.Replace("'", "'\''")
$docker = "docker run --rm -v `$HOME/${remote}:/src -w /src -v pvefand-gomod:/go/pkg/mod -v pvefand-gocache:/root/.cache/go-build -e CGO_ENABLED=0 golang:1.25-alpine sh -c '${escaped}'"
ssh builder $docker
$rc = $LASTEXITCODE
if ($Fetch -and $rc -eq 0) {
    New-Item -ItemType Directory -Force (Join-Path $Path "dist") | Out-Null
    scp -q "builder:$remote/pvefand" (Join-Path $Path "dist\pvefand")
    Write-Host "fetched -> $(Join-Path $Path 'dist\pvefand')"
}
exit $rc
