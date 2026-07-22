[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$configRoot = if ($env:OPENCODE_CONFIG_DIR) { $env:OPENCODE_CONFIG_DIR } else { Join-Path $HOME ".config/context-fabric-opencode" }
$ownerFile = ".context-fabric-owner.v1"
$ownerValue = "context-fabric-opencode.v1"

function Get-OwnedStage {
  param([string]$Root)
  $item = Get-Item -Force -LiteralPath $Root -ErrorAction SilentlyContinue
  if ($null -eq $item -or $item.LinkType -ne "SymbolicLink") { return $null }
  $target = [string]$item.Target
  if ($target -notmatch '^\.context-fabric-opencode\.[A-Za-z0-9]+$') { return $null }
  $stage = Join-Path (Split-Path -Parent $Root) $target
  $marker = Join-Path $stage $ownerFile
  $stageItem = Get-Item -Force -LiteralPath $stage -ErrorAction SilentlyContinue
  $markerItem = Get-Item -Force -LiteralPath $marker -ErrorAction SilentlyContinue
  if ($null -eq $stageItem -or $stageItem.LinkType -or $null -eq $markerItem -or $markerItem.LinkType) { return $null }
  if ((Get-Content -Raw -LiteralPath $marker) -ne $ownerValue) { return $null }
  return $stage
}

if (-not [IO.Path]::IsPathRooted($configRoot)) { throw "refusing to remove a target not owned by Context Fabric" }
$stage = Get-OwnedStage $configRoot
if ($null -eq $stage) { throw "refusing to remove a target not owned by Context Fabric" }
Remove-Item -Force -LiteralPath $configRoot
Remove-Item -Recurse -Force -LiteralPath $stage
Write-Output "removed Context Fabric OpenCode config at $configRoot"
