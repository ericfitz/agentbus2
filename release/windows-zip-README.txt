agentbus2 for Windows
====================

Unzipping this archive does not install agentbus2. The harnesses start
agentbus2.exe by name, so it must be on your PATH.

Recommended: install with the script, which verifies the download, copies
agentbus2.exe to %LocalAppData%\Programs\agentbus2 and adds that directory to
your user PATH:

    irm https://github.com/ericfitz/agentbus2/releases/latest/download/install.ps1 | iex

Or with a package manager:

    scoop bucket add ericfitz https://github.com/ericfitz/scoop-bucket
    scoop install agentbus2

To install this copy by hand instead, in PowerShell from this folder:

    $dir = "$env:LOCALAPPDATA\Programs\agentbus2"
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    Copy-Item .\agentbus2.exe $dir
    $p = [Environment]::GetEnvironmentVariable('Path', 'User')
    [Environment]::SetEnvironmentVariable('Path', "$p;$dir", 'User')

Then open a new terminal and run:

    agentbus2 init --global      (once per machine)
    agentbus2 init               (inside each repository)

Documentation: https://github.com/ericfitz/agentbus2/blob/main/docs/install.md
