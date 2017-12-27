package evidence

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tendermint/tendermint/types"
	dbm "github.com/tendermint/tmlibs/db"
)

type mockState struct{}

func (m mockState) VerifyEvidence(ev types.Evidence) (int64, error) {
	err := ev.Verify("")
	return 10, err
}

func TestEvidencePool(t *testing.T) {
	assert := assert.New(t)

	params := types.EvidenceParams{}
	store := NewEvidenceStore(dbm.NewMemDB())
	state := mockState{}
	pool := NewEvidencePool(params, store, state)

	goodEvidence := newMockGoodEvidence(5, 1, []byte("val1"))
	badEvidence := MockBadEvidence{goodEvidence}

	err := pool.AddEvidence(badEvidence)
	assert.NotNil(err)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		<-pool.EvidenceChan()
		wg.Done()
	}()

	err = pool.AddEvidence(goodEvidence)
	assert.Nil(err)
	wg.Wait()

	// if we send it again it wont fire on the chan
	err = pool.AddEvidence(goodEvidence)
	assert.Nil(err)
	select {
	case <-pool.EvidenceChan():
		t.Fatal("unexpected read on EvidenceChan")
	default:
	}
}
