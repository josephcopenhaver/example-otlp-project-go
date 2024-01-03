#!/bin/bash
set -euxo pipefail

main() {
    local gitsha="$(git log --pretty=format:%H -n 1)"

    local pids=()
    sleep 0 &
    pids+=("$!")
    kind load docker-image "josephcopenhaver/middle-service:$gitsha" &
    pids+=("$!")
    kind load docker-image "josephcopenhaver/end-service:$gitsha" &
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


    sleep 0 &
    pids+=("$!")
    helm install -f <(printf 'image:\n  tag: "%s"\nautoscaling:\n  enabled: true\n' "$gitsha") middle-service ./helm/middle-service --wait &
    pids+=("$!")
    helm install -f <(printf 'image:\n  tag: "%s"\nautoscaling:\n  enabled: true\n' "$gitsha") end-service ./helm/end-service --wait &
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
