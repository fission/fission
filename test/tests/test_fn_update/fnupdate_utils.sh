#!/bin/bash

#
# Common methods used for testing function update
#

set -euo pipefail

test_fn() {
    echo "Doing an HTTP GET on the function's route"
    echo "Checking for valid response"

    while true; do
      response0=$(curl http://$FISSION_ROUTER/$1)
      echo $response0 | grep -i $2
      if [[ $? -eq 0 ]]; then
        break
      fi
      sleep 1
    done
}
export -f test_fn
