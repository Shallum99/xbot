$ErrorActionPreference = "Stop"

$repo = "Shallum99/xbot"
$installDir = "$env:LOCALAPPDATA\xbot"

# Detect arch
$arch = if ([Environment]::Is64BitOperatingSystem) {
    if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
} else {
    Write-Error "32-bit Windows is not supported"
    exit 1
}

# Get latest version
$release = Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest"
$version = $release.tag_name

$filename = "xbot_windows_$arch.zip"
$url = "https://github.com/$repo/releases/download/$version/$filename"

Write-Host "Installing xbot $version (windows/$arch)..."

# Download and extract
$tmp = New-TemporaryFile | Rename-Item -NewName { $_.Name + ".zip" } -PassThru
Invoke-WebRequest -Uri $url -OutFile $tmp.FullName

# Create install dir
New-Item -ItemType Directory -Force -Path $installDir | Out-Null

# Extract
Expand-Archive -Path $tmp.FullName -DestinationPath $installDir -Force
Remove-Item $tmp.FullName

# Add to PATH if not already there
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$installDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$installDir", "User")
    Write-Host "Added $installDir to your PATH (restart your terminal to use it)"
}

Write-Host ""
Write-Host "xbot $version installed to $installDir\xbot.exe" -ForegroundColor Green
Write-Host ""
Write-Host "Next: xbot auth --client-id YOUR_ID --client-secret YOUR_SECRET"
