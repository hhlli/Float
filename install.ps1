param (
    [string]$Server,
    [string]$Token,
    [string]$NodeId = "",
    [string]$InstallDir = "C:\ProgramData\MonitorProbe",
    [string]$ServiceName = "MonitorProbe",
    [switch]$Insecure,
    [switch]$DisableRpc,
    [switch]$IncludeBuffer
)

if (-not $Server -or -not $Token) {
    Write-Host "缺少必要参数 Server 或 Token"
    exit 1
}

$ProbeUrl = "$Server/probe-windows-amd64.exe"
$BinPath = "$InstallDir\$ServiceName.exe"

if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
}

Write-Host "正在下载探针: $ProbeUrl"
Invoke-WebRequest -Uri $ProbeUrl -OutFile $BinPath

$ArgsList = "-s `"$Server`" -t `"$Token`""
if ($NodeId) { $ArgsList += " -i `"$NodeId`"" }
if ($Insecure) { $ArgsList += " --insecure" }
if ($DisableRpc) { $ArgsList += " --disable-rpc" }
if ($IncludeBuffer) { $ArgsList += " --include-buffer" }

# 注册为计划任务，随系统启动在后台运行
$Action = New-ScheduledTaskAction -Execute $BinPath -Argument $ArgsList
$Trigger = New-ScheduledTaskTrigger -AtStartup
$Principal = New-ScheduledTaskPrincipal -UserId "SYSTEM" -LogonType ServiceAccount -RunLevel Highest
$Task = New-ScheduledTask -Action $Action -Trigger $Trigger -Principal $Principal

Unregister-ScheduledTask -TaskName $ServiceName -Confirm:$false -ErrorAction SilentlyContinue
Register-ScheduledTask -TaskName $ServiceName -InputObject $Task | Out-Null
Start-ScheduledTask -TaskName $ServiceName

Write-Host "Windows 探针已部署为后台计划任务并启动。"