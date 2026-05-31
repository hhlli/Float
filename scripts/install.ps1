param (
    [string]$Server,
    [string]$Token,
    [string]$NodeId = "",
    [string]$RegisterToken = "",
    [string]$InstallDir = "",
    [string]$ServiceName = "FloatAgent",
    [switch]$Insecure,
    [switch]$DisableRpc,
    [switch]$IncludeBuffer,
    [switch]$EnableTerminal,
    [string]$Source = "github"
)

Write-Host "=========================================="
Write-Host "🚀 开始安装 Float Agent Windows 探针"
Write-Host "=========================================="

if (-not $Server) {
    Write-Host "❌ [错误] 缺少必要参数: 必须提供 -Server" -ForegroundColor Red
    exit 1
}
if (-not $NodeId -and -not $RegisterToken) {
    Write-Host "❌ [错误] 缺少标识: 必须提供 -NodeId 或 -RegisterToken" -ForegroundColor Red
    exit 1
}
if ($NodeId -and -not $Token) {
    Write-Host "❌ [错误] 静态指定 ID 时，必须提供 -Token" -ForegroundColor Red
    exit 1
}
Write-Host "✅ [1/5] 参数校验通过"

$IsAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)

if ($IsAdmin) {
    Write-Host "✅ [2/5] 检测到 Administrator 权限，执行系统级全局安装"
    $TargetInstallDir = if ($InstallDir) { $InstallDir } else { "C:\ProgramData\FloatAgent" }
    $Trigger = New-ScheduledTaskTrigger -AtStartup
    $Principal = New-ScheduledTaskPrincipal -UserId "SYSTEM" -LogonType ServiceAccount -RunLevel Highest
} else {
    Write-Host "⚠️ [2/5] 无 Administrator 权限，降级执行当前用户级安装"
    $TargetInstallDir = if ($InstallDir) { $InstallDir } else { "$env:LOCALAPPDATA\FloatAgent" }
    $Trigger = New-ScheduledTaskTrigger -AtLogOn
    $Principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive
}

if ($Source -eq "server") {
    $ProbeUrl = "$Server/float-agent-windows-amd64.exe"
} else {
    $ProbeUrl = "https://github.com/hhlli/Float-agent/releases/latest/download/float-agent-windows-amd64.exe"
}
$BinPath = "$TargetInstallDir\float-agent.exe"

Write-Host "📁 [3/5] 准备目录并下载文件..."
Write-Host "   -> 目标路径: $TargetInstallDir"
if (-not (Test-Path $TargetInstallDir)) {
    New-Item -ItemType Directory -Force -Path $TargetInstallDir | Out-Null
}
Invoke-WebRequest -Uri $ProbeUrl -OutFile $BinPath

$ArgsList = "-s `"$Server`""
if ($Token) { $ArgsList += " -t `"$Token`"" }
if ($NodeId) { $ArgsList += " -i `"$NodeId`"" }
if ($RegisterToken) { $ArgsList += " -register `"$RegisterToken`"" }
if ($Insecure) { $ArgsList += " --insecure" }
if ($DisableRpc) { $ArgsList += " --disable-rpc" }
if ($IncludeBuffer) { $ArgsList += " --include-buffer" }
if ($EnableTerminal) { $ArgsList += " --enable-terminal" }

Write-Host "⚙️ [4/5] 注册系统计划任务..."
$Action = New-ScheduledTaskAction -Execute $BinPath -Argument $ArgsList
$Task = New-ScheduledTask -Action $Action -Trigger $Trigger -Principal $Principal

Unregister-ScheduledTask -TaskName $ServiceName -Confirm:$false -ErrorAction SilentlyContinue
Register-ScheduledTask -TaskName $ServiceName -InputObject $Task | Out-Null

Write-Host "🔄 [5/5] 启动探针服务..."
Start-ScheduledTask -TaskName $ServiceName

Write-Host "=========================================="
Write-Host "✅ 安装与启动完成！"
Write-Host "=========================================="