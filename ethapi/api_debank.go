package ethapi

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/0xsoniclabs/sonic/evmcore"
	"github.com/0xsoniclabs/sonic/gossip/evmstore"
	"github.com/0xsoniclabs/sonic/inter"
	"github.com/0xsoniclabs/sonic/inter/state"
	"github.com/0xsoniclabs/sonic/opera"
	ptracer "github.com/Chaintable/pipeline/tracer"
	ptypes "github.com/Chaintable/pipeline/types"
	"github.com/Chaintable/pipeline/util"
	"github.com/Fantom-foundation/lachesis-base/inter/idx"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/trie"
)

type DebankAPI struct {
	b Backend
}

func NewDebankAPI(b Backend) *DebankAPI {
	return &DebankAPI{
		b: b,
	}
}

type debankHeaderReader struct {
	ctx context.Context
	b   Backend
}

func (r debankHeaderReader) GetHeader(hash common.Hash, number uint64) *evmcore.EvmHeader {
	if hash != (common.Hash{}) {
		header, err := r.b.HeaderByHash(r.ctx, hash)
		if err == nil && header != nil {
			return header
		}
	}

	header, err := r.b.HeaderByNumber(r.ctx, rpc.BlockNumber(number))
	if err != nil {
		log.Warn("Failed to read header for Debank replay", "number", number, "hash", hash, "err", err)
		return nil
	}
	if hash != (common.Hash{}) && header != nil && header.Hash != hash {
		return nil
	}
	return header
}

func (api *DebankAPI) getBlockReceipts(ctx context.Context, blkNumber rpc.BlockNumber) (types.Receipts, error) {
	if blkNumber == rpc.EarliestBlockNumber {
		return types.Receipts{}, nil
	}
	return api.b.GetReceiptsByNumber(ctx, blkNumber)
}

func (api *DebankAPI) DebankBlock(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (*ptypes.DebankOutPut, error) {
	log.Info("trace DebankBlock number", "number", blockNrOrHash)
	var block *evmcore.EvmBlock
	var err error
	if blockNrOrHash.BlockHash != nil {
		block, err = api.b.BlockByHash(ctx, *blockNrOrHash.BlockHash)
	} else if blockNrOrHash.BlockNumber != nil {
		block, err = api.b.BlockByNumber(ctx, *blockNrOrHash.BlockNumber)
	} else {
		log.Error("Either block number or block hash must be provided")
		return nil, fmt.Errorf("either block number or block hash must be provided")
	}

	if err != nil {
		log.Error("Failed to get block", "err", err, "blockNrOrHash", blockNrOrHash)
		return nil, err
	}
	if block == nil {
		return nil, fmt.Errorf("block %v not found", blockNrOrHash)
	}

	receipts, err := api.getBlockReceipts(ctx, rpc.BlockNumber(block.NumberU64()))
	if err != nil {
		return nil, err
	}
	fullBlock, err := RPCMarshalBlock(block, receipts, true, true, api.b.ChainID())
	if err != nil {
		return nil, err
	}

	evmBlockHeader := &types.Header{
		ParentHash:  fullBlock.ParentHash,
		UncleHash:   types.EmptyUncleHash,
		Coinbase:    common.Address{}, // < in Sonic, the coinbase is always 0
		Root:        fullBlock.Root,
		TxHash:      fullBlock.TxHash,
		ReceiptHash: fullBlock.ReceiptHash,
		Bloom:       fullBlock.Bloom,
		Difficulty:  fullBlock.Difficulty.ToInt(),
		Number:      fullBlock.Number.ToInt(),
		GasLimit:    uint64(fullBlock.GasLimit),
		GasUsed:     uint64(fullBlock.GasUsed),
		Time:        uint64(fullBlock.Time),
		Extra: inter.EncodeExtraData(
			block.Time.Time(),
			block.Duration*time.Nanosecond,
		),
		MixDigest: fullBlock.PrevRandao,
		Nonce:     types.BlockNonce{}, // constant 0 in Ethereum
		BaseFee:   fullBlock.BaseFee.ToInt(),

		// Sonic does not have a beacon chain and no withdrawals.
		WithdrawalsHash: &types.EmptyWithdrawalsHash,

		// Sonic does not support blobs, so no blob gas is used and there is
		// no excess blob gas.
		BlobGasUsed:   new(uint64), // = 0
		ExcessBlobGas: new(uint64), // = 0
	}
	evmBlock := block.EthBlock()
	evmBlock = types.NewBlockForPipelineTrace(evmBlockHeader, &types.Body{Transactions: evmBlock.Transactions()}, nil, trie.NewStackTrie(nil))

	if block.NumberU64() == 0 {
		header := util.BuildPilelineBlockHeader(evmBlock)
		blockDiff := ptracer.GenesisAllocToStateDiff(evmcore.GenesisAlloc)
		blockDiff.Hash = evmBlockHeader.Root
		blockDiff.ParentHash = types.EmptyRootHash
		blockFile := &ptypes.BlockFile{
			Block:            util.BuildPipelineBlock(evmBlock),
			Txs:              make([]ptypes.Transaction, 0),
			Events:           make([]ptypes.Event, 0),
			Traces:           make([]ptypes.Trace, 0),
			ErrorEvents:      make([]ptypes.Event, 0),
			ErrorTraces:      make([]ptypes.Trace, 0),
			StorageContracts: make([]string, 0),
		}
		for addr, account := range evmcore.GenesisAlloc {
			if len(account.Storage) > 0 {
				blockFile.StorageContracts = append(blockFile.StorageContracts, strings.ToLower(addr.Hex()))
			}
		}
		var stateDiffBytes []byte
		stateDiffBytes, err = util.EncodeToRlp(blockDiff)
		if err != nil {
			log.Error("Failed to encode state diff", "err", err)
			stateDiffBytes = []byte{}
		}

		return &ptypes.DebankOutPut{
			BlockFile:      blockFile,
			Header:         header,
			StateDiff:      stateDiffBytes,
			ValidationHash: blockFile.Validation().ValidationHash,
		}, nil
	}
	// Prepare base state
	parent, err := api.b.BlockByNumber(ctx, rpc.BlockNumber(block.NumberU64()-1))
	if err != nil {
		return nil, err
	}
	if parent == nil {
		return nil, fmt.Errorf("parent block %d not found", block.NumberU64()-1)
	}

	pStateDB, _, err := api.b.StateAndHeaderByNumberOrHash(ctx, rpc.BlockNumberOrHashWithNumber(rpc.BlockNumber(parent.NumberU64())))
	if err != nil {
		return nil, err
	}
	if pStateDB == nil {
		return nil, fmt.Errorf("state for parent block %d not found", parent.NumberU64())
	}
	defer pStateDB.Release()

	rules, err := api.b.GetNetworkRules(ctx, idx.Block(block.NumberU64()))
	if err != nil {
		return nil, fmt.Errorf("failed to get network rules: %w", err)
	}
	if rules == nil {
		return nil, fmt.Errorf("no network rules found for block height %d", block.NumberU64())
	}
	vmConfig := opera.GetVmConfig(*rules)
	vmConfig.NoBaseFee = true

	rpcTracer := ptracer.RPCTracer{}
	vmConfig.Tracer = newDebankTraceHooks(&rpcTracer)

	diffStateDB := newDebankStateDiffDB(pStateDB)
	pStateDB = evmstore.WrapStateDbWithLogger(diffStateDB, vmConfig.Tracer)
	pStateDB.BeginBlock(block.NumberU64())

	blockIdx := idx.Block(block.NumberU64())
	chainConfig := api.b.ChainConfig(blockIdx)
	rpcTracer.OnBlockStart(evmBlock, chainConfig)

	replayRules := *rules
	replayRules.Upgrades.GasSubsidies = false
	replayHeader := block.EvmHeader
	replayBlock := &evmcore.EvmBlock{
		EvmHeader:    replayHeader,
		Transactions: block.Transactions,
	}
	var usedGas uint64
	processed := evmcore.NewStateProcessor(
		chainConfig,
		debankHeaderReader{ctx: ctx, b: api.b},
		replayRules.Upgrades,
	).Process(replayBlock, pStateDB, vmConfig, block.GasLimit, &usedGas, nil)
	if err := validateDebankReplayReceipts(block, processed, evmBlockHeader.ReceiptHash, evmBlockHeader.Bloom); err != nil {
		return nil, err
	}

	pStateDB.EndBlock(block.NumberU64())
	replayedRoot := pStateDB.GetStateHash()
	if replayedRoot != block.Root {
		// RPC archive states are non-committable: replay can execute transactions,
		// but EndBlock cannot apply the block update, so the hash stays at parent.
		if replayedRoot != parent.Root || parent.Root == block.Root || len(diffStateDB.changes) == 0 {
			return nil, fmt.Errorf("replayed state root mismatch for block %d: got %s, want %s (txs=%d processed=%d usedGas=%d blockGasUsed=%d)", block.NumberU64(), replayedRoot, block.Root, len(block.Transactions), len(processed), usedGas, block.GasUsed)
		}
		if err := api.validateDebankReplayPostState(ctx, block, diffStateDB); err != nil {
			return nil, fmt.Errorf("replayed state root mismatch for block %d: got %s, want %s (txs=%d processed=%d usedGas=%d blockGasUsed=%d); post-state validation failed: %w", block.NumberU64(), replayedRoot, block.Root, len(block.Transactions), len(processed), usedGas, block.GasUsed, err)
		}
		log.Debug("Validated Debank replay against canonical post-state because archive StateDB cannot commit root", "block", block.NumberU64(), "replayedRoot", replayedRoot, "blockRoot", block.Root)
	}
	if usedGas != block.GasUsed {
		return nil, fmt.Errorf("replayed gas used mismatch for block %d: got %d, want %d (txs=%d processed=%d)", block.NumberU64(), usedGas, block.GasUsed, len(block.Transactions), len(processed))
	}

	res := rpcTracer.GetOutPut(parent.Root, parent.Root)
	normalizeDebankBlockFileForRPC(block, receipts, res.BlockFile)
	if err := validateDebankBlockFileTxs(block, res.BlockFile.Txs); err != nil {
		return nil, err
	}
	if parent.Root != block.Root {
		blockDiff := diffStateDB.BuildStateDiff(parent.Root, block.Root)
		stateDiffBytes, err := util.EncodeToRlp(blockDiff)
		if err != nil {
			log.Error("Failed to encode state diff", "err", err)
			stateDiffBytes = []byte{}
		}
		res.StateDiff = stateDiffBytes
	}
	sort.Strings(res.BlockFile.StorageContracts)
	res.ValidationHash = res.BlockFile.Validation().ValidationHash

	return res, nil
}

func (api *DebankAPI) validateDebankReplayPostState(ctx context.Context, block *evmcore.EvmBlock, replayState *debankStateDiffDB) error {
	postState, _, err := api.b.StateAndHeaderByNumberOrHash(ctx, rpc.BlockNumberOrHashWithNumber(rpc.BlockNumber(block.NumberU64())))
	if err != nil {
		return err
	}
	if postState == nil {
		return fmt.Errorf("post-state for block %d not found", block.NumberU64())
	}
	defer postState.Release()
	return validateDebankReplayPostStateValues(block.NumberU64(), replayState, postState)
}

func validateDebankReplayPostStateValues(blockNumber uint64, replayState *debankStateDiffDB, postState state.StateDB) error {
	addrs := make([]common.Address, 0, len(replayState.changes))
	for addr := range replayState.changes {
		addrs = append(addrs, addr)
	}
	sort.Slice(addrs, func(i, j int) bool {
		return strings.Compare(addrs[i].Hex(), addrs[j].Hex()) < 0
	})

	for _, addr := range addrs {
		change := replayState.changes[addr]
		if got, want := replayState.Exist(addr), postState.Exist(addr); got != want {
			return fmt.Errorf("account existence mismatch for block %d addr %s: got %t, want %t", blockNumber, addr, got, want)
		}
		if got, want := replayState.GetNonce(addr), postState.GetNonce(addr); got != want {
			return fmt.Errorf("account nonce mismatch for block %d addr %s: got %d, want %d", blockNumber, addr, got, want)
		}
		if got, want := replayState.GetCodeHash(addr), postState.GetCodeHash(addr); got != want {
			return fmt.Errorf("account code hash mismatch for block %d addr %s: got %s, want %s", blockNumber, addr, got, want)
		}
		if got, want := replayState.GetBalance(addr), postState.GetBalance(addr); got.Cmp(want) != 0 {
			return fmt.Errorf("account balance mismatch for block %d addr %s: got %s, want %s", blockNumber, addr, got, want)
		}
		if change.codeTouched && !bytes.Equal(replayState.GetCode(addr), postState.GetCode(addr)) {
			return fmt.Errorf("account code mismatch for block %d addr %s", blockNumber, addr)
		}
		for key := range change.storage {
			if got, want := replayState.GetState(addr, key), postState.GetState(addr, key); got != want {
				return fmt.Errorf("storage mismatch for block %d addr %s slot %s: got %s, want %s", blockNumber, addr, key, got, want)
			}
		}
	}
	return nil
}

func validateDebankReplayReceipts(block *evmcore.EvmBlock, processed []evmcore.ProcessedTransaction, receiptHash common.Hash, bloom types.Bloom) error {
	if len(processed) != len(block.Transactions) {
		return fmt.Errorf("replayed tx count mismatch for block %d: got %d, want %d", block.NumberU64(), len(processed), len(block.Transactions))
	}
	replayReceipts := make(types.Receipts, 0, len(processed))
	for i, processedTx := range processed {
		if processedTx.Transaction == nil {
			return fmt.Errorf("replayed tx %d in block %d has nil transaction", i, block.NumberU64())
		}
		if processedTx.Transaction.Hash() != block.Transactions[i].Hash() {
			return fmt.Errorf("replayed tx %d mismatch for block %d: got %s, want %s", i, block.NumberU64(), processedTx.Transaction.Hash(), block.Transactions[i].Hash())
		}
		if processedTx.Receipt == nil {
			return fmt.Errorf("could not replay tx %d [%v] in block %d", i, processedTx.Transaction.Hash().Hex(), block.NumberU64())
		}
		replayReceipts = append(replayReceipts, processedTx.Receipt)
	}
	replayedReceiptHash := types.DeriveSha(replayReceipts, trie.NewStackTrie(nil))
	if replayedReceiptHash != receiptHash {
		return fmt.Errorf("replayed receipt root mismatch for block %d: got %s, want %s (txs=%d)", block.NumberU64(), replayedReceiptHash, receiptHash, len(block.Transactions))
	}
	replayedBloom := types.MergeBloom(replayReceipts)
	if replayedBloom != bloom {
		return fmt.Errorf("replayed logs bloom mismatch for block %d", block.NumberU64())
	}
	return nil
}

func validateDebankBlockFileTxs(block *evmcore.EvmBlock, txs []ptypes.Transaction) error {
	if len(txs) != len(block.Transactions) {
		return fmt.Errorf("block_file tx count mismatch for block %d: got %d, want %d", block.NumberU64(), len(txs), len(block.Transactions))
	}
	for i, tx := range block.Transactions {
		if txs[i].ID != tx.Hash().Hex() {
			return fmt.Errorf("block_file tx %d id mismatch for block %d: got %s, want %s", i, block.NumberU64(), txs[i].ID, tx.Hash().Hex())
		}
	}
	return nil
}

func normalizeDebankBlockFileForRPC(block *evmcore.EvmBlock, receipts types.Receipts, blockFile *ptypes.BlockFile) {
	if blockFile == nil {
		return
	}
	receiptByTxID := debankReceiptsByTxID(receipts)

	normalizeDebankTxsForRPC(block, blockFile.Txs, receiptByTxID)

	allTraces := make([]ptypes.Trace, 0, len(blockFile.Traces)+len(blockFile.ErrorTraces))
	allTraces = append(allTraces, blockFile.Traces...)
	allTraces = append(allTraces, blockFile.ErrorTraces...)
	normalizeDebankTracesForRPC(allTraces, receiptByTxID)
	blockFile.Traces, blockFile.ErrorTraces = splitDebankTracesByRPCStatus(allTraces)

	traceByID := debankTraceByID(allTraces)
	allEvents := make([]ptypes.Event, 0, len(blockFile.Events)+len(blockFile.ErrorEvents))
	allEvents = append(allEvents, blockFile.Events...)
	allEvents = append(allEvents, blockFile.ErrorEvents...)
	receiptEvents := normalizeDebankEventsForRPC(allEvents, traceByID, receipts)
	blockFile.Events, blockFile.ErrorEvents = splitDebankEventsByRPCStatus(receiptEvents, traceByID)
	blockFile.StorageContracts = debankStorageContractsFromRPCTraces(allTraces)
}

func debankReceiptsByTxID(receipts types.Receipts) map[string]*types.Receipt {
	byID := make(map[string]*types.Receipt, len(receipts))
	for _, receipt := range receipts {
		if receipt == nil {
			continue
		}
		byID[strings.ToLower(receipt.TxHash.Hex())] = receipt
	}
	return byID
}

func normalizeDebankTxsForRPC(block *evmcore.EvmBlock, txs []ptypes.Transaction, receiptByTxID map[string]*types.Receipt) {
	for i := range txs {
		receipt := receiptByTxID[strings.ToLower(txs[i].ID)]
		if receipt == nil {
			continue
		}
		txs[i].GasUsed = new(big.Int).SetUint64(receipt.GasUsed)
		txs[i].Status = receipt.Status == types.ReceiptStatusSuccessful
		txs[i].TransactionIndex = int64(receipt.TransactionIndex)
		if receipt.EffectiveGasPrice != nil {
			txs[i].GasPrice = new(big.Int).Set(receipt.EffectiveGasPrice)
			continue
		}
		if block != nil && i < len(block.Transactions) && block.Transactions[i] != nil {
			txs[i].GasPrice = new(big.Int).Set(block.Transactions[i].GasPrice())
		}
	}
}

func normalizeDebankTracesForRPC(traces []ptypes.Trace, receiptByTxID map[string]*types.Receipt) {
	for i := range traces {
		receipt := receiptByTxID[strings.ToLower(traces[i].TxID)]
		if receipt == nil || len(traces[i].TraceAddress) != 0 {
			continue
		}
		if traces[i].Error != "" {
			traces[i].GasUsed = nil
			traces[i].Output = nil
			traces[i].SelfStorageChange = false
			traces[i].StorageChange = false
			continue
		}
		traces[i].GasUsed = new(big.Int).SetUint64(receipt.GasUsed)
	}
}

func splitDebankTracesByRPCStatus(allTraces []ptypes.Trace) ([]ptypes.Trace, []ptypes.Trace) {
	traces := make([]ptypes.Trace, 0, len(allTraces))
	errorTraces := make([]ptypes.Trace, 0)
	for _, trace := range allTraces {
		if trace.Error != "" {
			errorTraces = append(errorTraces, trace)
		} else {
			traces = append(traces, trace)
		}
	}
	return traces, errorTraces
}

func debankTraceByID(traces []ptypes.Trace) map[string]ptypes.Trace {
	byID := make(map[string]ptypes.Trace, len(traces))
	for _, trace := range traces {
		if trace.ID == "" {
			continue
		}
		byID[trace.ID] = trace
	}
	return byID
}

func normalizeDebankEventsForRPC(events []ptypes.Event, traceByID map[string]ptypes.Trace, receipts types.Receipts) []ptypes.Event {
	logsByTxID := make(map[string][]*types.Log, len(receipts))
	usedByTxID := make(map[string][]bool, len(receipts))
	for _, receipt := range receipts {
		if receipt == nil || len(receipt.Logs) == 0 {
			continue
		}
		txID := strings.ToLower(receipt.TxHash.Hex())
		logsByTxID[txID] = receipt.Logs
		usedByTxID[txID] = make([]bool, len(receipt.Logs))
	}

	normalized := make([]ptypes.Event, 0, len(events))
	for _, event := range events {
		txID := debankEventTxID(event, traceByID)
		if txID == "" {
			continue
		}
		logs := logsByTxID[txID]
		used := usedByTxID[txID]
		for i, log := range logs {
			if used[i] || !debankEventMatchesLog(event, log) {
				continue
			}
			event.LogIndex = int64(log.Index)
			used[i] = true
			normalized = append(normalized, event)
			break
		}
	}
	return normalized
}

func debankEventTxID(event ptypes.Event, traceByID map[string]ptypes.Trace) string {
	trace, ok := traceByID[event.ParentTraceID]
	if !ok {
		return ""
	}
	return strings.ToLower(trace.TxID)
}

func debankEventMatchesLog(event ptypes.Event, log *types.Log) bool {
	if log == nil || !strings.EqualFold(event.Address, log.Address.Hex()) || !bytes.Equal(event.Data, log.Data) {
		return false
	}
	selector := ""
	topics := make([]string, 0)
	if len(log.Topics) > 0 {
		selector = log.Topics[0].Hex()
		topics = make([]string, 0, len(log.Topics)-1)
		for _, topic := range log.Topics[1:] {
			topics = append(topics, topic.Hex())
		}
	}
	if !strings.EqualFold(event.Selector, selector) || len(event.Topics) != len(topics) {
		return false
	}
	for i := range topics {
		if !strings.EqualFold(event.Topics[i], topics[i]) {
			return false
		}
	}
	return true
}

func splitDebankEventsByRPCStatus(events []ptypes.Event, traceByID map[string]ptypes.Trace) ([]ptypes.Event, []ptypes.Event) {
	traceErrorStatus := debankTraceErrorStatus(traceByID)
	successEvents := make([]ptypes.Event, 0, len(events))
	errorEvents := make([]ptypes.Event, 0)
	for _, event := range events {
		if traceErrorStatus[event.ParentTraceID] {
			errorEvents = append(errorEvents, event)
		} else {
			successEvents = append(successEvents, event)
		}
	}
	return successEvents, errorEvents
}

func debankTraceErrorStatus(traceByID map[string]ptypes.Trace) map[string]bool {
	status := make(map[string]bool, len(traceByID))
	var compute func(string) bool
	compute = func(traceID string) bool {
		if traceID == "" {
			return false
		}
		if value, ok := status[traceID]; ok {
			return value
		}
		trace, ok := traceByID[traceID]
		if !ok {
			status[traceID] = false
			return false
		}
		value := trace.Error != "" || compute(trace.ParentTraceID)
		status[traceID] = value
		return value
	}
	for traceID := range traceByID {
		compute(traceID)
	}
	return status
}

func debankStorageContractsFromRPCTraces(traces []ptypes.Trace) []string {
	contracts := make(map[string]struct{})
	for _, trace := range traces {
		if trace.Error != "" || !trace.SelfStorageChange {
			continue
		}
		var addr string
		switch trace.CallType {
		case "staticcall", "callcode":
			continue
		case "delegatecall":
			addr = trace.From
		case "call":
			addr = trace.To
		default:
			continue
		}
		if addr == "" {
			continue
		}
		contracts[strings.ToLower(addr)] = struct{}{}
	}
	result := make([]string, 0, len(contracts))
	for addr := range contracts {
		result = append(result, addr)
	}
	sort.Strings(result)
	return result
}
