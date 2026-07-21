[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$packageRoot = Split-Path -Parent $PSScriptRoot
$configRoot = if ($env:CURSOR_PLUGIN_DIR) { $env:CURSOR_PLUGIN_DIR } else { Join-Path $HOME ".cursor/plugins/local/context-fabric" }
$ownerFile = ".context-fabric-owner.v1"
$ownerValue = "context-fabric-cursor.v1"

if (-not [IO.Path]::IsPathRooted($configRoot)) { throw "CURSOR_PLUGIN_DIR must be an absolute path" }
if (Test-Path -LiteralPath $configRoot) {
  $entries = Get-ChildItem -Force -LiteralPath $configRoot
  if ($entries.Count -eq 0) { Remove-Item -LiteralPath $configRoot -Force } else { throw "refusing to install into a non-empty target" }
}

$parent = Split-Path -Parent $configRoot
New-Item -ItemType Directory -Force -Path $parent | Out-Null
$stage = Join-Path $parent (".context-fabric-cursor." + [Guid]::NewGuid().ToString("N"))
$installed = $false
try {
  New-Item -ItemType Directory -Path $stage | Out-Null
  New-Item -ItemType Directory -Path (Join-Path $stage ".cursor-plugin") | Out-Null
  Copy-Item -Path (Join-Path $packageRoot ".cursor-plugin/plugin.json") -Destination (Join-Path $stage ".cursor-plugin/plugin.json")
  Copy-Item -Path (Join-Path $packageRoot "mcp.json") -Destination (Join-Path $stage "mcp.json")
  Copy-Item -Recurse -Path (Join-Path $packageRoot "commands") -Destination (Join-Path $stage "commands")
  Copy-Item -Recurse -Path (Join-Path $packageRoot "rules") -Destination (Join-Path $stage "rules")
  Copy-Item -Recurse -Path (Join-Path $packageRoot "skills") -Destination (Join-Path $stage "skills")
  Set-Content -NoNewline -Path (Join-Path $stage $ownerFile) -Value $ownerValue
  New-Item -ItemType SymbolicLink -Path $configRoot -Target (Split-Path -Leaf $stage) | Out-Null
  $installed = $true
} finally {
  if (-not $installed -and (Test-Path -LiteralPath $stage)) { Remove-Item -Recurse -Force -LiteralPath $stage }
}
Write-Output "installed Context Fabric Cursor plugin at $configRoot"
