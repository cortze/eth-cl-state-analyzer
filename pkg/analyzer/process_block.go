package analyzer

import (
	"fmt"

	"github.com/attestantio/go-eth2-client/spec/bellatrix"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/migalabs/goteth/pkg/spec"
)

var (
	slotProcesserTag = "slot="
)

func (s *ChainAnalyzer) ProcessBlock(slot phase0.Slot) {
	if !s.metrics.Block {
		return
	}
	routineKey := fmt.Sprintf("%s%d", slotProcesserTag, slot)
	s.processerBook.Acquire(routineKey) // register a new slot to process, good for monitoring

	block := s.downloadCache.BlockHistory.Wait(SlotTo[uint64](slot))
	s.dbClient.Persist(*block)

	for _, item := range block.ExecutionPayload.Withdrawals {
		s.dbClient.Persist(spec.Withdrawal{
			Slot:           block.Slot,
			Index:          item.Index,
			ValidatorIndex: item.ValidatorIndex,
			Address:        item.Address,
			Amount:         item.Amount,
		})
	}

	if s.metrics.Transactions {
		s.processTransactions(block)
	}
	s.processerBook.FreePage(routineKey)
}

func (s *ChainAnalyzer) processTransactions(block *spec.AgnosticBlock) {

	for idx, tx := range block.ExecutionPayload.Transactions {
		go func(txID int, transaction bellatrix.Transaction) {
			detailedTx, err := s.cli.RequestTransactionDetails(
				transaction,
				block.Slot,
				block.ExecutionPayload.BlockNumber,
				block.ExecutionPayload.Timestamp)
			if err != nil {
				log.Errorf("could not request transaction details in slot %d for transaction %d: %s", block.Slot, txID, err)
			}
			log.Tracef("persisting transaction metrics: slot %d, tx number: %d", block.Slot, txID)
			s.dbClient.Persist(detailedTx)
		}(idx, tx)

	}
}
