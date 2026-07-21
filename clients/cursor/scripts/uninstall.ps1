[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$configRoot = if ($env:CURSOR_PLUGIN_DIR) { $env:CURSOR_PLUGIN_DIR } else { Join-Path $HOME ".cursor/plugins/local/context-fabric" }
$ownerFile = ".context-fabric-owner.v1"
$ownerValue = "context-fabric-cursor.v1"
$stagesRoot = "$configRoot.stages"

function Get-OwnedStage {
  param([string]$Root)
  $item = Get-Item -Force -LiteralPath $Root -ErrorAction SilentlyContinue
  if ($null -eq $item -or $item.LinkType -ne "SymbolicLink") { return $null }
  $target = [string]$item.Target
  $prefix = (Split-Path -Leaf $Root) + ".stages/"
  if (-not $target.StartsWith($prefix)) { return $null }
  $remainder = $target.Substring($prefix.Length)
  if ($remainder.Length -eq 0 -or $remainder.Contains("/") -or $remainder.Contains("\")) { return $null }
  $stage = Join-Path "$Root.stages" $remainder
  $marker = Join-Path $stage $ownerFile
  $stageItem = Get-Item -Force -LiteralPath $stage -ErrorAction SilentlyContinue
  $markerItem = Get-Item -Force -LiteralPath $marker -ErrorAction SilentlyContinue
  if ($null -eq $stageItem -or $stageItem.LinkType -or $null -eq $markerItem -or $markerItem.LinkType) { return $null }
  if ((Get-Content -Raw -LiteralPath $marker) -ne $ownerValue) { return $null }
  return $stage
}

if (-not [IO.Path]::IsPathRooted($configRoot)) { throw "refusing to remove a target not owned by Context Fabric" }
if ($null -eq (Get-OwnedStage $configRoot)) { throw "refusing to remove a target not owned by Context Fabric" }
Remove-Item -Force -LiteralPath $configRoot
Remove-Item -Recurse -Force -LiteralPath $stagesRoot
Write-Output "removed Context Fabric Cursor plugin at $configRoot"
