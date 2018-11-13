#!/bin/bash

#### Purpose ####
# The purpose of this script is to enable running some/all of test scripts locally on mac so that 
# you don't have to rely on CI cycle for feedback.
#### Usage ####
# You need to source this script into the test script you wan to run locally:
#       source $(dirname $0)/mac_util.sh
#### Prerequisite ####
# Need to install coreutils for using gtimeout as a replacement for timeout
#### Caution #### 
# Some scripts might use additional variables only available during CI cycle such as an image 
# being built by CI - which you will have to override manually in that script.

if [ $(uname -s) == 'Darwin' ]
then
    # gtimeout needs to be installed separately, do "brew install coreutils".
    timeout() {
        gtimeout "$@"
    }
    export -f timeout

    log() {
        echo "$@"
    }
    export -f log

    export FISSION_ROUTER=$(kubectl -n fission get svc router -o jsonpath='{...ip}')
fi
