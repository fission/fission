#!/bin/bash

set -euo pipefail
source $(dirname $0)/../utils.sh

TEST_ID=$(generate_test_id)
echo "TEST_ID = $TEST_ID"

tmp_dir="/tmp/test-$TEST_ID"
mkdir -p $tmp_dir

podname=$(kubectl get pods -n fission | grep "storagesvc" |awk '{print $1}')

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

create_archive() {
    log "Creating an archive"
    mkdir -p $tmp_dir/archive
    dd if=/dev/urandom of=$tmp_dir/archive/dynamically_generated_file bs=256k count=1
    printf 'def main():\n    return "Hello, world!"' > $tmp_dir/archive/hello.py
    zip -jr $tmp_dir/test-deploy-pkg.zip $tmp_dir/archive/
}

#Test for upload
create_archive
uploadResp=$(fission ar upload --name $tmp_dir/test-deploy-pkg.zip)
filename=$(echo $uploadResp | cut -d':' -f2 | tr -d ' ')

kubectl exec -i $podname -n fission -- /bin/sh -c "ls $filename"

#Test for list
listResp=$(fission ar list)

echo "$listResp" | grep "$filename"

#Test for download
downloadResp=$(fission ar download --id $filename)
fileID=$(echo $filename | cut -d'/' -f4)

ls | grep "$fileID"

#Test for get-url
getURLResp=$(fission ar get-url --id $filename)

echo $getURLResp | grep "http://storagesvc.fission/v1/archive?id=%2Ffission%2Ffission-functions%2F$fileID"

#Test for delete
fission ar delete --id $filename

fission ar list | grep -v "$filename"

log "Archive CLI tests done."







