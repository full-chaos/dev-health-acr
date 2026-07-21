[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$packageRoot = Split-Path -Parent $PSScriptRoot
$configRoot = if ($env:CURSOR_PLUGIN_DIR) { $env:CURSOR_PLUGIN_DIR } else { Join-Path $HOME ".cursor/plugins/local/context-fabric" }
$ownerFile = ".context-fabric-owner.v1"
$ownerValue = "context-fabric-cursor.v1"

function Get-OwnedStage {
  param([string]$Root)
  $item = Get-Item -Force -LiteralPath $Root -ErrorAction SilentlyContinue
  if ($null -eq $item -or $item.LinkType -ne "SymbolicLink") { return $null }
  $target = [string]$item.Target
  if ($target -notmatch '^\.context-fabric-cursor\.[A-Za-z0-9]+$') { return $null }
  $stage = Join-Path (Split-Path -Parent $Root) $target
  $marker = Join-Path $stage $ownerFile
  $stageItem = Get-Item -Force -LiteralPath $stage -ErrorAction SilentlyContinue
  $markerItem = Get-Item -Force -LiteralPath $marker -ErrorAction SilentlyContinue
  if ($null -eq $stageItem -or $stageItem.LinkType -or $null -eq $markerItem -or $markerItem.LinkType) { return $null }
  if ((Get-Content -Raw -LiteralPath $marker) -ne $ownerValue) { return $null }
  return $stage
}

if (-not [IO.Path]::IsPathRooted($configRoot)) { throw "refusing to update a target not owned by Context Fabric" }
$previousStage = Get-OwnedStage $configRoot
if ($null -eq $previousStage) { throw "refusing to update a target not owned by Context Fabric" }
$parent = Split-Path -Parent $configRoot
$stage = Join-Path $parent (".context-fabric-cursor." + [Guid]::NewGuid().ToString("N"))
$link = Join-Path $parent (".context-fabric-cursor.link." + [Guid]::NewGuid().ToString("N"))
try {
  New-Item -ItemType Directory -Path $stage | Out-Null
  New-Item -ItemType Directory -Path (Join-Path $stage ".cursor-plugin") | Out-Null
  Copy-Item -Path (Join-Path $packageRoot ".cursor-plugin/plugin.json") -Destination (Join-Path $stage ".cursor-plugin/plugin.json")
  Copy-Item -Path (Join-Path $packageRoot "mcp.json") -Destination (Join-Path $stage "mcp.json")
  Copy-Item -Recurse -Path (Join-Path $packageRoot "commands") -Destination (Join-Path $stage "commands")
  Copy-Item -Recurse -Path (Join-Path $packageRoot "rules") -Destination (Join-Path $stage "rules")
  Copy-Item -Recurse -Path (Join-Path $packageRoot "skills") -Destination (Join-Path $stage "skills")
  Set-Content -NoNewline -Path (Join-Path $stage $ownerFile) -Value $ownerValue
  New-Item -ItemType SymbolicLink -Path $link -Target (Split-Path -Leaf $stage) | Out-Null
  [IO.File]::Move($link, $configRoot, $true)
  Remove-Item -Recurse -Force -LiteralPath $previousStage
} finally {
  if (Test-Path -LiteralPath $link) { Remove-Item -Force -LiteralPath $link }
  if ((Test-Path -LiteralPath $stage) -and ((Get-OwnedStage $configRoot) -ne $stage)) { Remove-Item -Recurse -Force -LiteralPath $stage }
}
Write-Output "updated Context Fabric Cursor plugin at $configRoot"
