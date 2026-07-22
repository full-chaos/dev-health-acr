[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$packageRoot = Split-Path -Parent $PSScriptRoot
$configRoot = if ($env:CURSOR_PLUGIN_DIR) { $env:CURSOR_PLUGIN_DIR } else { Join-Path $HOME ".cursor/plugins/local/context-fabric" }
$markerFile = ".context-fabric-owner.v1"
$markerValue = "context-fabric-cursor.v1"

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

# A target is owned only if it is a real directory (never a symlink or
# junction -- that would be a leftover from an earlier, unsafe design) and
# carries our exact marker. Never adopt an unowned or legacy-linked target.
function Test-OwnedDirectory {
  param([string]$Root)
  $item = Get-Item -Force -LiteralPath $Root -ErrorAction SilentlyContinue
  if ($null -eq $item -or -not $item.PSIsContainer -or $item.LinkType) { return $false }
  $marker = Join-Path $Root $markerFile
  $markerItem = Get-Item -Force -LiteralPath $marker -ErrorAction SilentlyContinue
  if ($null -eq $markerItem -or $markerItem.LinkType) { return $false }
  if ((Get-Content -Raw -LiteralPath $marker) -ne $markerValue) { return $false }
  return $true
}

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

if (-not [IO.Path]::IsPathRooted($configRoot)) { throw "refusing to update a target not owned by Context Fabric" }
$existing = Get-Item -Force -LiteralPath $configRoot -ErrorAction SilentlyContinue
if ($null -ne $existing -and $existing.LinkType) {
  throw "refusing to operate on a legacy symlink or junction target; remove it manually first"
}
if (-not (Test-OwnedDirectory -Root $configRoot)) {
  throw "refusing to update a target not owned by Context Fabric"
}

# Full staged validation before any mutation: every required source file
# must exist and be readable before the target is touched at all.
foreach ($rel in $payloadFiles) {
  $src = Join-Path $packageRoot $rel
  if (-not (Test-Path -LiteralPath $src -PathType Leaf)) { throw "package source missing required file: $rel" }
}

# Required directories exist before any file is replaced. If a prior update
# was interrupted partway, rerunning starts here again and converges: every
# required path is created (idempotently) before any content is touched.
New-Item -ItemType Directory -Force -Path (Join-Path $configRoot ".cursor-plugin") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $configRoot "commands") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $configRoot "rules") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $configRoot "skills/context-fabric") | Out-Null

# Each file is replaced individually and atomically (see Invoke-AtomicWrite
# above). A failure partway through this loop leaves every already-replaced
# file on its new content, every not-yet-reached file on its old content,
# and the target directory itself always fully populated -- no required
# path is ever missing. Rerunning the whole update after a failure is
# safe: it simply re-replaces every file and converges to the same state.
foreach ($rel in $payloadFiles) {
  Invoke-AtomicWrite -Destination (Join-Path $configRoot $rel) -Source (Join-Path $packageRoot $rel)
}

# Commit/version marker replaced last, after every content file: proves
# this update ran to completion. A failure above never reaches this line.
$markerTmp = Join-Path $configRoot (".atomic." + [Guid]::NewGuid().ToString("N"))
Set-Content -NoNewline -Path $markerTmp -Value $markerValue
[System.IO.File]::Move($markerTmp, (Join-Path $configRoot $markerFile), $true)

Write-Output "updated Context Fabric Cursor plugin at $configRoot"
