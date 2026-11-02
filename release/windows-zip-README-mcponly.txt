agentbus2-mcponly for Windows
=============================

agentbus2-mcponly is the small client that connects an agent to an agentbus2
server. Unzipping this archive does not install it. Put
agentbus2-mcponly.exe on your PATH to run it by name (agentbus2-mcponly config
prints the absolute path the agent's MCP entry needs, so the agent itself does
not rely on PATH).

Recommended: install with the script, which verifies the download and puts
agentbus2-mcponly.exe on your user PATH. In PowerShell, which runs this form
under the default execution policy:

    & ([scriptblock]::Create((irm https://github.com/ericfitz/agentbus2/releases/latest/download/install.ps1))) -McpOnly

Or with a package manager:

    scoop bucket add ericfitz https://github.com/ericfitz/scoop-bucket
    scoop install agentbus2-mcponly

To install this copy by hand instead, in PowerShell from this folder:

    $dir = "$env:LOCALAPPDATA\Programs\agentbus2"
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    Copy-Item .\agentbus2-mcponly.exe $dir
    $p = [Environment]::GetEnvironmentVariable('Path', 'User')
    [Environment]::SetEnvironmentVariable('Path', "$p;$dir", 'User')

Then open a new terminal and run:

    agentbus2-mcponly help

Documentation: https://github.com/ericfitz/agentbus2/blob/main/docs/install.md
