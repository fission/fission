#!/bin/bash

fission spec destroy

# this one is generated in run.sh
rm specs/function-hello-go.yaml

rm *.~?~

