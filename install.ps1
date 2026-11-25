# Installs sh-mcp from GitHub Releases on Windows.
#
#   irm https://raw.githubusercontent.com/yh2237/sh-mcp/main/install.ps1 | iex
#
# $env:SH_MCP_VERSION     release tag to install, e.g. v1.2.0 (default: latest)
# $env:SH_MCP_INSTALL_DIR where to put sh-mcp.exe (default: %LOCALAPPDATA%\Programs\sh-mcp)

# A script block keeps variables out of the caller's session when run via iex.
& {
	$ErrorActionPreference = 'Stop'
	# Invoke-WebRequest is very slow with the progress bar in Windows PowerShell.
	$ProgressPreference = 'SilentlyContinue'
	[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

	$base = if ($env:SH_MCP_BASE_URL) { $env:SH_MCP_BASE_URL } else { 'https://github.com/yh2237/sh-mcp/releases' }
	$version = if ($env:SH_MCP_VERSION) { $env:SH_MCP_VERSION } else { 'latest' }
	$dir = if ($env:SH_MCP_INSTALL_DIR) { $env:SH_MCP_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\sh-mcp' }

	# PROCESSOR_ARCHITEW6432 holds the real architecture in a 32-bit PowerShell.
	$cpu = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
	$arch = switch ($cpu) {
		'AMD64' { 'amd64' }
		'ARM64' { 'arm64' }
		default { throw "install.ps1: unsupported architecture: $cpu" }
	}

	$asset = "sh-mcp-windows-$arch.exe"
	$url = if ($version -eq 'latest') { "$base/latest/download" } else { "$base/download/$version" }

	$tmp = Join-Path ([IO.Path]::GetTempPath()) ("sh-mcp-" + [Guid]::NewGuid())
	New-Item -ItemType Directory -Path $tmp | Out-Null
	try {
		Write-Host "Downloading $asset ($version)"
		$exe = Join-Path $tmp $asset
		$sums = Join-Path $tmp 'SHA256SUMS'
		foreach ($f in @(@($asset, $exe), @('SHA256SUMS', $sums))) {
			try {
				Invoke-WebRequest -UseBasicParsing -Uri "$url/$($f[0])" -OutFile $f[1]
			} catch {
				throw "install.ps1: download failed: $url/$($f[0]) ($($_.Exception.Message))"
			}
		}

		$want = $null
		foreach ($line in Get-Content $sums) {
			$fields = -split $line
			if ($fields.Count -eq 2 -and $fields[1].TrimStart('*') -eq $asset) { $want = $fields[0].ToLower() }
		}
		if (-not $want) { throw "install.ps1: $asset is not listed in SHA256SUMS" }
		$got = (Get-FileHash -Algorithm SHA256 -Path $exe).Hash.ToLower()
		if ($want -ne $got) { throw "install.ps1: checksum mismatch for $asset (want $want, got $got)" }

		New-Item -ItemType Directory -Force -Path $dir | Out-Null
		$dest = Join-Path $dir 'sh-mcp.exe'
		if (Test-Path $dest) {
			# A running exe cannot be overwritten but can be renamed, so updates
			# work while sh-mcp is in use.
			$old = "$dest.old"
			Remove-Item -Force $old -ErrorAction SilentlyContinue
			Move-Item -Force $dest $old
		}
		Move-Item $exe $dest
		Remove-Item -Force "$dest.old" -ErrorAction SilentlyContinue
		Write-Host "Installed $(& $dest --version) to $dest"
	} finally {
		Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
	}

	$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
	$entries = if ($userPath) { $userPath -split ';' } else { @() }
	if ($entries -notcontains $dir) {
		[Environment]::SetEnvironmentVariable('Path', (($entries + $dir) -join ';').TrimStart(';'), 'User')
		$env:Path = "$env:Path;$dir"
		Write-Host "Added $dir to your user PATH. Open a new terminal to use sh-mcp there."
	}
}
