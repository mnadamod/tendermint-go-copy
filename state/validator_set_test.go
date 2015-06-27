package state

import (
	"github.com/tendermint/tendermint/account"
	. "github.com/tendermint/tendermint/common"

	"bytes"
	"fmt"
	"testing"
)

func randValidator_() *Validator {
	return &Validator{
		Address:     RandBytes(20),
		PubKey:      account.PubKeyEd25519(RandBytes(64)),
		BondHeight:  RandInt(),
		VotingPower: RandInt64(),
		Accum:       RandInt64(),
	}
}

func randValidatorSet(numValidators int) *ValidatorSet {
	validators := make([]*Validator, numValidators)
	for i := 0; i < numValidators; i++ {
		validators[i] = randValidator_()
	}
	return NewValidatorSet(validators)
}

func TestCopy(t *testing.T) {
	vset := randValidatorSet(10)
	vsetHash := vset.Hash()
	if len(vsetHash) == 0 {
		t.Fatalf("ValidatorSet had unexpected zero hash")
	}

	vsetCopy := vset.Copy()
	vsetCopyHash := vsetCopy.Hash()

	if !bytes.Equal(vsetHash, vsetCopyHash) {
		t.Fatalf("ValidatorSet copy had wrong hash. Orig: %X, Copy: %X", vsetHash, vsetCopyHash)
	}
}

func TestProposerSelection(t *testing.T) {
	vset := randValidatorSet(10)
	for i := 0; i < 100; i++ {
		val := vset.Proposer()
		fmt.Printf("Proposer: %v\n", val)
		vset.IncrementAccum(1)
	}
}

func BenchmarkValidatorSetCopy(b *testing.B) {
	b.StopTimer()
	vset := NewValidatorSet([]*Validator{})
	for i := 0; i < 1000; i++ {
		privAccount := account.GenPrivAccount()
		val := &Validator{
			Address: privAccount.Address,
			PubKey:  privAccount.PubKey.(account.PubKeyEd25519),
		}
		if !vset.Add(val) {
			panic("Failed to add validator")
		}
	}
	b.StartTimer()

	for i := 0; i < b.N; i++ {
		vset.Copy()
	}
}
