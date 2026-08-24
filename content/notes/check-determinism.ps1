<#
.SYNOPSIS
  Prove that two ingests of the same inputs produce a byte-identical forge.json.

.DESCRIPTION
  frznforge's core promise is that `npm run ingest` is a pure function of the repos on disk
  plus frznforge.config.ts. Anything volatile that sneaks in -- a wall-clock timestamp, an
  unsorted Map iteration, a path built with the host separator -- shows up here as a hash
  mismatch, and shows up in CI long before it shows up as a churning git diff.

  Run it from the repo root. It ingests twice into throwaway directories so your real
  ./data is never touched.

.EXAMPLE
  pwsh ./content/notes/check-determinism.ps1
  pwsh ./content/notes/check-determinism.ps1 -Keep
#>
[CmdletBinding()]
param(
    # Leave the two temp output directories in place for inspection after a mismatch.
    [switch]$Keep
)

$ErrorActionPreference = 'Stop'

function New-Ingest {
    param([string]$Label)

    $out = Join-Path ([System.IO.Path]::GetTempPath()) "frznforge-$Label-$(New-Guid)"
    New-Item -ItemType Directory -Path $out -Force | Out-Null

    # FRZNFORGE_OUT_DIR overrides ingest.outDir, so config stays untouched.
    $env:FRZNFORGE_OUT_DIR = $out
    try {
        npm run --silent ingest
        if ($LASTEXITCODE -ne 0) { throw "ingest failed for run '$Label' (exit $LASTEXITCODE)" }
    } finally {
        Remove-Item Env:\FRZNFORGE_OUT_DIR -ErrorAction SilentlyContinue
    }

    return $out
}

# Hash every emitted file, not just forge.json: the blob store is part of the artifact.
function Get-TreeHash {
    param([string]$Root)

    Get-ChildItem -Path $Root -Recurse -File |
        Sort-Object { $_.FullName.Substring($Root.Length).Replace('\', '/') } |
        ForEach-Object {
            $rel = $_.FullName.Substring($Root.Length).TrimStart('\', '/').Replace('\', '/')
            '{0}  {1}' -f (Get-FileHash -Algorithm SHA256 -Path $_.FullName).Hash, $rel
        }
}

Write-Host 'run 1/2 ...' -ForegroundColor DarkGray
$a = New-Ingest -Label 'a'
Write-Host 'run 2/2 ...' -ForegroundColor DarkGray
$b = New-Ingest -Label 'b'

$hashA = Get-TreeHash -Root $a
$hashB = Get-TreeHash -Root $b
$diff = Compare-Object -ReferenceObject $hashA -DifferenceObject $hashB

if ($null -eq $diff) {
    Write-Host "deterministic: $($hashA.Count) files identical across both runs" -ForegroundColor Green
    if (-not $Keep) { Remove-Item -Recurse -Force $a, $b }
    exit 0
}

Write-Host 'NOT deterministic -- these files differ between runs:' -ForegroundColor Red
$diff | ForEach-Object { '  {0} {1}' -f $_.SideIndicator, ($_.InputObject -split '  ')[1] }
Write-Host "run A: $a"
Write-Host "run B: $b"
Write-Host 'Tip: diff the two forge.json files with a JSON-aware differ; the usual culprits'
Write-Host 'are generatedAt-style fields, Set/Map iteration order, and path.sep leaking in.'
exit 1
