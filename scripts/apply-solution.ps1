# Copy a chapter's reference solution over your files.   THIS OVERWRITES YOUR WORK: commit or stash first.
#   .\scripts\apply-solution.ps1 05            apply chapter 05
#   .\scripts\apply-solution.ps1 -Through 05   apply chapters 01..05 in order
#   .\scripts\apply-solution.ps1 -List         list the chapters that have solutions
# (not tested by the author: no PowerShell was available when this course was built)
param([string]$Chapter = "", [int]$Through = 0, [switch]$List)
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")

function Apply-One([int]$n) {
    $dir = "_solutions/ch{0:D2}" -f $n
    if (-not (Test-Path $dir)) { throw "no solution for chapter $n (expected $dir)" }
    Copy-Item -Path "$dir/*" -Destination "." -Recurse -Force
    Write-Host ("applied the reference solution of chapter {0:D2}" -f $n)
}

if ($List) { (Get-ChildItem _solutions -Directory).Name -replace '^ch','' | ForEach-Object { Write-Host -NoNewline "$_ " }; Write-Host; exit 0 }
if ($Through -gt 0) { 1..$Through | ForEach-Object { Apply-One $_ }; exit 0 }
if ($Chapter -ne "") { Apply-One ([int]$Chapter); exit 0 }
Write-Host "usage: apply-solution.ps1 NN | -Through NN | -List"; exit 2
