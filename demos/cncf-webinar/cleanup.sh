#!/bin/bash

fission fn delete --name hello
fission route delete --name $(fission route list|grep hello|cut -f1 -d' ')
