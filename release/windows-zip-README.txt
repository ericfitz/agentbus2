agentmesh for Windows
====================

Unzipping this archive does not install agentmesh. The harnesses start
agentmesh.exe by name, so it must be on your PATH.

Recommended: install with the script, which verifies the download, copies
agentmesh.exe to %LocalAppData%\Programs\agentmesh and adds that directory to
your user PATH:

    irm https://github.com/ericfitz/agentmesh/releases/latest/download/install.ps1 | iex

Or with a package manager:

    scoop bucket add ericfitz https://github.com/ericfitz/scoop-bucket
    scoop install agentmesh

To install this copy by hand instead, in PowerShell from this folder:

    $dir = "$env:LOCALAPPDATA\Programs\agentmesh"
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    Copy-Item .\agentmesh.exe $dir
    $p = [Environment]::GetEnvironmentVariable('Path', 'User')
    [Environment]::SetEnvironmentVariable('Path', "$p;$dir", 'User')

Then open a new terminal and run:

    agentmesh init --global      (once per machine)
    agentmesh init               (inside each repository)

Documentation: https://github.com/ericfitz/agentmesh/blob/main/docs/install.md
