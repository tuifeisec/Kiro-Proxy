@echo off
chcp 65001 >nul
setlocal

set "MSYS2_MINGW64=C:\msys64\mingw64\bin"
if exist "%MSYS2_MINGW64%\gcc.exe" (
    set "PATH=%MSYS2_MINGW64%;%PATH%"
)

echo ============================================================
echo   Kiro Proxy 桌面版打包 (Windows)
echo ============================================================
echo.

where gcc >nul 2>nul
if errorlevel 1 (
    echo [错误] 未找到 gcc。桌面壳依赖 cgo，需要 MinGW-w64 GCC。
    echo        预期路径: %MSYS2_MINGW64%\gcc.exe
    echo        可安装 MSYS2 后执行: pacman -Sy --noconfirm mingw-w64-x86_64-gcc
    goto err
)

echo [环境] GCC: 
gcc --version | findstr /R "gcc.*"
echo.

echo [1/3] 构建前端 (Vite)...
pushd frontend
call npm run build
if errorlevel 1 (
    popd
    goto err
)
popd
echo.

echo [2/3] 编译 Windows 图标资源...
where windres >nul 2>nul
if errorlevel 1 (
    echo [错误] 未找到 windres。请确认 MSYS2 MinGW-w64 bin 目录已加入 PATH。
    echo        预期路径: %MSYS2_MINGW64%\windres.exe
    echo        可安装 MSYS2 后执行: pacman -Sy --noconfirm mingw-w64-x86_64-binutils
    goto err
)
windres build\windows\kiro-proxy.rc -O coff -o resource_windows.syso
if errorlevel 1 goto err
echo.

echo [3/3] 构建 Go 桌面壳二进制 (含嵌入前端资源和程序图标)...
go build -tags desktop -ldflags="-s -w -H=windowsgui" -trimpath -o kiro-proxy.exe .
if errorlevel 1 goto err

echo.
echo ============================================================
echo   构建完成: kiro-proxy.exe
echo   关闭桌面窗口会隐藏到后台，托盘菜单可退出代理进程
echo ============================================================
for %%I in (kiro-proxy.exe) do echo   大小: %%~zI 字节
echo.
goto end

:err
echo.
echo ============================================================
echo   构建失败，请检查上方错误信息
echo ============================================================
exit /b 1

:end
endlocal
pause
