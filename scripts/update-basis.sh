#!/bin/bash

set -euxo pipefail

main() {
    docker pull golang:1.21-bookworm
    docker pull gcr.io/distroless/static-debian12
}

(set -euxo pipefail; main "$@")
