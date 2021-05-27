#!/bin/bash
set -x


setup_fission_function() {
    log "==== Setting up objects for upgrade test ===="
    log "Creating env, function and route"
    fission env create --name nodejs --image fission/node-env:latest
    curl -LO https://raw.githubusercontent.com/fission/examples/master/nodejs/hello.js
    
    fission function create --name hello --env nodejs --code hello.js

    if [ $? == 0 ]
      then
      echo "Success, function created successfully !!!"
      else
      echo "Failure, received a non zero response"
      fi

    sleep 2

    echo "Executing function...."
    fission function test --name hello
    
    if [ $? == 0 ]
      then
      echo "Success, function response received !!!"
      else
      echo "Failure, received a non zero response"
      fi
    
}

setup_fission_function