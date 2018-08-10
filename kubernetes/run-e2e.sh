#!/usr/bin/env bash

set -e

echo -e '- Staring wr Kubernetes End-to-End tests'

# Environment variables.
echo -e '- Setting up the test environment..'

SCRIPT_ROOT=$(dirname ${BASH_SOURCE})/..
export KUBECONFIG=${KUBECONFIG:-$HOME/.kube/config}

echo ''
echo -e 'kubernetes version:\t' $(kubectl version -o json | jq .serverVersion.gitVersion)
echo -e 'wr version:\t' $(git rev-parse --verify HEAD)
echo ''

# Test deployment controller.
echo '- Testing wr kubernetes deployment:'

# Run Go tests.
echo '* Running client tests'
GOCACHE=off go test -v ${SCRIPT_ROOT}/kubernetes/client/...

echo '* Running deployment controller tests'
GOCACHE=off go test -v ${SCRIPT_ROOT}/kubernetes/deployment/...

echo '* Running scheduler controller tests'
GOCACHE=off go test -v ${SCRIPT_ROOT}/kubernetes/scheduler/...


# Test kubeCmd
# Test kubeDeployCmd
/tmp/wr kubernetes deploy --debug || (cat ~/.wr_development/kubelog; /bin/false)

# Test kubeTeardownCmd
/tmp/wr kubernetes teardown || (cat ~/.wr_development/kubeScheduler{,Controller}log; /bin/false)

echo -e '- Tests completed successfully!'