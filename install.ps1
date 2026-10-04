# QS-PodScript installer for Windows
#
# Double-click install.cmd (it starts this script). What it does:
#   - copies QS-PodScript to your user folder (no admin rights needed)
#   - checks the graphics card and lets QS-PodScript pick the fastest working
#     transcription: NVIDIA (CUDA), AMD / Intel Arc / NVIDIA (Vulkan) or the
#     processor - every graphics option is tested for real, with fallback
#   - downloads ffmpeg, whisper.cpp and the models (about 2.5 GB, once)
#   - Start menu shortcut, optional desktop shortcut and autostart
#   - writes uninstall.cmd
# Running it again (e.g. from a newer package) updates QS-PodScript and keeps
# all transcripts, people and settings.
#
# Options (install.cmd passes them on):
#   -y  (-Yes)           answer every question with yes
#   -n  (-No)            answer every question with no
#   -Defaults            no questions, use the default answers
#   -Gpu auto|cuda|vulkan|cpu
#   -Model NAME          turbo, turbo-q5, large-v3, medium.en, small.en, base.en
#   -Dir PATH            install folder (default: %LOCALAPPDATA%\qs-podscript)
#   -Desktop / -Autostart / -NoAutostart
#   -NoSetup             only copy files (no downloads)
#   -GpuSpeakers         speaker detection on the NVIDIA graphics card too,
#                        without asking (about 1.5 GB download; also in Setup)
#   -NoGpuSpeakers       speaker detection on the processor (removes that part)
#   Connect to a shared server (like Setup -> "Add a server"; without these
#   the installer asks once, unless a server is saved already):
#   -Connect ADDRESS     the server, e.g. https://transcribe.example.org
#   -ConnectUser NAME    your login there (the password is asked for, hidden)
#   -ConnectLabel TEXT   name for the server on this computer
#   -ConnectWork         "Transcribe for this server"

param(
    [Alias('y')][switch]$Yes,
    [Alias('n')][switch]$No,
    [switch]$Defaults,
    [ValidateSet('auto', 'cuda', 'vulkan', 'cpu')][string]$Gpu = 'auto',
    [string]$Model = '',
    [string]$Dir = (Join-Path $env:LOCALAPPDATA 'qs-podscript'),
    [switch]$Desktop,
    [switch]$Autostart,
    [switch]$NoAutostart,
    [switch]$NoSetup,
    [switch]$GpuSpeakers,
    [switch]$NoGpuSpeakers,
    [string]$Connect = '',
    [string]$ConnectUser = '',
    [string]$ConnectLabel = '',
    [switch]$ConnectWork
)

$ErrorActionPreference = 'Stop'
if ($Yes -and $No) { Write-Host "Use either -y or -n, not both." -ForegroundColor Red; exit 1 }
$ProgressPreference = 'SilentlyContinue'
$Src = $PSScriptRoot

function Step($t) { Write-Host ""; Write-Host "== $t" -ForegroundColor Cyan }
function Ok($t) { Write-Host "   ok  $t" -ForegroundColor Green }
function Info($t) { Write-Host "       $t" }
function Warn($t) { Write-Host "   !   $t" -ForegroundColor Yellow }
function Fail($t) {
    Write-Host ""
    Write-Host "   X   $t" -ForegroundColor Red
    Write-Host "       Details: $Dir\install.log" -ForegroundColor Red
    try { Stop-Transcript | Out-Null } catch {}
    exit 1
}
function Ask($question, [bool]$default) {
    if ($Yes) { Info "$question -> yes (-y)"; return $true }
    if ($No) { Info "$question -> no (-n)"; return $false }
    if ($Defaults) { return $default }
    $hint = if ($default) { '[Y/n]' } else { '[y/N]' }
    $a = Read-Host "   ?   $question $hint"
    if ([string]::IsNullOrWhiteSpace($a)) { return $default }
    return $a.Trim().ToLower().StartsWith('y')
}

Write-Host ""
Write-Host "QS-PodScript installer" -ForegroundColor White

# ---------------------------------------------------------------- checks
Step "Checking this computer"
if (-not [Environment]::Is64BitOperatingSystem) { Fail "QS-PodScript needs 64-bit Windows." }
$os = [Environment]::OSVersion.Version
if ($os.Major -lt 10) { Fail "QS-PodScript needs Windows 10 or newer." }
Ok ("Windows {0}.{1} build {2}, 64-bit" -f $os.Major, $os.Minor, $os.Build)

if (-not (Test-Path (Join-Path $Src 'qs-podscript.exe'))) {
    Fail "qs-podscript.exe not found next to this script - unzip the whole package first and run install.cmd from that folder."
}

$sameFolder = $false
try { $sameFolder = ((Resolve-Path $Src).Path.TrimEnd('\') -ieq [IO.Path]::GetFullPath($Dir).TrimEnd('\')) } catch {}

New-Item -ItemType Directory -Force -Path $Dir | Out-Null
try { Start-Transcript -Path (Join-Path $Dir 'install.log') -Force | Out-Null } catch {}

# ---------------------------------------------------------------- old name
# Before the rename the app was called "podscribe" and lived in
# %LOCALAPPDATA%\podscribe. Move its data (transcripts, people, voice samples,
# models, graphics card build) over and remove the old shortcuts.
$oldDir = Join-Path $env:LOCALAPPDATA 'podscribe'
$hadOldAutostart = $false
if ((Test-Path $oldDir) -and -not ([IO.Path]::GetFullPath($oldDir).TrimEnd('\') -ieq [IO.Path]::GetFullPath($Dir).TrimEnd('\'))) {
    Step "Moving the old 'podscribe' installation to QS-PodScript"
    $oldRunning = @(Get-Process -Name podscribe -ErrorAction SilentlyContinue)
    if ($oldRunning.Count -gt 0) {
        if (Ask "The old podscribe is running. Stop it now? (a running episode goes back into the queue)" $true) {
            $oldRunning | Stop-Process -Force
            Start-Sleep -Seconds 2
        }
        else { Fail "Close the old podscribe first, then run the installer again." }
    }
    $oldData = Join-Path $oldDir 'data'
    $newData = Join-Path $Dir 'data'
    if ((Test-Path $oldData) -and -not (Test-Path $newData)) {
        try { Move-Item -Path $oldData -Destination $newData -ErrorAction Stop }
        catch { Fail "Could not move $oldData to $newData : $($_.Exception.Message)" }
        Ok "Transcripts, people, models and the graphics card build moved to $Dir"
        Remove-Item -Path $oldDir -Recurse -Force -ErrorAction SilentlyContinue
    }
    elseif (Test-Path $newData) {
        Warn "Both $oldDir and $Dir contain data - keeping the new one."
        Warn "The old folder is left untouched; delete it yourself once you don't need it."
    }
    else { Remove-Item -Path $oldDir -Recurse -Force -ErrorAction SilentlyContinue }
    $oldStartup = Join-Path ([Environment]::GetFolderPath('Startup')) 'podscribe.lnk'
    if (Test-Path $oldStartup) { $hadOldAutostart = $true }
    foreach ($l in @((Join-Path ([Environment]::GetFolderPath('Programs')) 'podscribe.lnk'),
            (Join-Path ([Environment]::GetFolderPath('Desktop')) 'podscribe.lnk'), $oldStartup)) {
        if (Test-Path $l) { Remove-Item $l -Force -ErrorAction SilentlyContinue }
    }
    Ok "Old shortcuts removed"
}

$free = (Get-PSDrive -Name ([IO.Path]::GetPathRoot([IO.Path]::GetFullPath($Dir)).Substring(0, 1))).Free
if ($free -lt 4GB) { Warn ("Only {0:N1} GB free on this drive - QS-PodScript needs about 3 GB plus ~11 MB per hour of audio." -f ($free / 1GB)) }
else { Ok ("{0:N0} GB free disk space" -f ($free / 1GB)) }

# ---------------------------------------------------------------- graphics
Step "Graphics card"
$gpus = @()
try { $gpus = @(Get-CimInstance Win32_VideoController | ForEach-Object { $_.Name }) } catch {}
foreach ($g in $gpus) { Info $g }
$hasNvidia = $gpus -match 'NVIDIA'
$hasAmd = $gpus | Where-Object { $_ -match 'Radeon|AMD' -and $_ -notmatch 'Radeon\(TM\) Graphics' }
$hasArc = $gpus -match 'Arc'
$vulkanDll = Test-Path (Join-Path $env:SystemRoot 'System32\vulkan-1.dll')

if ($hasNvidia) {
    $smi = Join-Path $env:SystemRoot 'System32\nvidia-smi.exe'
    if (Test-Path $smi) { Ok "NVIDIA driver found - QS-PodScript will try CUDA first, then Vulkan." }
    else { Warn "NVIDIA card found but no NVIDIA driver (nvidia-smi) - install the driver from nvidia.com for GPU speed." }
}
elseif ($hasAmd -or $hasArc) {
    if ($vulkanDll) { Ok "QS-PodScript will use the graphics card via Vulkan." }
    else { Warn "No Vulkan runtime found - install the current AMD/Intel graphics driver for GPU speed." }
}
else {
    Info "No suitable graphics card found - transcription runs on the processor (slower, but works)."
}
$gpuLikely = ($hasNvidia -or (($hasAmd -or $hasArc) -and $vulkanDll))

if ($Model -eq '') { $Model = if ($gpuLikely -and $Gpu -ne 'cpu') { 'turbo' } else { 'turbo-q5' } }
Info "Speech model: $Model"

# ---------------------------------------------------------------- stop running copy
$running = @(Get-Process -Name qs-podscript -ErrorAction SilentlyContinue | Where-Object {
        $_.Path -and ($_.Path -like (Join-Path $Dir '*'))
    })
if ($running.Count -gt 0) {
    Step "QS-PodScript is running"
    if (Ask "Stop it now to update? (a running episode goes back into the queue)" $true) {
        $running | Stop-Process -Force
        Start-Sleep -Seconds 2
        Ok "Stopped"
    }
    else { Fail "Close QS-PodScript first, then run the installer again." }
}

# ---------------------------------------------------------------- copy files
Step "Installing QS-PodScript to $Dir"
if (-not $sameFolder) {
    $items = @('qs-podscript.exe', '*.dll', 'README.txt', 'install.ps1', 'install.cmd', 'gpu')
    foreach ($i in $items) {
        $p = Join-Path $Src $i
        if (Test-Path $p) { Copy-Item -Path $p -Destination $Dir -Recurse -Force }
    }
}
# files from a downloaded zip carry a "from the internet" mark -> SmartScreen
# prompts; the user already decided to install, so remove it
Get-ChildItem -Path $Dir -Recurse -File -ErrorAction SilentlyContinue |
    Where-Object { $_.FullName -notlike (Join-Path $Dir 'data\*') } |
    Unblock-File -ErrorAction SilentlyContinue
$ver = (& (Join-Path $Dir 'qs-podscript.exe') version) 2>$null
Ok "$ver installed"
if ((Test-Path (Join-Path $Dir 'data\qs-podscript.db')) -or (Test-Path (Join-Path $Dir 'data\podscribe.db'))) { Ok "Existing transcripts and settings kept" }

$exe = Join-Path $Dir 'qs-podscript.exe'
function Invoke-QS([string[]]$a) {
    $prev = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
    try { $out = (& $exe @a 2>&1 | Out-String) } finally { $ErrorActionPreference = $prev }
    return @{ Code = $LASTEXITCODE; Out = $out }
}

# ---------------------------------------------------------------- setup
if (-not $NoSetup) {
    Step "Downloading tools and models (about 2.5 GB the first time)"
    Info "This can take a while. The graphics card options are tested one by one."
    Write-Host ""
    & $exe setup --gpu $Gpu --model $Model
    if ($LASTEXITCODE -ne 0) { Fail "Setup failed (see above). Fix the problem and run the installer again - finished downloads are kept." }
}

# ---------------------------------------------------------------- speaker detection on the graphics card
# Optional (NVIDIA only). QS-PodScript does the work itself (same as Setup ->
# Speaker detection): sherpa-onnx's GPU build, NVIDIA's CUDA 13 / cuDNN 9
# libraries and the Visual C++ runtime (pinned versions, checked by SHA-256),
# then a test. An update that brings the normal libraries back is fixed by
# QS-PodScript at its next start.
$gpuSpkDir = Join-Path $Dir 'data\tools\onnxruntime-gpu'
function Install-GpuSpeakers {
    & $exe gpu-speakers install
    switch ($LASTEXITCODE) {
        0 { Ok "Speaker detection uses the graphics card (see above)." }
        2 { Warn "Not possible on this computer (see above) - speaker detection runs on the processor." }
        default { Warn "QS-PodScript uses the processor for speaker detection. Remove this part again with: install.cmd -NoGpuSpeakers (or in Setup)." }
    }
}
if ($NoGpuSpeakers) {
    if ((Test-Path $gpuSpkDir) -or (Test-Path (Join-Path $Dir 'cuda'))) {
        Step "Speaker detection on the graphics card"
        $null = Invoke-QS @('gpu-speakers', 'remove')
        Ok "Removed - speaker detection runs on the processor"
    }
}
elseif (-not $NoSetup -and $Gpu -ne 'cpu') {
    if (Test-Path $gpuSpkDir) {
        Step "Speaker detection on the graphics card"
        Info "Updating and testing it (downloads only what changed)"
        Install-GpuSpeakers
    }
    else {
        $st = (Invoke-QS @('gpu-speakers', 'status')).Out
        if ($st -match 'GPU-SPEAKERS POSSIBLE') {
            Step "Speaker detection on the graphics card"
            Info "Speaker detection (telling voices apart) takes about as long as the transcription on the processor."
            Info "On your NVIDIA card it is many times faster. This downloads about 1.5 GB of NVIDIA libraries once."
            if ($GpuSpeakers -or (Ask "Use the graphics card for speaker detection too?" $true)) { Install-GpuSpeakers }
            else { Info "Speaker detection runs on the processor (add it later: Setup -> Speaker detection)." }
        }
        elseif ($GpuSpeakers) {
            Step "Speaker detection on the graphics card"
            Warn (($st -split "`r?`n" | Where-Object { $_ -like 'GPU-SPEAKERS*' } | Select-Object -First 1) -replace '^GPU-SPEAKERS \w+ ', 'Not possible here: ')
        }
    }
}

# ---------------------------------------------------------------- shared server (optional)
# Same as Setup -> "Where you work" -> "Add a server". The password is read
# hidden, handed to qs-podscript through an environment variable and never
# written to the install log.
function Connect-Server {
    for ($try = 1; $try -le 3; $try++) {
        $sec = Read-Host "   ?   Password for $ConnectUser on the server" -AsSecureString
        $b = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($sec)
        try { $pw = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($b) } finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($b) }
        if ([string]::IsNullOrEmpty($pw)) { Warn "No password given."; return $false }
        $a = @('connect', $Connect, '--user', $ConnectUser)
        if ($ConnectLabel) { $a += @('--label', $ConnectLabel) }
        if ($ConnectWork) { $a += '--work' }
        $env:QSPODSCRIPT_PASSWORD = $pw
        try { $r = Invoke-QS $a } finally { Remove-Item Env:QSPODSCRIPT_PASSWORD -ErrorAction SilentlyContinue; $pw = $null }
        if ($r.Code -eq 0) {
            Ok "Connected to $Connect as $ConnectUser"
            if ($ConnectWork) { Info '"Start transcribing" also does this server''s episodes (after your own).' }
            return $true
        }
        $msg = ($r.Out -split "`n" | Where-Object { $_ -match 'error' } | Select-Object -First 1) -replace '^\s*ERROR:\s*', ''
        Warn $msg.Trim()
        if ($try -lt 3 -and -not (Ask "Try again?" $true)) { return $false }
    }
    return $false
}
if (-not $NoSetup -or $Connect) {
    $saved = (Invoke-QS @('connect', 'list')).Out
    $hasServers = $saved -notmatch 'No servers saved'
    $later = 'Not connected. Add the server later in QS-PodScript: Setup -> Where you work -> Add a server.'
    if ($Connect) {
        Step "Connecting to the shared server"
        if (-not $ConnectUser) { $ConnectUser = (Read-Host "   ?   Your name (login) on the server").Trim() }
        if (-not $ConnectUser) { Warn "-Connect needs -ConnectUser NAME - skipped." }
        elseif (-not (Connect-Server)) { Warn $later }
    }
    elseif (-not $hasServers -and -not ($Yes -or $No -or $Defaults)) {
        Step "Shared server (optional)"
        Info "If someone runs a shared QS-PodScript server (e.g. for a podcast you help with),"
        Info "this computer can connect to it now - to work on its podcasts and help transcribing."
        Info "You can also do this later in QS-PodScript: Setup -> Where you work -> Add a server."
        if (Ask "Connect to a shared server now?" $false) {
            $Connect = (Read-Host "   ?   Server address (e.g. https://transcribe.example.org)").Trim()
            $ConnectUser = (Read-Host "   ?   Your name (login) on the server").Trim()
            $ConnectLabel = (Read-Host "   ?   Name for it on this computer (optional, Enter to skip)").Trim()
            if (Ask 'Transcribe for this server when you press "Start transcribing"?' $false) { $ConnectWork = [switch]$true }
            if ($Connect -and $ConnectUser) { if (-not (Connect-Server)) { Warn $later } }
            else { Info "Skipped (address or name missing)." }
        }
    }
}

# ---------------------------------------------------------------- shortcuts
Step "Shortcuts"
$shell = New-Object -ComObject WScript.Shell
function New-Shortcut($path, $arguments, [int]$style) {
    $s = $shell.CreateShortcut($path)
    $s.TargetPath = Join-Path $Dir 'qs-podscript.exe'
    $s.Arguments = $arguments
    $s.WorkingDirectory = $Dir
    $s.WindowStyle = $style
    $s.Description = 'QS-PodScript - podcast transcription'
    $s.Save()
}
$programs = [Environment]::GetFolderPath('Programs')
New-Shortcut (Join-Path $programs 'QS-PodScript.lnk') '' 1
Ok "Start menu: QS-PodScript"
if ($Desktop -or (Ask "Also a desktop shortcut?" $false)) {
    New-Shortcut (Join-Path ([Environment]::GetFolderPath('Desktop')) 'QS-PodScript.lnk') '' 1
    Ok "Desktop shortcut"
}
$startupLnk = Join-Path ([Environment]::GetFolderPath('Startup')) 'QS-PodScript.lnk'
$wantAuto = if ($Autostart) { $true } elseif ($NoAutostart) { $false } else { Ask "Start QS-PodScript automatically when you log in (minimized, open http://127.0.0.1:8321/ to use it)?" ($hadOldAutostart -or (Test-Path $startupLnk)) }
if ($wantAuto) {
    New-Shortcut $startupLnk 'serve --no-browser' 7
    Ok "Autostart at login (minimized window)"
}
elseif (Test-Path $startupLnk) { Remove-Item $startupLnk -Force; Info "Autostart removed" }

# ---------------------------------------------------------------- uninstaller
$un = @'
@echo off
rem runs itself from %TEMP%, so it can delete the QS-PodScript folder it lives in
if not "%~1"=="--from-temp" (
  copy /y "%~f0" "%TEMP%\qs-podscript-uninstall.cmd" >nul
  "%TEMP%\qs-podscript-uninstall.cmd" --from-temp "%~dp0"
  exit /b
)
set "D=%~2"
echo This removes QS-PodScript from %D%
choice /M "Also delete all transcripts, people and downloaded models (the data folder)"
set DELALL=%ERRORLEVEL%
taskkill /IM qs-podscript.exe /F >nul 2>&1
timeout /t 2 /nobreak >nul
del "%APPDATA%\Microsoft\Windows\Start Menu\Programs\QS-PodScript.lnk" >nul 2>&1
del "%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup\QS-PodScript.lnk" >nul 2>&1
del "%USERPROFILE%\Desktop\QS-PodScript.lnk" >nul 2>&1
cd /d "%TEMP%"
if "%DELALL%"=="1" (
  rmdir /s /q "%D%"
  echo QS-PodScript and all its data removed.
) else (
  del /q "%D%qs-podscript.exe" "%D%*.dll" "%D%install.*" "%D%README.txt" "%D%uninstall.cmd" >nul 2>&1
  rmdir /s /q "%D%gpu" >nul 2>&1
  echo QS-PodScript removed. The data folder was kept: %D%data
)
pause
'@
Set-Content -Path (Join-Path $Dir 'uninstall.cmd') -Value $un -Encoding ASCII
Ok "Uninstaller: $Dir\uninstall.cmd"

# ---------------------------------------------------------------- summary
Step "Done"
$log = Join-Path $Dir 'data\qs-podscript.log'
if (Test-Path $log) {
    $line = Select-String -Path $log -Pattern 'Transcription uses the graphics card|Transcription will run on the processor' | Select-Object -Last 1
    if ($line) { Ok ($line.Line -replace '^\S+ \S+ ', '') }
}
Info "Start QS-PodScript from the Start menu (or: $Dir\qs-podscript.exe)."
Info "It opens in your browser. Close the QS-PodScript window to quit."
try { Stop-Transcript | Out-Null } catch {}
if (-not $NoSetup) {
    if (Ask "Start QS-PodScript now?" $true) { Start-Process -FilePath (Join-Path $Dir 'qs-podscript.exe') -WorkingDirectory $Dir }
}
