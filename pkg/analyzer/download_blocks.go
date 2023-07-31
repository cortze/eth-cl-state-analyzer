package analyzer

import (
	"sync"
	"time"

	v1 "github.com/attestantio/go-eth2-client/api/v1"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/cortze/eth-cl-state-analyzer/pkg/db"
	"github.com/cortze/eth-cl-state-analyzer/pkg/spec"
	"github.com/cortze/eth-cl-state-analyzer/pkg/utils"
	"github.com/pkg/errors"
)

// This routine is able to download block by block in the slot range
func (s *ChainAnalyzer) runDownloadBlocks(wgDownload *sync.WaitGroup) {
	defer wgDownload.Done()
	log.Info("Launching Beacon Block Requester")
	queue := StateQueue{}

loop:
	// loop over the list of slots that we need to analyze
	for slot := s.initSlot; slot < s.finalSlot; slot += 1 {

		select {
		case <-s.ctx.Done():
			log.Info("context has died, closing block requester routine")
			break loop

		default:
			if s.stop {
				log.Info("sudden shutdown detected, block downloader routine")
				break loop
			}

			log.Infof("requesting Beacon Block from endpoint: slot %d", slot)
			s.DownloadNewBlock(&queue, phase0.Slot(slot))

			// if epoch boundary, download state
			if slot%spec.SlotsPerEpoch == 0 {
				// new epoch
				s.DownloadNewState(&queue, slot-1, false)
			}

		}

	}

	log.Infof("Block Download routine finished")
}

func (s *ChainAnalyzer) runDownloadBlocksFinalized(wgDownload *sync.WaitGroup) {
	defer wgDownload.Done()
	log.Info("Launching Beacon Block Finalized Requester")

	// ------ fill from last epoch in database to current head -------

	// obtain current finalized
	finalizedBlock, err := s.cli.RequestFinalizedBeaconBlock()
	if err != nil {
		log.Panicf("could not request the finalized block: %s", err)
	}

	// obtain current head
	headSlot := s.cli.RequestCurrentHead()

	// obtain last epoch in database
	nextSlotDownload, err := s.dbClient.ObtainLastSlot()
	if err != nil {
		log.Errorf("could not obtain last slot in database: %s", err)
	}
	// if we did not get a last slot from the database, or we were too close to the head
	// then start from the current finalized in the chain
	if nextSlotDownload == 0 || nextSlotDownload > finalizedBlock.Slot {
		log.Infof("continue from finalized slot %d, epoch %d", finalizedBlock.Slot, finalizedBlock.Slot/spec.SlotsPerEpoch)
		nextSlotDownload = finalizedBlock.Slot
	} else {
		// database detected
		log.Infof("database detected, continue from slot %d, epoch %d", nextSlotDownload, nextSlotDownload/spec.SlotsPerEpoch)
		nextSlotDownload = nextSlotDownload - (epochsToFinalizedTentative * spec.SlotsPerEpoch) // 2 epochs before

	}

	queue := NewStateQueue(finalizedBlock)
	for nextSlotDownload < headSlot {
		log.Infof("filling missing blocks: %d", nextSlotDownload)
		s.DownloadNewBlock(&queue, phase0.Slot(nextSlotDownload))
		if nextSlotDownload%spec.SlotsPerEpoch == 0 {
			// new epoch
			s.DownloadNewState(&queue, nextSlotDownload-1, true)
		}
		nextSlotDownload = nextSlotDownload + 1
		if s.stop {
			log.Info("sudden shutdown detected, block downloader routine")
			return
		}
	}

	// -----------------------------------------------------------------------------------
	s.eventsObj.SubscribeToHeadEvents()
	s.eventsObj.SubscribeToFinalizedCheckpointEvents()
	s.eventsObj.SubscribeToReorgsEvents()
	ticker := time.NewTicker(utils.RoutineFlushTimeout)
	// loop over the list of slots that we need to analyze

	for {
		select {

		case headSlot := <-s.eventsObj.HeadChan: // wait for new head event
			// make the block query
			log.Tracef("received new head signal: %d", headSlot)

			for nextSlotDownload <= headSlot {
				if s.stop {
					log.Info("sudden shutdown detected, block downloader routine")
					return
				}

				s.DownloadNewBlock(&queue, phase0.Slot(nextSlotDownload))

				// if epoch boundary, download state
				if nextSlotDownload%spec.SlotsPerEpoch == 0 {
					// new epoch
					s.DownloadNewState(&queue, nextSlotDownload-1, true)
				}
				nextSlotDownload = nextSlotDownload + 1

			}
		case newFinalCheckpoint := <-s.eventsObj.FinalizedChan:
			s.dbClient.Persist(db.ChepointTypeFromCheckpoint(newFinalCheckpoint))

			slotRewind, ok, err := s.CheckFinalized(newFinalCheckpoint, &queue)

			if err != nil {
				log.Errorf("error checking finalized: %s", err)
				s.stop = true
			}

			if !ok {
				// there was a rewind
				nextSlotDownload = slotRewind
			}

		case newReorg := <-s.eventsObj.ReorgChan:
			s.dbClient.Persist(db.ReorgTypeFromReorg(newReorg))
			baseSlot := newReorg.Slot - phase0.Slot(newReorg.Depth)

			// if we have already downloaded baseSlot
			if nextSlotDownload > baseSlot {
				log.Infof("rewinding to %d", newReorg.Slot-phase0.Slot(newReorg.Depth))

				s.Reorg(baseSlot, newReorg.Slot, &queue)
				nextSlotDownload = queue.HeadBlock.Slot + 1
			}

		case <-s.ctx.Done():
			log.Info("context has died, closing block requester routine")
			return

		case <-ticker.C:
			if s.stop {
				log.Info("sudden shutdown detected, block downloader routine")
				return
			}
		}

	}
}

func (s ChainAnalyzer) DownloadNewBlock(queue *StateQueue, slot phase0.Slot) {

	ticker := time.NewTicker(minBlockReqTime)
	newBlock, err := s.cli.RequestBeaconBlock(slot)
	if err != nil {
		log.Panicf("block error at slot %d: %s", slot, err)
	}
	queue.AddNewBlock(newBlock)

	// send task to be processed
	blockTask := &BlockTask{
		Block:    newBlock,
		Slot:     uint64(slot),
		Proposed: newBlock.Proposed,
	}
	log.Tracef("sending a new task for slot %d", slot)
	s.blockTaskChan <- blockTask

	// store transactions if it has been enabled
	if s.metrics.Transactions {

		for _, tx := range newBlock.ExecutionPayload.Transactions {
			log.Tracef("sending a new tx task for slot %d", slot)
			s.transactionTaskChan <- &TransactionTask{
				Slot:        slot,
				Transaction: tx,
			}
		}
	}

	<-ticker.C
	// check if the min Request time has been completed (to avoid spaming the API)
}

func (s *ChainAnalyzer) Reorg(baseSlot phase0.Slot, slot phase0.Slot, queue *StateQueue) {

	s.RewindBlockMetrics(baseSlot)

	baseEpoch := phase0.Epoch((baseSlot) / spec.SlotsPerEpoch)
	reorgEpoch := phase0.Epoch(slot / spec.SlotsPerEpoch)
	if slot%spec.SlotsPerEpoch == 31 || // end of epoch
		baseEpoch != reorgEpoch { // the reorg crosses and epoch boundary
		epoch := baseEpoch - 1
		s.RewindEpochMetrics(epoch) // epoch metrics are written at epoch(nextstate)-1

	}

	// persist orphans
	var orphanBlock db.OrphanBlock
	for i := baseSlot; i < slot; i++ {

		_, ok := queue.BlockHistory[i]

		if ok { // only persist orphans if we had downloaded them
			orphanBlock = db.OrphanBlock(queue.BlockHistory[i])
			s.dbClient.Persist(orphanBlock)
		}

	}

	// rewind states and roots until the reorg base slot
	queue.Rewind(baseSlot)

}

func (s *ChainAnalyzer) RewindBlockMetrics(slot phase0.Slot) {
	log.Infof("deleting block data from %d (included) onwards", slot)
	s.dbClient.Persist(db.BlockDropType(slot))
	s.dbClient.Persist(db.TransactionDropType(slot))
	s.dbClient.Persist(db.WithdrawalDropType(slot))
}

func (s *ChainAnalyzer) RewindEpochMetrics(epoch phase0.Epoch) {
	log.Infof("deleting epoch data from %d (included) onwards", epoch)
	s.dbClient.Persist(db.EpochDropType(epoch))
	s.dbClient.Persist(db.ProposerDutiesDropType(epoch))
	s.dbClient.Persist(db.ValidatorRewardsDropType(epoch + 1)) // validator rewards are always written at epoch+1
}

func (s *ChainAnalyzer) CheckFinalized(checkpoint v1.FinalizedCheckpointEvent, queue *StateQueue) (phase0.Slot, bool, error) {

	finalizedBlock, err := s.cli.RequestBeaconBlock(phase0.Slot(checkpoint.Epoch * spec.SlotsPerEpoch))

	if err != nil {
		return 0, false, errors.Wrap(err, "error requesting finalized checkpoint block")
	}

	// A new finalized arrived, remove old roots from the list

	for i := queue.LatestFinalized.Slot; i < finalizedBlock.Slot; i++ {
		// for every slot, request the stateroot and compare with our list
		requestedRoot := s.cli.RequestStateRoot(i)

		_, ok := queue.BlockHistory[i]
		if ok {
			// we dont review parent roots, so we need to review block by block
			if requestedRoot == queue.BlockHistory[i].StateRoot {
				// the roots are the same, all ok
				queue.AdvanceFinalized(i)
			} else {

				log.Infof("Checkpoint mismatch!")
				log.Infof("Chain Checkpoint for slot %d: %s", i, requestedRoot.String())
				log.Infof("Stored Checkpoint for slot %d: %s", i, queue.BlockHistory[i].StateRoot.String())
				log.Infof("rewinding to slot %d...", i)
				// rewind until this slot
				s.RewindBlockMetrics(i)
				s.RewindEpochMetrics(phase0.Epoch(i/spec.SlotsPerEpoch) - 1)
				// epoch metrics are written a current state (next state epoch -1)

				newQueue := NewStateQueue(finalizedBlock)
				*queue = newQueue
				return i - (epochsToFinalizedTentative * spec.SlotsPerEpoch), false, nil
				// redownload from one epoch before the epoch metrics were deleted
				// return slot at which download should re-continue

			}
		}
	}

	log.Infof("state roots verified, advance stored finalized to %d", queue.LatestFinalized.Slot)
	return finalizedBlock.Slot, true, nil
}
