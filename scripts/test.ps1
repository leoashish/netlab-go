# Run one chapter's tests (or all) with the race detector and a timeout.   .\scripts\test.ps1 07   |   .\scripts\test.ps1 all
# (not tested by the author: no PowerShell was available when this course was built)
param([string]$Chapter = "all")
Set-Location (Join-Path $PSScriptRoot "..")
if ($Chapter -eq "all") { go test ./... -race -timeout 300s; exit $LASTEXITCODE }
$n = "{0:D2}" -f [int]$Chapter
go test ./internal/... -race -timeout 120s -run "Ch${n}_" -v
exit $LASTEXITCODE
