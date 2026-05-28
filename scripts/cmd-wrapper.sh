#!/usr/bin/env bash
# cmd-wrapper.sh — shim for Windows cmd.exe called by autodeploy.ps1.
#
# The PS1 uses "cmd /c <command>" to execute shell commands on Windows.
# Inside the container we re-dispatch to bash.
#
# Handles both calling conventions the PS1 may use:
#   cmd /c "wsl xorriso ..."   -> bash -c "wsl xorriso ..."  (single-string)
#   cmd /c wsl xorriso ...     -> exec wsl xorriso ...       (split args)

# Drop /c or /C flag (run-and-exit — default behaviour for us too)
if [[ $# -gt 0 ]] && [[ "${1^^}" == "/C" ]]; then
    shift
fi

if [[ $# -eq 0 ]]; then
    exit 0
elif [[ $# -eq 1 ]]; then
    # Single quoted string passed by PowerShell — run via bash
    exec /bin/bash -c "$1"
else
    # Separate arguments — exec directly so PATH (wsl shim etc.) is found
    exec "$@"
fi
