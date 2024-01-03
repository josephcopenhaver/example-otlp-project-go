#!/bin/bash

set -euxo pipefail

main() {
    local pids=()
    sleep 0 &
    pids+=("$!")
    helm uninstall middle-service --wait &
    pids+=("$!")
    helm uninstall end-service --wait &
    pids+=("$!")

    set +exo pipefail
    local cec=0
    for pid in ${pids[*]}; do
        wait $pid
        local ec="$?"
        if [[ $ec -ne 0 ]]; then
            cec=$ec
        fi
    done
    pids=()
    set -exo pipefail
    if [[ $cec -ne 0 ]]; then
        exit $cec
    fi
}

(set -euxo pipefail; main "$@")
