package state

import (
	"io/ioutil"
	"os"
	"time"

	"github.com/tendermint/tendermint/account"
	"github.com/tendermint/tendermint/binary"
	. "github.com/tendermint/tendermint/common"
	dbm "github.com/tendermint/tendermint/db"
	"github.com/tendermint/tendermint/merkle"
	ptypes "github.com/tendermint/tendermint/permission/types"
	"github.com/tendermint/tendermint/types"
)

type GenesisAccount struct {
	Address     []byte              `json:"address"`
	Amount      uint64              `json:"amount"`
	Permissions *ptypes.Permissions `json:"global_permissions"` // pointer so optional
}

type GenesisValidator struct {
	PubKey   account.PubKeyEd25519 `json:"pub_key"`
	Amount   int64                 `json:"amount"`
	UnbondTo []GenesisAccount      `json:"unbond_to"`
}

type GenesisParams struct {
	// Default permissions for newly created accounts
	GlobalPermissions *ptypes.Permissions `json:"global_permissions"`

	// TODO: other params we may want to tweak?
}

type GenesisDoc struct {
	GenesisTime time.Time          `json:"genesis_time"`
	ChainID     string             `json:"chain_id"`
	Params      *GenesisParams     `json:"params"` // pointer so optional
	Accounts    []GenesisAccount   `json:"accounts"`
	Validators  []GenesisValidator `json:"validators"`
}

func GenesisDocFromJSON(jsonBlob []byte) (genState *GenesisDoc) {
	var err error
	binary.ReadJSON(&genState, jsonBlob, &err)
	if err != nil {
		log.Error(Fmt("Couldn't read GenesisDoc: %v", err))
		os.Exit(1)
	}
	return
}

func MakeGenesisStateFromFile(db dbm.DB, genDocFile string) *State {
	jsonBlob, err := ioutil.ReadFile(genDocFile)
	if err != nil {
		log.Error(Fmt("Couldn't read GenesisDoc file: %v", err))
		os.Exit(1)
	}
	genDoc := GenesisDocFromJSON(jsonBlob)
	return MakeGenesisState(db, genDoc)
}

func MakeGenesisState(db dbm.DB, genDoc *GenesisDoc) *State {
	if len(genDoc.Validators) == 0 {
		Exit(Fmt("The genesis file has no validators"))
	}

	if genDoc.GenesisTime.IsZero() {
		genDoc.GenesisTime = time.Now()
	}

	// Make accounts state tree
	accounts := merkle.NewIAVLTree(binary.BasicCodec, account.AccountCodec, defaultAccountsCacheCapacity, db)
	for _, genAcc := range genDoc.Accounts {
		perm_ := account.DefaultPermissions
		perm := &perm_
		if genAcc.Permissions != nil {
			perm = genAcc.Permissions
		}
		acc := &account.Account{
			Address:     genAcc.Address,
			PubKey:      nil,
			Sequence:    0,
			Balance:     genAcc.Amount,
			Permissions: perm,
		}
		accounts.Set(acc.Address, acc)
	}

	// global permissions are saved as the 0 address
	// so they are included in the accounts tree
	globalPerms_ := account.DefaultPermissions
	globalPerms := &globalPerms_
	if genDoc.Params != nil && genDoc.Params.GlobalPermissions != nil {
		globalPerms = genDoc.Params.GlobalPermissions
	}
	permsAcc := &account.Account{
		Address:     ptypes.GlobalPermissionsAddress,
		PubKey:      nil,
		Sequence:    0,
		Balance:     1337,
		Permissions: globalPerms,
	}
	accounts.Set(permsAcc.Address, permsAcc)

	// Make validatorInfos state tree && validators slice
	validatorInfos := merkle.NewIAVLTree(binary.BasicCodec, ValidatorInfoCodec, 0, db)
	validators := make([]*Validator, len(genDoc.Validators))
	for i, val := range genDoc.Validators {
		pubKey := val.PubKey
		address := pubKey.Address()

		// Make ValidatorInfo
		valInfo := &ValidatorInfo{
			Address:         address,
			PubKey:          pubKey,
			UnbondTo:        make([]*types.TxOutput, len(val.UnbondTo)),
			FirstBondHeight: 0,
			FirstBondAmount: val.Amount,
		}
		for i, unbondTo := range val.UnbondTo {
			valInfo.UnbondTo[i] = &types.TxOutput{
				Address: unbondTo.Address,
				Amount:  unbondTo.Amount,
			}
		}
		validatorInfos.Set(address, valInfo)

		// Make validator
		validators[i] = &Validator{
			Address:     address,
			PubKey:      pubKey,
			VotingPower: val.Amount,
		}
	}

	// Make namereg tree
	nameReg := merkle.NewIAVLTree(binary.BasicCodec, NameRegCodec, 0, db)
	// TODO: add names to genesis.json

	// IAVLTrees must be persisted before copy operations.
	accounts.Save()
	validatorInfos.Save()
	nameReg.Save()

	return &State{
		DB:                   db,
		ChainID:              genDoc.ChainID,
		LastBlockHeight:      0,
		LastBlockHash:        nil,
		LastBlockParts:       types.PartSetHeader{},
		LastBlockTime:        genDoc.GenesisTime,
		BondedValidators:     NewValidatorSet(validators),
		LastBondedValidators: NewValidatorSet(nil),
		UnbondingValidators:  NewValidatorSet(nil),
		accounts:             accounts,
		validatorInfos:       validatorInfos,
		nameReg:              nameReg,
	}
}
