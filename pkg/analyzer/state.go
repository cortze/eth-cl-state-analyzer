package analyzer

import (
	"context"
	"sync"
	"time"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	"github.com/cortze/eth-cl-state-analyzer/pkg/clientapi"
	"github.com/cortze/eth-cl-state-analyzer/pkg/db"
	"github.com/cortze/eth-cl-state-analyzer/pkg/events"
	"github.com/cortze/eth-cl-state-analyzer/pkg/spec"
	"github.com/cortze/eth-cl-state-analyzer/pkg/spec/metrics"
	"github.com/cortze/eth-cl-state-analyzer/pkg/utils"
)

// TODO: reorganize routines
type StateAnalyzer struct {
	ctx      context.Context
	cancel   context.CancelFunc
	initTime time.Time

	ValidatorIndexes []uint64

	// User inputs
	InitSlot           phase0.Slot
	FinalSlot          phase0.Slot
	SlotRanges         []uint64
	MissingVals        bool
	validatorWorkerNum int
	downloadMode       string
	Metrics            DBMetrics
	PoolValidators     []utils.PoolKeys

	// Clients
	cli      *clientapi.APIClient
	dbClient *db.PostgresDBService

	// Channels
	EpochTaskChan chan *EpochTask
	ValTaskChan   chan *ValTask

	// Control Variables
	finishDownload bool
	routineClosed  chan struct{}
	eventsObj      events.Events
}

func NewStateAnalyzer(
	pCtx context.Context,
	httpCli *clientapi.APIClient,
	initSlot uint64,
	finalSlot uint64,
	idbUrl string,
	workerNum int,
	dbWorkerNum int,
	downloadMode string,
	customPoolsFile string,
	missingVals bool,
	metrics string) (*StateAnalyzer, error) {
	log.Infof("generating new State Analzyer from slots %d:%d", initSlot, finalSlot)
	// gen new ctx from parent
	ctx, cancel := context.WithCancel(pCtx)

	slotRanges := make([]uint64, 0)

	// if historical is active
	if downloadMode == "hybrid" || downloadMode == "historical" {

		// Check if the range of slots is valid
		if finalSlot <= initSlot {
			return nil, errors.New("provided slot range isn't valid")
		}
		// calculate the list of slots that we will analyze

		epochRange := uint64(0)

		// minimum slot is 31
		// force to be in the previous epoch than select by user
		initEpoch := uint64(initSlot) / 32
		finalEpoch := uint64(finalSlot / 32)

		initSlot = (initEpoch+1)*spec.SlotsPerEpoch - 1   // take last slot of init Epoch
		finalSlot = (finalEpoch+1)*spec.SlotsPerEpoch - 1 // take last slot of final Epoch

		// start two epochs before and end two epochs after
		for i := initSlot - (spec.SlotsPerEpoch * 2); i <= (finalSlot + spec.SlotsPerEpoch*2); i += spec.SlotsPerEpoch {
			slotRanges = append(slotRanges, i)
			epochRange++
		}
		log.Debug("slotRanges are:", slotRanges)
	}
	// size of channel of maximum number of workers that read from the channel, testing have shown it works well for 500K validators
	i_dbClient, err := db.ConnectToDB(ctx, idbUrl, dbWorkerNum)
	if err != nil {
		return nil, errors.Wrap(err, "unable to generate DB Client.")
	}

	poolValidators := make([]utils.PoolKeys, 0)
	if customPoolsFile != "" {
		poolValidators, err = utils.ReadCustomValidatorsFile(customPoolsFile)
		if err != nil {
			return nil, errors.Wrap(err, "unable to read custom pools file.")
		}
		for _, item := range poolValidators {
			log.Infof("monitoring pool %s of length %d", item.PoolName, len(item.ValIdxs))
		}

	}

	metricsObj, err := NewMetrics(metrics)
	if err != nil {
		return nil, errors.Wrap(err, "unable to read metric.")
	}

	return &StateAnalyzer{
		ctx:                ctx,
		cancel:             cancel,
		InitSlot:           phase0.Slot(initSlot),
		FinalSlot:          phase0.Slot(finalSlot),
		SlotRanges:         slotRanges,
		EpochTaskChan:      make(chan *EpochTask, 1),
		ValTaskChan:        make(chan *ValTask, workerNum), // chan length is the same as the number of workers
		cli:                httpCli,
		dbClient:           i_dbClient,
		validatorWorkerNum: workerNum,
		routineClosed:      make(chan struct{}),
		downloadMode:       downloadMode,
		eventsObj:          events.NewEventsObj(ctx, httpCli),
		PoolValidators:     poolValidators,
		MissingVals:        missingVals,
		Metrics:            metricsObj,
	}, nil
}

func (s *StateAnalyzer) Run() {
	defer s.cancel()
	// Get init time
	s.initTime = time.Now()

	log.Info("State Analyzer initialized at ", s.initTime)

	// State requester
	var wgDownload sync.WaitGroup
	downloadFinishedFlag := false

	// Rewards per process
	var wgProcess sync.WaitGroup
	processFinishedFlag := false

	// Workers to process each validator rewards
	var wgWorkers sync.WaitGroup

	totalTime := int64(0)
	start := time.Now()

	if s.downloadMode == "hybrid" || s.downloadMode == "historical" {
		// State requester + Task generator
		wgDownload.Add(1)
		go s.runDownloadStates(&wgDownload)
	}

	if s.downloadMode == "hybrid" || s.downloadMode == "finalized" {
		// State requester in finalized slots, not used for now
		wgDownload.Add(1)
		go s.runDownloadStatesFinalized(&wgDownload)
	}
	wgProcess.Add(1)
	go s.runProcessState(&wgProcess, &downloadFinishedFlag)

	for i := 0; i < s.validatorWorkerNum; i++ {
		// state workers, receiving State and valIdx to measure performance
		wlog := logrus.WithField(
			"worker", i,
		)

		wlog.Tracef("Launching Task Worker")
		wgWorkers.Add(1)
		go s.runWorker(wlog, &wgWorkers, &processFinishedFlag)
	}

	wgDownload.Wait()
	downloadFinishedFlag = true
	log.Info("Beacon State Downloads finished")

	wgProcess.Wait()
	processFinishedFlag = true
	log.Info("Beacon State Processing finished")

	wgWorkers.Wait()
	log.Info("All validator workers finished")
	s.dbClient.DoneTasks()
	<-s.dbClient.FinishSignalChan
	log.Info("All database workers finished")
	s.dbClient.Close()
	close(s.ValTaskChan)
	totalTime += int64(time.Since(start).Seconds())
	analysisDuration := time.Since(s.initTime).Seconds()

	if s.finishDownload {
		s.routineClosed <- struct{}{}
	}
	log.Info("State Analyzer finished in ", analysisDuration)

}

func (s *StateAnalyzer) Close() {
	log.Info("Sudden closed detected, closing StateAnalyzer")
	s.finishDownload = true
	<-s.routineClosed
	s.cancel()
}

type EpochTask struct {
	NextState spec.AgnosticState
	State     spec.AgnosticState
	PrevState spec.AgnosticState
	Finalized bool
}

type ValTask struct {
	ValIdxs         []phase0.ValidatorIndex
	StateMetricsObj metrics.StateMetrics
	OnlyPrevAtt     bool
	PoolName        string
	Finalized       bool
}
