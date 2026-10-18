#!/usr/bin/env bash
# Shares the receipt/ownership implementation; no vendor CLIs needed for recovery.
set -euo pipefail
installer_directory=${BASH_SOURCE[0]%/*}
[[ ${BASH_SOURCE[0]} == */* ]] || installer_directory=.
source "$installer_directory/install.sh"
run_installer uninstall "$@"
