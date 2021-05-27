#!/bin/bash

flag=0

setup_fission_function() {

    echo "Creating up objects for upgrade test..."
    
    fission env create --name nodejs --image fission/node-env:latest
    curl -LO https://raw.githubusercontent.com/fission/examples/master/nodejs/hello.js
    fission function create --name hello --env nodejs --code hello.js
    if [ $? == 0 ]
      then
      echo "Success, function created successfully !!!"
      else
      echo "Failure, received a non zero response"
      exit
      fi
}

test_fission_function() {

    echo "Testing function...."
    fission function test --name hello
    if [ $? == 0 ]
      then
      echo "Success, function response received !!!"
      else
      echo "Failure, received a non zero response"
      flag=1
      fi
}

setup_fission_function
test_fission_function


    if [ $flag == 0 ]
      then
      echo "Both function creation and testing are successfull. Good to go :-)"
      else
      echo "Oops.. some failure occured :-("
      fi