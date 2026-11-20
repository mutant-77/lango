# Downloads conpty.dll and OpenConsole.exe (x64) from Microsoft's official NuGet package.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$ver  = '1.24.261001001'
$hash = '4d6aaddc1d2385c9f5897df28f33879f699f8f2783315d5204cf3d8c3616ac5f'
$dir  = $PSScriptRoot
$tmp  = Join-Path ([IO.Path]::GetTempPath()) "conpty-$ver"
Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force $tmp | Out-Null
$pkg = Join-Path $tmp 'c.zip'
Invoke-WebRequest "https://api.nuget.org/v3-flatcontainer/microsoft.windows.console.conpty/$ver/microsoft.windows.console.conpty.$ver.nupkg" -OutFile $pkg
$sha = [BitConverter]::ToString([Security.Cryptography.SHA256]::Create().ComputeHash([IO.File]::ReadAllBytes($pkg))).Replace('-','').ToLower()
if ($sha -ne $hash) { throw 'SHA256 of the package does not match' }
Add-Type -AssemblyName System.IO.Compression.FileSystem
[IO.Compression.ZipFile]::ExtractToDirectory($pkg, (Join-Path $tmp 'x'))
Copy-Item (Join-Path $tmp 'x\runtimes\win-x64\native\conpty.dll') $dir -Force
Copy-Item (Join-Path $tmp 'x\build\native\runtimes\x64\OpenConsole.exe') $dir -Force
Remove-Item $tmp -Recurse -Force
Write-Host "conpty $ver ready in $dir"
