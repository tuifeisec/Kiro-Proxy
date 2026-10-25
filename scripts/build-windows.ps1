#requires -Version 7.0
[CmdletBinding()]
param([string]$Version = '')

$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($Version)) {
    $Version = Read-Host '请输入版本号（例如 1.0.0）'
}
if ($Version -notmatch '^\d+\.\d+\.\d+$') {
    throw '版本号必须是三段数字，例如 1.0.0。'
}
foreach ($part in $Version.Split('.')) {
    if ([long]$part -gt 65535) { throw 'Windows 资源版本的每段数字不能大于 65535。' }
}

$repoRoot = Split-Path -Parent $PSScriptRoot
$gccCommand = Get-Command gcc.exe -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
if (-not $gccCommand) {
    $localMingwBin = 'C:\msys64\mingw64\bin'
    if (-not (Test-Path -LiteralPath (Join-Path $localMingwBin 'gcc.exe'))) {
        throw 'PATH 中没有 GCC，且未找到本地 MSYS2 MinGW-w64；请先安装或配置构建工具链。'
    }
    Write-Output "PATH 中未发现 GCC，使用本地 MSYS2 工具链：$localMingwBin"
    $env:PATH = "$localMingwBin;$env:PATH"
}
Write-Output ('GCC: ' + (Get-Command gcc.exe -CommandType Application | Select-Object -First 1).Source)
foreach ($toolName in @('go.exe', 'gcc.exe', 'windres.exe', 'objdump.exe', 'npm.cmd', 'git.exe')) {
    $null = Get-Command $toolName -CommandType Application -ErrorAction Stop
}
$env:CGO_ENABLED = '1'
$env:CC = 'gcc'
$env:CXX = 'g++'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'

$outputDir = Join-Path $repoRoot "build/windows-release/$Version"
$releaseDir = Join-Path $repoRoot 'release'
$archiveName = "kiro-proxy-v$Version-windows-amd64-unsigned.zip"
$archivePath = Join-Path $releaseDir $archiveName
if (Test-Path -LiteralPath $outputDir) {
    throw "构建目标已存在，请先备份旧产物：$outputDir"
}
if (Test-Path -LiteralPath $archivePath) {
    throw "发行包已存在，请先备份旧产物：$archivePath"
}
$null = New-Item -ItemType Directory -Path $outputDir -Force
$null = New-Item -ItemType Directory -Path $releaseDir -Force
$tempDir = Join-Path ([System.IO.Path]::GetTempPath()) ('kiro-windows-build-' + [guid]::NewGuid())
$null = New-Item -ItemType Directory -Path $tempDir
$resourcePath = Join-Path $repoRoot 'resource_windows.syso'
$resourceBackup = Join-Path $tempDir 'resource.original.syso'
$resourceExisted = Test-Path -LiteralPath $resourcePath
if ($resourceExisted) { Copy-Item -LiteralPath $resourcePath -Destination $resourceBackup }

Push-Location $repoRoot
try {
    & npm.cmd --prefix frontend ci
    if ($LASTEXITCODE -ne 0) { throw "npm ci 失败：$LASTEXITCODE" }
    & npm.cmd --prefix frontend run build
    if ($LASTEXITCODE -ne 0) { throw "前端构建失败：$LASTEXITCODE" }

    $resourceSource = Get-Content -LiteralPath (Join-Path $repoRoot 'build/windows/kiro-proxy.rc') -Raw
    $numericVersion = $Version.Replace('.', ',') + ',0'
    $resourceSource = $resourceSource -replace '(?m)^\s*FILEVERSION\s+.+$', " FILEVERSION $numericVersion"
    $resourceSource = $resourceSource -replace '(?m)^\s*PRODUCTVERSION\s+.+$', " PRODUCTVERSION $numericVersion"
    $resourceSource = $resourceSource -replace '(VALUE "(?:FileVersion|ProductVersion)", ")[^"]+(\\0")', ('${1}' + $Version + '${2}')
    $tempResource = Join-Path $tempDir 'kiro-proxy.rc'
    [IO.File]::WriteAllText($tempResource, $resourceSource, [Text.UTF8Encoding]::new($false))
    & windres.exe -I (Join-Path $repoRoot 'build/windows') -i $tempResource -O coff -o $resourcePath
    if ($LASTEXITCODE -ne 0) { throw "Windows 资源编译失败：$LASTEXITCODE" }

    $executablePath = Join-Path $outputDir 'kiro-proxy.exe'
    & go.exe build -tags desktop -ldflags "-s -w -H=windowsgui -X kiro-proxy/buildinfo.AppVersion=$Version" -trimpath -o $executablePath .
    if ($LASTEXITCODE -ne 0) { throw "Go 桌面构建失败：$LASTEXITCODE" }
    $binaryHeaders = @(& objdump.exe -p $executablePath)
    if ($LASTEXITCODE -ne 0) { throw "PE 依赖检查失败：$LASTEXITCODE" }
    $importedDLLs = @($binaryHeaders | ForEach-Object {
        if ($_ -match 'DLL Name:\s*(\S+)') { $Matches[1] }
    } | Sort-Object -Unique)
    if ($importedDLLs.Count -eq 0) { throw '未读取到 PE 系统 DLL 导入表。' }
    $windowsSystemDLLs = @(
        'advapi32.dll', 'bcrypt.dll', 'comctl32.dll', 'comdlg32.dll', 'crypt32.dll', 'dnsapi.dll',
        'dwmapi.dll', 'gdi32.dll', 'iphlpapi.dll', 'kernel32.dll', 'msvcrt.dll', 'ntdll.dll',
        'ole32.dll', 'oleaut32.dll', 'psapi.dll', 'rpcrt4.dll', 'secur32.dll', 'setupapi.dll',
        'shell32.dll', 'shlwapi.dll', 'ucrtbase.dll', 'user32.dll', 'userenv.dll', 'uxtheme.dll',
        'version.dll', 'winhttp.dll', 'wininet.dll', 'winmm.dll', 'winspool.drv', 'ws2_32.dll',
        'wsock32.dll', 'wtsapi32.dll'
    )
    foreach ($dllName in $importedDLLs) {
        if ($windowsSystemDLLs -notcontains $dllName.ToLowerInvariant() -and
            $dllName -notmatch '^(api-ms-win-|ext-ms-win-)') {
            throw "发行包包含未附带的非系统 DLL 依赖：$dllName"
        }
    }
    Write-Output ('PE imports: ' + ($importedDLLs -join ', '))
    $fileVersion = [Diagnostics.FileVersionInfo]::GetVersionInfo($executablePath).ProductVersion
    if ($fileVersion -ne $Version) { throw "Windows 资源版本不匹配：$fileVersion" }
    & git.exe rev-parse HEAD
    if ($LASTEXITCODE -ne 0) { throw '无法读取构建提交。' }
    $revision = (& git.exe rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0) { throw '无法读取构建提交。' }
    $binarySHA256 = (Get-FileHash -LiteralPath $executablePath -Algorithm SHA256).Hash.ToLowerInvariant()
    @"
Kiro Proxy $Version - Windows x64
本版本不需要机器授权或激活。
需要 Microsoft Edge WebView2 Runtime；桌面窗口关闭后程序留在托盘，使用托盘菜单退出。
本发行包未进行 Windows 代码签名。
源码提交：$revision
"@ | Set-Content -LiteralPath (Join-Path $outputDir 'README.txt') -Encoding utf8NoBOM
    Compress-Archive -LiteralPath @($executablePath, (Join-Path $outputDir 'README.txt')) -DestinationPath $archivePath
    $archiveSHA256 = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText("$archivePath.sha256", "$archiveSHA256  $archiveName`n", [Text.UTF8Encoding]::new($false))
    $manifest = [ordered]@{
        version = $Version; revision = $revision; platform = 'windows'; arch = 'amd64'
        binary_sha256 = $binarySHA256; archive = $archiveName; archive_sha256 = $archiveSHA256
        developer_signed = $false; notarized = $false; imported_dlls = $importedDLLs
    }
    $manifest | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $releaseDir 'windows-amd64-manifest.json') -Encoding utf8NoBOM
    Write-Output "构建完成：$archivePath"
    Write-Output "SHA256：$archiveSHA256"
} finally {
    Pop-Location
    if ($resourceExisted) {
        Copy-Item -LiteralPath $resourceBackup -Destination $resourcePath -Force
    } elseif (Test-Path -LiteralPath $resourcePath) {
        Remove-Item -LiteralPath $resourcePath
    }
    # 保留临时资源及原文件备份，便于定位失败及恢复。
    Write-Output "资源构建记录：$tempDir"
}