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
echo 'echo hello world > /tmp/hw' | /tmp/wr add --rtimeout 250

# Test different can support the runner deployment method
echo 'echo golang:latest' | /tmp/wr add --cloud_os golang:latest --rtimeout 250
echo 'echo genomicpariscentre/samtools' | /tmp/wr add --cloud_os genomicpariscentre/samtools --rtimeout 250

echo '* Running e2e tests'
GOCACHE=off go test -v -timeout 500s ${SCRIPT_ROOT}/kubernetes/e2e/...

# Test kubeTeardownCmd
/tmp/wr kubernetes teardown || (cat ~/.wr_development/kubeScheduler{,Controller}log; /bin/false)

echo -e '- Tests completed successfully!'