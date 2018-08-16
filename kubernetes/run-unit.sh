#!/usr/bin/env bash

set -e
echo -e '- Staring wr Kubernetes unit and integration tests'
# Environment variables.
echo -e '- Setting up the test environment..'
SCRIPT_ROOT=$(dirname ${BASH_SOURCE})/..
export KUBECONFIG=${KUBECONFIG:-$HOME/.kube/config}

echo ''
echo -e 'kubernetes version:\t' $(kubectl version -o json | jq .serverVersion.gitVersion)
echo -e 'wr version:\t' $(git rev-parse --verify HEAD)
echo ''

echo '* Running client tests'
GOCACHE=off go test -v ${SCRIPT_ROOT}/kubernetes/client/...

echo '* Running deployment controller tests'
GOCACHE=off go test -v ${SCRIPT_ROOT}/kubernetes/deployment/...

echo '* Running scheduler controller tests'
GOCACHE=off go test -v ${SCRIPT_ROOT}/kubernetes/scheduler/...

echo -e '- Tests completed successfully!'