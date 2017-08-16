#!/bin/bash

kubectl delete ns guestbook

fission fn delete --name guestbook-add
fission fn delete --name guestbook-get

fission route list|grep guestbook|cut -f1 -d' '|xargs -n1 fission route delete --name
