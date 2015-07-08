#!/bin/bash
set -euo pipefail
IFS=$'\n\t'

debora open "[::]:46661"
debora --group default.upgrade status
printf "\n\nWill shut down barak default port..."
sleep 3
debora --group default.upgrade close "[::]:46660"
debora --group default.upgrade run -- bash -c "cd \$GOPATH/src/github.com/tendermint/tendermint; git pull origin develop; make"
debora --group default.upgrade run -- bash -c "cd \$GOPATH/src/github.com/tendermint/tendermint; mkdir -p ~/.barak/logs"
debora --group default.upgrade run --bg --label barak -- bash -c "cd \$GOPATH/src/github.com/tendermint/tendermint; barak --config=cmd/barak/seed | stdinwriter -outpath ~/.barak/logs/barak.log"
printf "\n\nTesting new barak..."
debora status
sleep 3
printf "\n\nWill shut down old barak..."
debora --group default.upgrade quit
printf "Done!"
