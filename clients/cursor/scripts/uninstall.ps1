[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$configRoot = if ($env:CURSOR_PLUGIN_DIR) { $env:CURSOR_PLUGIN_DIR } else { Join-Path $HOME ".cursor/plugins/local/context-fabric" }
$markerFile = ".context-fabric-owner.v1"
$markerValue = "context-fabric-cursor.v1"

# Only ever remove a target proven owned: a real directory (never a symlink
# or junction) carrying our exact marker. An unowned or legacy-linked
# target is left completely untouched.
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

if (-not [IO.Path]::IsPathRooted($configRoot)) { throw "refusing to remove a target not owned by Context Fabric" }
$existing = Get-Item -Force -LiteralPath $configRoot -ErrorAction SilentlyContinue
if ($null -ne $existing -and $existing.LinkType) {
  throw "refusing to operate on a legacy symlink or junction target; remove it manually first"
}
if (-not (Test-OwnedDirectory -Root $configRoot)) {
  throw "refusing to remove a target not owned by Context Fabric"
}
Remove-Item -Recurse -Force -LiteralPath $configRoot
Write-Output "removed Context Fabric Cursor plugin at $configRoot"
