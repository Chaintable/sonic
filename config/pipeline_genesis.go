package config

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"math/big"
)

var GenesisHeader = types.Header{
	ParentHash:      common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000"),
	UncleHash:       types.EmptyUncleHash,
	Root:            common.HexToHash("0xa1953bba1bec04a28376140593c2ede9df88d71b13d0f1dbc0fc542b143a9dd9"),
	TxHash:          common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
	ReceiptHash:     common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
	Number:          big.NewInt(0),
	Time:            1733011200,
	GasLimit:        1000000000,
	GasUsed:         0,
	BaseFee:         big.NewInt(10000000000),
	MixDigest:       common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001"),
	Extra:           []byte("0x000000000000000000000000"),
	Coinbase:        common.HexToAddress("0x0000000000000000000000000000000000000000"),
	Difficulty:      big.NewInt(0),
	WithdrawalsHash: &types.EmptyWithdrawalsHash,
	BlobGasUsed:     new(uint64),
	ExcessBlobGas:   new(uint64),
}

var Alloc types.GenesisAlloc

func init() {
	var genesisBloom = types.Bloom{}
	err := genesisBloom.UnmarshalText([]byte("0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"))
	if err != nil {
		panic(err)
	}
	GenesisHeader.Bloom = genesisBloom

	var nonce types.BlockNonce
	err = nonce.UnmarshalText([]byte("0x0000000000000000"))
	if err != nil {
		panic(err)
	}
	GenesisHeader.Nonce = nonce
}
