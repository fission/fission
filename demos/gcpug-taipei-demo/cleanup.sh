#!/bin/bash

fission env delete --name nodejs
fission fn delete --name hello
fission fn delete --name func-v1
fission fn delete --name func-v2
fission route delete --name $(fission route list|grep -v NAME|cut -f1 -d' ')
fission canary-config delete --name canary-1
