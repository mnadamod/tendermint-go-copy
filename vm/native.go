package vm

import (
	"crypto/sha256"
	"github.com/tendermint/tendermint/Godeps/_workspace/src/code.google.com/p/go.crypto/ripemd160"
	. "github.com/tendermint/tendermint/common"
	"github.com/tendermint/tendermint/vm/secp256k1"
	"github.com/tendermint/tendermint/vm/sha3"
)

var nativeContracts = make(map[Word256]NativeContract)

func init() {
	nativeContracts[Int64ToWord256(1)] = ecrecoverFunc
	nativeContracts[Int64ToWord256(2)] = sha256Func
	nativeContracts[Int64ToWord256(3)] = ripemd160Func
	nativeContracts[Int64ToWord256(4)] = identityFunc
}

//-----------------------------------------------------------------------------

type NativeContract func(input []byte, gas *int64) (output []byte, err error)

func ecrecoverFunc(input []byte, gas *int64) (output []byte, err error) {
	// Deduct gas
	gasRequired := GasEcRecover
	if *gas < gasRequired {
		return nil, ErrInsufficientGas
	} else {
		*gas -= gasRequired
	}
	// Recover
	hash := input[:32]
	v := byte(input[32] - 27) // ignore input[33:64], v is small.
	sig := append(input[64:], v)

	recovered, err := secp256k1.RecoverPubkey(hash, sig)
	if err != nil {
		return nil, err
	}
	hashed := sha3.Sha3(recovered[1:])
	return LeftPadBytes(hashed, 32), nil
}

func sha256Func(input []byte, gas *int64) (output []byte, err error) {
	// Deduct gas
	gasRequired := int64((len(input)+31)/32)*GasSha256Word + GasSha256Base
	if *gas < gasRequired {
		return nil, ErrInsufficientGas
	} else {
		*gas -= gasRequired
	}
	// Hash
	hasher := sha256.New()
	// CONTRACT: this does not err
	_, err = hasher.Write(input)
	if err != nil {
		panic(err)
	}
	return hasher.Sum(nil), nil
}

func ripemd160Func(input []byte, gas *int64) (output []byte, err error) {
	// Deduct gas
	gasRequired := int64((len(input)+31)/32)*GasRipemd160Word + GasRipemd160Base
	if *gas < gasRequired {
		return nil, ErrInsufficientGas
	} else {
		*gas -= gasRequired
	}
	// Hash
	hasher := ripemd160.New()
	// CONTRACT: this does not err
	_, err = hasher.Write(input)
	if err != nil {
		panic(err)
	}
	return LeftPadBytes(hasher.Sum(nil), 32), nil
}

func identityFunc(input []byte, gas *int64) (output []byte, err error) {
	// Deduct gas
	gasRequired := int64((len(input)+31)/32)*GasIdentityWord + GasIdentityBase
	if *gas < gasRequired {
		return nil, ErrInsufficientGas
	} else {
		*gas -= gasRequired
	}
	// Return identity
	return input, nil
}

//-----------------------------------------------------------------------------
// Doug Contracts are stateful and must be set with closures wrapping the current tx cache
// Note they should be reset to refresh the closure or it will be stale

var dougContracts = make(map[Word256]NativeContract)

func (vm *VM) SetDougFunc(n Word256, f NativeContract) {
	dougContracts[n] = f
}
