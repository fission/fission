#!/bin/sh

set -e

kubectl create -f redis.yaml

if [ -z "$FISSION_URL" ]
then
    echo "Need $FISSION_URL set to a fission controller address"
    exit 1
fi

fission env create --name python --image fission/python-env
fission function create --name guestbook-get --env python --code get.py --url /guestbook --method GET
fission function create --name guestbook-add --env python --code add.py --url /guestbook --method POST
