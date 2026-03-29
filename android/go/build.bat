@echo off
:: ──────────────────────────────────────────────────────────────────────────────
:: build.bat — Compile the Go hysteria2 package into an Android AAR via gomobile.
::
:: Prerequisites:
::   • Go 1.23+  (https://go.dev/dl/)
::   • Android SDK + NDK installed via Android Studio
::     (SDK Manager → SDK Tools → NDK (Side by side))
::   • gomobile installed:
::       go install golang.org/x/mobile/cmd/gomobile@latest
::       gomobile init
::   • JAVA_HOME set to JDK 17
::
:: Usage:
::   cd android\go
::   build.bat
::
:: Output:
::   ..\app\libs\hysteria2.aar
:: ──────────────────────────────────────────────────────────────────────────────
setlocal EnableDelayedExpansion

set "SCRIPT_DIR=%~dp0"
set "OUTPUT_DIR=%SCRIPT_DIR%..\app\libs"
set "AAR_NAME=hysteria2.aar"

:: ── Check Go ──────────────────────────────────────────────────────────────────
where go >nul 2>&1
if errorlevel 1 (
    echo ERROR: go not found. Install Go 1.23+ from https://go.dev/dl/
    exit /b 1
)
echo =^> Go found:
go version

:: ── Check gomobile ────────────────────────────────────────────────────────────
where gomobile >nul 2>&1
if errorlevel 1 (
    echo ERROR: gomobile not found. Install it with:
    echo   go install golang.org/x/mobile/cmd/gomobile@latest
    echo   gomobile init
    exit /b 1
)
echo =^> gomobile found.

:: ── Auto-detect Android NDK if ANDROID_NDK_HOME is not set ───────────────────
if "%ANDROID_NDK_HOME%"=="" (
    if exist "%LOCALAPPDATA%\Android\Sdk\ndk" (
        for /f "delims=" %%d in ('dir /b /ad "%LOCALAPPDATA%\Android\Sdk\ndk" 2^>nul') do (
            set "ANDROID_NDK_HOME=%LOCALAPPDATA%\Android\Sdk\ndk\%%d"
        )
    )
)
if "%ANDROID_NDK_HOME%"=="" (
    echo WARNING: ANDROID_NDK_HOME is not set and NDK was not auto-detected.
    echo Set it manually, e.g.:
    echo   set ANDROID_NDK_HOME=C:\Users\%USERNAME%\AppData\Local\Android\Sdk\ndk\26.3.11579264
) else (
    echo =^> NDK: %ANDROID_NDK_HOME%
)

:: ── Auto-detect ANDROID_HOME if not set ──────────────────────────────────────
if "%ANDROID_HOME%"=="" (
    if exist "%LOCALAPPDATA%\Android\Sdk" (
        set "ANDROID_HOME=%LOCALAPPDATA%\Android\Sdk"
    )
)
if not "%ANDROID_HOME%"=="" (
    echo =^> Android SDK: %ANDROID_HOME%
)

:: ── Download dependencies ─────────────────────────────────────────────────────
echo =^> Downloading Go module dependencies...
cd /d "%SCRIPT_DIR%"
go mod tidy
if errorlevel 1 (
    echo ERROR: go mod tidy failed.
    exit /b 1
)

:: ── Build AAR ─────────────────────────────────────────────────────────────────
echo =^> Building Android AAR (targets: arm, arm64, 386, amd64)...
gomobile bind ^
    -target=android ^
    -androidapi=21 ^
    -o "%SCRIPT_DIR%%AAR_NAME%" ^
    -v ^
    .
if errorlevel 1 (
    echo ERROR: gomobile bind failed.
    exit /b 1
)

:: ── Copy output ───────────────────────────────────────────────────────────────
echo =^> Copying %AAR_NAME% to %OUTPUT_DIR%\
if not exist "%OUTPUT_DIR%" mkdir "%OUTPUT_DIR%"
copy /y "%SCRIPT_DIR%%AAR_NAME%" "%OUTPUT_DIR%\%AAR_NAME%" >nul

set "SOURCES_JAR=%SCRIPT_DIR%hysteria2-sources.jar"
if exist "%SOURCES_JAR%" (
    copy /y "%SOURCES_JAR%" "%OUTPUT_DIR%\hysteria2-sources.jar" >nul
)

echo.
echo Build complete!
echo   %OUTPUT_DIR%\%AAR_NAME%
echo.
echo Next steps:
echo   1. Open android\ in Android Studio.
echo   2. Sync Gradle — it will pick up app\libs\hysteria2.aar automatically.
echo   3. Build ^& run the app.

endlocal
