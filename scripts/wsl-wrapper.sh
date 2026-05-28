#!/usr/bin/env bash
# wsl-wrapper.sh — shim used inside the container.
#
# The upstream autodeploy.ps1 calls `wsl xorriso ...` because it expects to run
# on Windows with WSL providing the Linux tools. Inside this container we ARE
# Linux already, so we just exec the real binary.
#
# Supports `wsl --version`, `wsl --list --verbose`, `wsl <cmd> [args...]`.

set -euo pipefail

if [[ $# -eq 0 ]]; then
    cat <<EOF
WSL shim (autodeploy-web). Forwards calls to native binaries.
Usage: wsl <command> [args...]
EOF
    exit 0
fi

case "${1:-}" in
    --version)
        echo "WSL version (container shim): 1.0"
        echo "Kernel: $(uname -r)"
        exit 0
        ;;
    --list|-l)
        echo "(container shim — no distros, native exec)"
        exit 0
        ;;
    --shutdown|--terminate|--unregister|--install|--update)
        # No-ops in container
        exit 0
        ;;
esac

# Forward to native binary, dropping the "wsl" prefix.
exec "$@"
