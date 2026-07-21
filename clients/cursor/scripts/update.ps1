[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$packageRoot = Split-Path -Parent $PSScriptRoot
$configRoot = if ($env:CURSOR_PLUGIN_DIR) { $env:CURSOR_PLUGIN_DIR } else { Join-Path $HOME ".cursor/plugins/local/context-fabric" }
$ownerFile = ".context-fabric-owner.v1"
$ownerValue = "context-fabric-cursor.v1"
# Stages for this target live in a directory scoped to the target's own
# name, never in the shared parent -- so a sibling install under the same
# parent can never observe or delete this target's generations. Every
# generation is retained on disk until an owned uninstall removes it.
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

# Atomically replace the symlink at $Destination with $Source using a
# single, checked native syscall -- never a delete-then-create pair, which
# would leave a window where $Destination briefly does not exist. Windows
# uses MoveFileEx with the single, bounded MOVEFILE_REPLACE_EXISTING flag
# (no MOVEFILE_COPY_ALLOWED, no delayed-reboot flag); everywhere else uses
# POSIX rename(2), which atomically replaces an existing symlink target by
# directory-entry swap without dereferencing it.
function Invoke-AtomicReplace {
  param([string]$Source, [string]$Destination)
  if ($IsWindows) {
    if (-not ([System.Management.Automation.PSTypeName]"ContextFabricNative.Interop").Type) {
      Add-Type -Namespace ContextFabricNative -Name Interop -MemberDefinition @'
[System.Runtime.InteropServices.DllImport("kernel32.dll", SetLastError = true, CharSet = System.Runtime.InteropServices.CharSet.Unicode)]
public static extern bool MoveFileEx(string lpExistingFileName, string lpNewFileName, uint dwFlags);
'@
    }
    $moveFileReplaceExisting = 0x1
    $ok = [ContextFabricNative.Interop]::MoveFileEx($Source, $Destination, $moveFileReplaceExisting)
    if (-not $ok) {
      $errorCode = [System.Runtime.InteropServices.Marshal]::GetLastWin32Error()
      throw "MoveFileEx atomic replace failed with Win32 error $errorCode"
    }
  } else {
    if (-not ([System.Management.Automation.PSTypeName]"ContextFabricNative.Interop").Type) {
      Add-Type -Namespace ContextFabricNative -Name Interop -MemberDefinition @'
[System.Runtime.InteropServices.DllImport("libc", SetLastError = true)]
public static extern int rename(string oldpath, string newpath);
'@
    }
    $result = [ContextFabricNative.Interop]::rename($Source, $Destination)
    if ($result -ne 0) {
      $errorCode = [System.Runtime.InteropServices.Marshal]::GetLastWin32Error()
      throw "rename atomic replace failed with errno $errorCode"
    }
  }
}

if (-not [IO.Path]::IsPathRooted($configRoot)) { throw "refusing to update a target not owned by Context Fabric" }
if ($null -eq (Get-OwnedStage $configRoot)) { throw "refusing to update a target not owned by Context Fabric" }
New-Item -ItemType Directory -Force -Path $stagesRoot | Out-Null
$stage = Join-Path $stagesRoot ([Guid]::NewGuid().ToString("N"))
$parent = Split-Path -Parent $configRoot
$link = Join-Path $parent (".context-fabric-cursor.link." + [Guid]::NewGuid().ToString("N"))
$targetName = Split-Path -Leaf $configRoot
try {
  New-Item -ItemType Directory -Path $stage | Out-Null
  New-Item -ItemType Directory -Path (Join-Path $stage ".cursor-plugin") | Out-Null
  Copy-Item -Path (Join-Path $packageRoot ".cursor-plugin/plugin.json") -Destination (Join-Path $stage ".cursor-plugin/plugin.json")
  Copy-Item -Path (Join-Path $packageRoot "mcp.json") -Destination (Join-Path $stage "mcp.json")
  Copy-Item -Recurse -Path (Join-Path $packageRoot "commands") -Destination (Join-Path $stage "commands")
  Copy-Item -Recurse -Path (Join-Path $packageRoot "rules") -Destination (Join-Path $stage "rules")
  Copy-Item -Recurse -Path (Join-Path $packageRoot "skills") -Destination (Join-Path $stage "skills")
  Set-Content -NoNewline -Path (Join-Path $stage $ownerFile) -Value $ownerValue
  New-Item -ItemType SymbolicLink -Path $link -Target "$targetName.stages/$(Split-Path -Leaf $stage)" | Out-Null
  Invoke-AtomicReplace -Source $link -Destination $configRoot
} finally {
  if (Test-Path -LiteralPath $link) { Remove-Item -Force -LiteralPath $link }
  if ((Test-Path -LiteralPath $stage) -and ((Get-OwnedStage $configRoot) -ne $stage)) { Remove-Item -Recurse -Force -LiteralPath $stage }
}
Write-Output "updated Context Fabric Cursor plugin at $configRoot (all prior generations retained under $stagesRoot until uninstall)"
