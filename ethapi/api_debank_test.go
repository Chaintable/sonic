package ethapi

import (
	"math/big"
	"testing"

	"github.com/0xsoniclabs/sonic/evmcore"
	ptypes "github.com/Chaintable/pipeline/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/stretchr/testify/require"
)

func TestValidateDebankReplayReceiptsRejectsReceiptRootMismatch(t *testing.T) {
	tx := types.NewTx(&types.LegacyTx{Nonce: 1, Gas: 21_000, GasPrice: big.NewInt(1)})
	receipt := &types.Receipt{
		TxHash:            tx.Hash(),
		Status:            types.ReceiptStatusSuccessful,
		GasUsed:           21_000,
		CumulativeGasUsed: 21_000,
	}
	block := &evmcore.EvmBlock{
		EvmHeader: evmcore.EvmHeader{Number: big.NewInt(7)},
		Transactions: types.Transactions{
			tx,
		},
	}

	err := validateDebankReplayReceipts(block, []evmcore.ProcessedTransaction{{
		Transaction: tx,
		Receipt:     receipt,
	}}, common.HexToHash("0x01"), types.Bloom{})

	require.Error(t, err)
	require.Contains(t, err.Error(), "replayed receipt root mismatch")
}

func TestValidateDebankReplayReceiptsRejectsBloomMismatch(t *testing.T) {
	tx := types.NewTx(&types.LegacyTx{Nonce: 1, Gas: 21_000, GasPrice: big.NewInt(1)})
	receipt := &types.Receipt{
		TxHash:            tx.Hash(),
		Status:            types.ReceiptStatusSuccessful,
		GasUsed:           21_000,
		CumulativeGasUsed: 21_000,
		Logs: []*types.Log{{
			Address: common.HexToAddress("0x1001"),
			Topics:  []common.Hash{common.HexToHash("0x01")},
		}},
	}
	receipt.Bloom = types.CreateBloom(receipt)
	block := &evmcore.EvmBlock{
		EvmHeader: evmcore.EvmHeader{Number: big.NewInt(7)},
		Transactions: types.Transactions{
			tx,
		},
	}
	receiptHash := types.DeriveSha(types.Receipts{receipt}, trie.NewStackTrie(nil))

	err := validateDebankReplayReceipts(block, []evmcore.ProcessedTransaction{{
		Transaction: tx,
		Receipt:     receipt,
	}}, receiptHash, types.Bloom{})

	require.Error(t, err)
	require.Contains(t, err.Error(), "replayed logs bloom mismatch")
}

func TestValidateDebankBlockFileTxsRejectsIDMismatch(t *testing.T) {
	tx := types.NewTx(&types.LegacyTx{Nonce: 1, Gas: 21_000, GasPrice: big.NewInt(1)})
	block := &evmcore.EvmBlock{
		EvmHeader: evmcore.EvmHeader{Number: big.NewInt(7)},
		Transactions: types.Transactions{
			tx,
		},
	}

	err := validateDebankBlockFileTxs(block, []ptypes.Transaction{{ID: common.HexToHash("0x01").Hex()}})

	require.Error(t, err)
	require.Contains(t, err.Error(), "block_file tx 0 id mismatch")
}
