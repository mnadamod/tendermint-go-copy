#! /bin/bash
set -eu

DATACENTER=single
VALSETSIZE=4
BLOCKSIZE=8092
TX_SIZE=200
NTXS=$((BLOCKSIZE*4))
MACH_PREFIX=mach
RESULTSDIR=results
CLOUD_PROVIDER=digitalocean

export TMHEAD=`git rev-parse --abbrev-ref HEAD`
export TM_IMAGE="tendermint/tmbase"

# not a go repo
set +e
go get github.com/tendermint/network_testing
set -e
cd $GOPATH/src/github.com/tendermint/network_testing
bash experiments/exp_throughput.sh $DATACENTER $VALSETSIZE $BLOCKSIZE $TX_SIZE $NTXS $MACH_PREFIX $RESULTSDIR $CLOUD_PROVIDER

# TODO echo result!
