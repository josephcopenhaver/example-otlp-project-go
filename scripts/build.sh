#!/bin/bash

set -euxo pipefail

build() {
    local cmd="$1"
    local gitsha="$2"
    mkdir -p build/logs
    docker build -f "Dockerfiles/$cmd" --build-arg "GitSHA=$gitsha" -t "josephcopenhaver/$cmd:latest" -t "josephcopenhaver/$cmd:$gitsha" . > "build/logs/build-$cmd.out.log" 2> "build/logs/build-$cmd.err.log"
}

main() {
    local gitsha="$(git log --pretty=format:%H -n 1)"
    
    local pids=()
    sleep 0 &
    pids+=("$!")
    build middle-service $gitsha &
    pids+=("$!")
    build end-service $gitsha &
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
