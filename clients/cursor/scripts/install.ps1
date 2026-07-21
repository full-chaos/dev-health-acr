[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$packageRoot = Split-Path -Parent $PSScriptRoot
$configRoot = if ($env:CURSOR_PLUGIN_DIR) { $env:CURSOR_PLUGIN_DIR } else { Join-Path $HOME ".cursor/plugins/local/context-fabric" }
$markerFile = ".context-fabric-owner.v1"
$markerValue = "context-fabric-cursor.v1"

# The plugin lives at $configRoot as a stable, owned, real directory --
# never a symlink or junction. Updates replace files in place; there is no
# directory swap, so mixed-old/new-file reads during an update are the
# accepted tradeoff for never needing a directory-level cutover primitive
# (which is unsafe for a directory target on Windows).
$payloadFiles = @(
  ".cursor-plugin/plugin.json",
  "mcp.json",
  "commands/get-context.md",
  "commands/plan-with-context-fabric.md",
  "rules/context-fabric.mdc",
  "rules/preplan-optional.mdc",
  "rules/no-automatic-use.mdc",
  "skills/context-fabric/SKILL.md"
)

# Replace one regular file atomically: copy into a same-directory temp
# file, then atomically move it over the destination with File.Move's
# checked overwrite=true overload -- a single filesystem operation, so the
# destination is never briefly absent, on Windows or elsewhere.
function Invoke-AtomicWrite {
  param([string]$Destination, [string]$Source)
  $destDir = Split-Path -Parent $Destination
  New-Item -ItemType Directory -Force -Path $destDir | Out-Null
  $tmp = Join-Path $destDir (".atomic." + [Guid]::NewGuid().ToString("N"))
  Copy-Item -Path $Source -Destination $tmp
  [System.IO.File]::Move($tmp, $Destination, $true)
}

if (-not [IO.Path]::IsPathRooted($configRoot)) { throw "CURSOR_PLUGIN_DIR must be an absolute path" }
$existing = Get-Item -Force -LiteralPath $configRoot -ErrorAction SilentlyContinue
if ($null -ne $existing -and $existing.LinkType) {
  throw "refusing to operate on a legacy symlink or junction target; remove it manually first"
}
if ($null -ne $existing) {
  $entries = Get-ChildItem -Force -LiteralPath $configRoot
  if ($entries.Count -ne 0) { throw "refusing to install into a non-empty target" }
}

# Full staged validation before any mutation: every required source file
# must exist and be readable before the target is touched at all.
foreach ($rel in $payloadFiles) {
  $src = Join-Path $packageRoot $rel
  if (-not (Test-Path -LiteralPath $src -PathType Leaf)) { throw "package source missing required file: $rel" }
}

# Required directories exist before any file is replaced.
New-Item -ItemType Directory -Force -Path (Join-Path $configRoot ".cursor-plugin") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $configRoot "commands") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $configRoot "rules") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $configRoot "skills/context-fabric") | Out-Null

foreach ($rel in $payloadFiles) {
  Invoke-AtomicWrite -Destination (Join-Path $configRoot $rel) -Source (Join-Path $packageRoot $rel)
}

# Commit/version marker written last: its presence with the correct value
# is the only proof of a complete, owned install. If anything above failed,
# execution never reaches this line, so a rerun converges safely.
$markerTmp = Join-Path $configRoot (".atomic." + [Guid]::NewGuid().ToString("N"))
Set-Content -NoNewline -Path $markerTmp -Value $markerValue
[System.IO.File]::Move($markerTmp, (Join-Path $configRoot $markerFile), $true)

Write-Output "installed Context Fabric Cursor plugin at $configRoot"
