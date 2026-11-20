@echo off
setlocal

cd /d "%~dp0"

echo Closing Lango.exe if it is running...
taskkill /F /IM Lango.exe /T >nul 2>&1

set "WAILS_CMD=wails"
where wails >nul 2>&1
if %ERRORLEVEL% NEQ 0 (
    if exist "%USERPROFILE%\go\bin\wails.exe" (
        set "WAILS_CMD=%USERPROFILE%\go\bin\wails.exe"
    ) else (
        echo Error: wails not found in PATH or at %USERPROFILE%\go\bin\wails.exe
        exit /b 1
    )
)

set MAX_TRIES=3
set TRY=1

:build
echo.
echo === Building Lango (attempt %TRY% of %MAX_TRIES%) ===
"%WAILS_CMD%" build -skipbindings
if %ERRORLEVEL% EQU 0 goto ok

REM Defender often briefly locks the freshly written .exe.
REM Retrying without -clean usually resolves it on its own.
if %TRY% GEQ %MAX_TRIES% goto fail
set /a TRY+=1
echo Failed, probably due to a transient Defender lock. Retrying...
timeout /t 2 /nobreak >nul
goto build

:ok
echo.
if not exist "third_party\conpty\conpty.dll" goto fetch
if not exist "third_party\conpty\OpenConsole.exe" goto fetch
goto copyconpty
:fetch
powershell -NoProfile -ExecutionPolicy Bypass -File "third_party\conpty\fetch.ps1" || goto failconpty
:copyconpty
echo Copying conpty to build\bin\...
if not exist "build\bin" mkdir "build\bin"
copy /y "third_party\conpty\conpty.dll" "build\bin\" >nul || goto failconpty
copy /y "third_party\conpty\OpenConsole.exe" "build\bin\" >nul || goto failconpty
echo === Build OK: build\bin\Lango.exe ===
exit /b 0

:fail
echo.
echo === Failed after %MAX_TRIES% attempts. Check the Wails log above. ===
exit /b 1

:failconpty
echo.
echo === Failed to fetch/copy conpty (third_party\conpty). Check the output above. ===
exit /b 1
