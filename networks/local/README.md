# Local Docker Testnet

## Requirements

- [Install docker](https://docs.docker.com/engine/installation/)
- [Install docker-compose](https://docs.docker.com/compose/install/)

## Build

Build the `tendermint` binary and the `tendermint/localnode` docker image:

```
cd $GOPATH/src/github.com/tendermint/tendermint

# Install dependencies (skip if already done)
make get_tools
make get_vendor_deps

# Build binary in ./build
make build-linux

# Build tendermint/localnode image
make build-docker-localnode
```

## Run a testnet

To start a 4 node testnet run:

```
make localnet-start
```

The nodes bind their RPC servers to ports 46657, 46660, 46662, and 46664 on the host.
This file creates a 4-node network using the localnode image.
The nodes of the network expose their P2P and RPC endpoints to the host machine on ports 46656-46657, 46659-46660, 46661-46662, and 46663-46664 respectively.

To update the binary, just rebuild it and restart the nodes:

```
make build-linux
make localnet-stop
make localnet-start
```

## Configuration

The `make localnet-start` creates files for a 4-node testnet in `./build` by calling the `tendermint testnet` command.

The `./build` directory is mounted to the `/tendermint` mount point to attach the binary and config files to the container.

For instance, to create a single node testnet:

```
cd $GOPATH/src/github.com/tendermint/tendermint

# Clear the build folder
rm -rf ./build

# Build binary
make build-linux

# Create configuration
docker run -e LOG="stdout" -v `pwd`/build:/tendermint tendermint/localnode testnet --o . --v 1

#Run the node
docker run -v `pwd`/build:/tendermint tendermint/localnode

```

## Logging

Log is saved under the attached volume, in the `tendermint.log` file. If the `LOG` environment variable is set to `stdout` at start, the log is not saved, but printed on the screen.

## Special binaries

If you have multiple binaries with different names, you can specify which one to run with the BINARY environment variable. The path of the binary is relative to the attached volume.

