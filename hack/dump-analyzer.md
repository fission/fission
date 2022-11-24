# Dump analyzer

## Usage

```text
./hack/dump-analyzer -h
showing usage!
./dump-analyzer [OPTIONS]
Utilities related to fission dump analysis

Options:
    -h              Show usage
    -x [run_id]     Download and extract dump locally
    -l [run_id]    List dump files for a run
    -k [run_id]    List kind exports in a run

    Following options required DUMP_CONTEXT variable set.

    -i              Display dump info
    -r              Find all race conditions in dump
    -e              Find all errors in logs

    Following options require PROM_CONTEXT to be set.

    -p             Run prometheus with the dump

```

## Downloading dump

```sh
$ ./hack/dump-analyzer -x 3179903728
! mkdir -p .dumps/3179903728
.dumps/3179903728
! gh run download 3179903728 -R fission/fission -D .dumps/3179903728
! unzip -q .dumps/3179903728/fission-dump-3179903728-v1.19.16/fission-dump_1664866624.zip -d .dumps/3179903728/extract
! unzip -q .dumps/3179903728/fission-dump-3179903728-v1.21.12/fission-dump_1664866628.zip -d .dumps/3179903728/extract
! unzip -q .dumps/3179903728/fission-dump-3179903728-v1.20.15/fission-dump_1664866662.zip -d .dumps/3179903728/extract
```

## Check all dumps

```sh
./hack/dump-analyzer -l 3179903728
==== .dumps/3179903728/extract/4b24c3db-b5d8-43a7-858d-48746530d29e2534094304 ====
-- Fission Version --
client:
    Version: v0.0.0
server:
    Version: v0.0.0
-- K8s Version --
gitVersion: v1.21.12
==== .dumps/3179903728/extract/e7febf06-79ce-4eb7-bba0-3fc66aa4bf542096519315 ====
-- Fission Version --
client:
    Version: v0.0.0
server:
    Version: v0.0.0
-- K8s Version --
gitVersion: v1.19.16
==== .dumps/3179903728/extract/3b339dc2-e9dd-4f7a-a721-41e16d9d248f4152710428 ====
-- Fission Version --
client:
    Version: v0.0.0
server:
    Version: v0.0.0
-- K8s Version --
gitVersion: v1.20.15
```

We can see that the dump is from 3 different versions of k8s.
To explore specific dump set `DUMP_CONTEXT` environment variable.

```sh
$ export DUMP_CONTEXT=.dumps/3179903728/extract/4b24c3db-b5d8-43a7-858d-48746530d29e2534094304

$ ./hack/dump-analyzer -i
Dump context: .dumps/3179903728/extract/4b24c3db-b5d8-43a7-858d-48746530d29e2534094304
! cat .dumps/3179903728/extract/4b24c3db-b5d8-43a7-858d-48746530d29e2534094304/fission-version/fission-version.txt
client:
  fission/core:
    BuildDate: "2022-10-04T06:28:54Z"
    GitCommit: 9e74b01
    Version: v0.0.0
server:
  fission/core:
    BuildDate: "2022-10-04T06:29:01Z"
    GitCommit: 9e74b01
    Version: v0.0.0
! cat .dumps/3179903728/extract/4b24c3db-b5d8-43a7-858d-48746530d29e2534094304/kubernetes-version/kubernetes-version.txt
buildDate: "2022-05-19T20:02:29Z"
compiler: gc
gitCommit: 696a9fdd2a58340e61e0d815c5769d266fca0802
gitTreeState: clean
gitVersion: v1.21.12
goVersion: go1.16.15
major: "1"
minor: "21"
platform: linux/amd64

# See all errors in the dump
$ ./hack/dump-analyzer -e

# If you want to see error in specific dump you can also grep

$ grep -rin "string" $DUMP_CONTEXT

# OR

$ ack "string" $DUMP_CONTEXT
```

## Kind logs

```sh
 ./hack/dump-analyzer -k 3179903728
==== .dumps/3179903728/kind-logs-3179903728-v1.20.15 ====
kind v0.14.0 go1.18.2 linux/amd64

==== .dumps/3179903728/kind-logs-3179903728-v1.19.16 ====
kind v0.14.0 go1.18.2 linux/amd64

==== .dumps/3179903728/kind-logs-3179903728-v1.21.12 ====
kind v0.14.0 go1.18.2 linux/amd64
```

## Prometheus with dump

```sh
$ export DUMP_CONTEXT=.dumps/3539792658/prom-dump-3539792658-v1.21.14
$ ./hack/dump-analyzer -p
# Visit localhost:9090
```
