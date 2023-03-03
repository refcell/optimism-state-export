package app

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/holiman/uint256"
	"github.com/ledgerwatch/erigon-lib/kv"
	"github.com/ledgerwatch/erigon/cmd/utils"
	"github.com/ledgerwatch/erigon/common"
	"github.com/ledgerwatch/erigon/core"
	"github.com/ledgerwatch/erigon/core/rawdb"
	"github.com/ledgerwatch/erigon/core/state"
	"github.com/ledgerwatch/erigon/core/types"
	"github.com/ledgerwatch/erigon/crypto"
	"github.com/ledgerwatch/erigon/eth"
	stageSync "github.com/ledgerwatch/erigon/eth/stagedsync/stages"
	"github.com/ledgerwatch/erigon/params"
	"github.com/ledgerwatch/erigon/rlp"
	turboNode "github.com/ledgerwatch/erigon/turbo/node"
	"github.com/ledgerwatch/erigon/turbo/stages"
	"github.com/ledgerwatch/erigon/turbo/trie"

	"github.com/ledgerwatch/log/v3"
	"github.com/urfave/cli"
)

const (
	importBatchSize = 2500
)

var importCommand = cli.Command{
	Action:    MigrateFlags(importChain),
	Name:      "import",
	Usage:     "Import a blockchain file",
	ArgsUsage: "<filename> (<filename 2> ... <filename N>) ",
	Flags: []cli.Flag{
		utils.DataDirFlag,
		utils.ChainFlag,
		utils.ImportExecutionFlag,
	},
	Category: "BLOCKCHAIN COMMANDS",
	Description: `
The import command imports blocks from an RLP-encoded form. The form can be one file
with several RLP-encoded blocks, or several files can be used.
If only one file is used, import error will result in failure. If several files are used,
processing will proceed even if an individual RLP-file import failure occurs.`,
}

var importReceiptCommand = cli.Command{
	Action:    MigrateFlags(importReceipts),
	Name:      "import-receipts",
	Usage:     "Import a receipts file",
	ArgsUsage: "<filename> ",
	Flags: []cli.Flag{
		utils.DataDirFlag,
		utils.ChainFlag,
	},
	Category: "BLOCKCHAIN COMMANDS",
	Description: `
The import command imports receipts from an RLP-encoded form.`,
}

var importStateCommand = cli.Command{
	Action:    MigrateFlags(importState),
	Name:      "import-state",
	Usage:     "Import a state file",
	ArgsUsage: "<filename> <blockNum>",
	Flags: []cli.Flag{
		utils.DataDirFlag,
		utils.ChainFlag,
	},
	Category: "BLOCKCHAIN COMMANDS",
	Description: `
The import command imports state from a json form`,
}

func importReceipts(ctx *cli.Context) error {
	if len(ctx.Args()) < 1 {
		utils.Fatalf("This command requires an argument.")
	}

	logger := log.New(ctx)

	nodeCfg := turboNode.NewNodConfigUrfave(ctx)
	ethCfg := turboNode.NewEthConfigUrfave(ctx, nodeCfg)

	stack := makeConfigNode(nodeCfg)
	defer stack.Close()

	ethereum, err := eth.New(stack, ethCfg, logger)
	if err != nil {
		return err
	}

	if err := ImportReceipts(ethereum, ethereum.ChainDB(), ctx.Args().First()); err != nil {
		return err
	}

	return nil
}

func importState(ctx *cli.Context) error {
	if ctx.NArg() < 2 {
		utils.Fatalf("This command requires an argument.")
	}

	logger := log.New(ctx)

	nodeCfg := turboNode.NewNodConfigUrfave(ctx)
	ethCfg := turboNode.NewEthConfigUrfave(ctx, nodeCfg)

	stack := makeConfigNode(nodeCfg)
	defer stack.Close()

	ethereum, err := eth.New(stack, ethCfg, logger)
	if err != nil {
		return err
	}
	fn := ctx.Args().First()
	blockNum, err := strconv.ParseInt(ctx.Args().Get(1), 10, 64)
	if err != nil {
		utils.Fatalf("Export error in parsing parameters: block number not an integer\n")
	}

	if err := ImportState(ethereum, fn, uint64(blockNum)); err != nil {
		return err
	}

	return nil
}

// modified from l2geth's core/state/dump.go
type ImportAccount struct {
	Balance  string                 `json:"balance"`
	Nonce    uint64                 `json:"nonce"`
	Root     string                 `json:"root"`
	CodeHash string                 `json:"codeHash"`
	Code     string                 `json:"code,omitempty"`
	Storage  map[common.Hash]string `json:"storage,omitempty"`
}

type ImportAlloc map[common.Address]ImportAccount

func (ia *ImportAlloc) UnmarshalJson(data []byte) error {
	m := make(ImportAlloc)
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	*ia = make(ImportAlloc)
	for addr, a := range m {
		(*ia)[common.Address(addr)] = a
	}
	return nil
}

func ImportState(ethereum *eth.Ethereum, fn string, blockNumber uint64) error {
	log.Info("Importing state", "file", fn)
	log.Info("Importing state for block number", "blockNumber", blockNumber)
	fh, err := os.Open(fn)
	if err != nil {
		return err
	}

	// TODO: make as json stream
	decoder := json.NewDecoder(fh)
	ia := make(ImportAlloc)

	if err := decoder.Decode(&ia); err != nil {
		return err
	}

	db := ethereum.ChainDB()
	tx, err := db.BeginRw(ethereum.SentryCtx())
	if err != nil {
		return err
	}
	defer tx.Rollback()

	r, w := state.NewDbStateReader(tx), state.NewDbStateWriter(tx, blockNumber)
	//stateReader := state.NewPlainStateReader(tx)
	statedb := state.New(r)

	idx := 0
	for address, account := range ia {
		idx += 1
		fmt.Println(idx, address.Hex())
		balanceBigInt, ok := new(big.Int).SetString(account.Balance, 10)
		if !ok {
			return errors.New("balance bigint conversion failure")
		}
		balance, overflow := uint256.FromBig(balanceBigInt)
		if overflow {
			return errors.New("balance overflow")
		}
		statedb.AddBalance(address, balance)
		hexCode := account.Code
		code, err := hex.DecodeString(hexCode)
		if err != nil {
			return fmt.Errorf("code hexdecode failure, %s", hexCode)
		}
		hexCodeHash := account.CodeHash
		codeHash, err := hex.DecodeString(hexCodeHash)
		if err != nil {
			return fmt.Errorf("codehash hexdecode failure, %s", hexCodeHash)
		}
		tempCodeHash := crypto.Keccak256(code)
		if !bytes.Equal(tempCodeHash, codeHash) {
			return fmt.Errorf("codehash mismatch, expected %x, got %x", codeHash, tempCodeHash)
		}
		statedb.SetCode(address, code)
		statedb.SetNonce(address, account.Nonce)
		for key, hexValue := range account.Storage {
			key := key
			value, err := hex.DecodeString(hexValue)
			if err != nil {
				return errors.New("value hexdecode failure")