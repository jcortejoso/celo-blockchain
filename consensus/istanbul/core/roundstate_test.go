package core

import (
	"math/big"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/istanbul"
	"github.com/ethereum/go-ethereum/consensus/istanbul/validator"
	"github.com/ethereum/go-ethereum/rlp"
)

func TestRLPEncoding(t *testing.T) {

	valSet := validator.NewSet([]istanbul.ValidatorData{
		istanbul.ValidatorData{Address: common.BytesToAddress([]byte(string(2))), BLSPublicKey: []byte{1, 2, 3}},
		istanbul.ValidatorData{Address: common.BytesToAddress([]byte(string(4))), BLSPublicKey: []byte{3, 1, 4}},
	})
	view := &istanbul.View{Round: big.NewInt(1), Sequence: big.NewInt(2)}
	rs := newRoundState(view, valSet, valSet.GetByIndex(0))

	rawVal, err := rlp.EncodeToBytes(rs)
	if err != nil {
		t.Errorf("Error %v", err)
	}

	var result *roundStateImpl
	if err = rlp.DecodeBytes(rawVal, &result); err != nil {
		t.Errorf("Error %v", err)
	}

	if !reflect.DeepEqual(rs, result) {
		t.Errorf("RoundState mismatch: have %v, want %v", rs, result)
	}
}

func TestMessageSetRLPEncoding(t *testing.T) {

	valSet := validator.NewSet([]istanbul.ValidatorData{
		istanbul.ValidatorData{Address: common.BytesToAddress([]byte(string(2))), BLSPublicKey: []byte{1, 2, 3}},
	})

	ms := newMessageSet(valSet)
	ms.Add(&istanbul.Message{
		Address:   common.BytesToAddress([]byte(string(2))),
		Code:      1,
		Msg:       []byte{12, 4},
		Signature: []byte{12, 4},
	})

	raw, err := rlp.EncodeToBytes(ms)
	if err != nil {
		t.Errorf("Error %v", err)
	}

	var result *messageSetImpl
	if err = rlp.DecodeBytes(raw, &result); err != nil {
		t.Errorf("Error %v", err)
	}

	if !reflect.DeepEqual(ms, result) {
		t.Errorf("MessageSet mismatch: have %v, want %v", ms, result)
	}
}
