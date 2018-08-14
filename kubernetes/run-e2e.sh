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

# For more granularty test the 3 main parts work in isolation.

# Test deployment controller.
echo '- Testing wr kubernetes deployment:'

# Run Go tests.
echo '* Running client tests'
GOCACHE=off go test -v ${SCRIPT_ROOT}/kubernetes/client/...

echo '* Running deployment controller tests'
GOCACHE=off go test -v ${SCRIPT_ROOT}/kubernetes/deployment/...

echo '* Running scheduler controller tests'
GOCACHE=off go test -v ${SCRIPT_ROOT}/kubernetes/scheduler/...

# Test the compiled binary as the end user would run it. 

# Test kubeDeployCmd
/tmp/wr kubernetes deploy --debug || (cat ~/.wr_development/kubelog; /bin/false)

# Test we can reach the manager
[ $(/tmp/wr manager status)  == started ] || (echo "unable to talk to manager"; /bin/false)

# Test 4 + simple commands execute without fail 
echo 'apt-get update && apt-get upgrade -y && apt-get install -y curl' > /tmp/curl.sh
echo {42,24,mice,test} | xargs -n 1  echo echo | /tmp/wr add

# Test we can run configmaps and create files
# rtimeout instructs the runner pod to stay alive for long enough to verify the file.
echo 'curl http://ovh.net/files/1Mio.dat -o /tmp/1Mio.dat' | /tmp/wr add --cloud_script /tmp/curl.sh --rtimeout 10

# Test different can support the runner deployment method
echo 'echo golang:latest' | /tmp/wr add --cloud_os golang:latest --rtimeout 100
echo 'echo genomicpariscentre/samtools' | /tmp/wr add --cloud_os genomicpariscentre/samtools --rtimeout 100





echo '* Running e2e tests'
GOCACHE=off go test -v -timeout 500s ${SCRIPT_ROOT}/kubernetes/e2e/...

# Test kubeTeardownCmd
/tmp/wr kubernetes teardown || (cat ~/.wr_development/kubeScheduler{,Controller}log; /bin/false)

echo -e '- Tests completed successfully!'