#!/bin/bash

set -euo pipefail
source $(dirname $0)/../../utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

tmp_dir="/tmp/test-$TEST_ID"
mkdir -p $tmp_dir

source $(dirname $0)/fnupdate_utils.sh

cleanup() {
    log "Cleaning up..."
    clean_resource_by_id $TEST_ID
    rm -rf $tmp_dir
}

if [ -z "${TEST_NOCLEANUP:-}" ]; then
    trap cleanup EXIT
else
    log "TEST_NOCLEANUP is set; not cleaning up test artifacts afterwards."
fi

# This test tests updating package and checking results of function, it does:
# Creates a archive, env. with builder and a function and tests for response
# Then updates archive with a different word and udpates functions to check for new string in response

env=python-$TEST_ID
fn_name=hellopython-$TEST_ID

log "Creating an archive"
mkdir -p $tmp_dir/test_dir
printf 'def main():\n    return "Hello, world!"' > $tmp_dir/test_dir/hello.py
zip -jr $tmp_dir/test-deploy-pkg.zip $tmp_dir/test_dir/

log "Creating environment"
fission env create --name $env --image $PYTHON_RUNTIME_IMAGE --builder $PYTHON_BUILDER_IMAGE --mincpu 40 --maxcpu 80 --minmemory 64 --maxmemory 128 --poolsize 2

log "Creating functiom"
fission fn create --name $fn_name --env $env --deploy $tmp_dir/test-deploy-pkg.zip --entrypoint "hello.main" --executortype newdeploy --minscale 1 --maxscale 4 --targetcpu 50

log "Creating route"
fission route create --function $fn_name --url /$fn_name --method GET

log "Waiting for router & newdeploy deployment creation"
sleep 5

timeout 60 bash -c "test_fn $fn_name 'world'"

log "Updating the archive"
sed -i 's/world/fission/' $tmp_dir/test_dir/hello.py
zip -jr $tmp_dir/test-deploy-pkg.zip $tmp_dir/test_dir/

log "Updating function with updated package"
fission fn update --name $fn_name --deploy $tmp_dir/test-deploy-pkg.zip --entrypoint "hello.main" --executortype newdeploy --minscale 1 --maxscale 4 --targetcpu 50

log "Waiting for deployment to update"
sleep 5

timeout 60 bash -c "test_fn $fn_name 'fission'"

log "Update function for new deployment executor passed"
