package spec

import (
	"fmt"
	"math"

	"github.com/attestantio/go-eth2-client/spec"
	"github.com/attestantio/go-eth2-client/spec/altair"
	"github.com/attestantio/go-eth2-client/spec/phase0"
)

// This Wrapper is meant to include all common objects across Ethereum Hard Fork Specs
type AgnosticState struct {
	Version                 spec.DataVersion
	Balances                []phase0.Gwei                     // balance of each validator
	Validators              []*phase0.Validator               // list of validators
	TotalActiveBalance      phase0.Gwei                       // effective balance
	TotalActiveRealBalance  phase0.Gwei                       // real balance
	AttestingBalance        []phase0.Gwei                     // one attesting balance per flag (of the previous epoch attestations)
	MaxAttestingBalance     phase0.Gwei                       // the effective balance of validators that did attest in any manner
	EpochStructs            EpochDuties                       // structs about beacon committees, proposers and attestation
	CorrectFlags            [][]uint                          // one aray per flag
	AttestingVals           []bool                            // the number of validators that did attest in the last epoch
	PrevAttestations        []*phase0.PendingAttestation      // array of attestations (currently only for Phase0)
	NumAttestingVals        uint                              // number of validators that attested in the last epoch
	NumActiveVals           uint                              // number of active validators in the epoch
	ValAttestationInclusion map[phase0.ValidatorIndex]ValVote // one map per validator including which slots it had to attest and when it was included
	AttestedValsPerSlot     map[phase0.Slot][]uint64          // for each slot in the epoch, how many vals attested
	Epoch                   phase0.Epoch                      // Epoch of the state
	Slot                    phase0.Slot                       // Slot of the state
	BlockRoots              [][]byte                          // array of block roots at this point (8192)
	SyncCommittee           altair.SyncCommittee              // list of pubkeys in the current sync committe
	BlockList               []AgnosticBlock                   // list of blocks for the given Epoch
}

func GetCustomState(bstate spec.VersionedBeaconState, epochDuties EpochDuties) (AgnosticState, error) {
	var agnosticState AgnosticState
	var err error
	switch bstate.Version {

	case spec.DataVersionPhase0:
		agnosticState = NewPhase0State(bstate)

	case spec.DataVersionAltair:
		agnosticState = NewAltairState(bstate)

	case spec.DataVersionBellatrix:
		agnosticState = NewBellatrixState(bstate)

	case spec.DataVersionCapella:
		agnosticState = NewCapellaState(bstate)
	default:
		return AgnosticState{}, fmt.Errorf("could not figure out the Beacon State Fork Version: %s", bstate.Version)
	}

	agnosticState.EpochStructs = epochDuties

	return agnosticState, err
}

// Initialize all necessary arrays and process anything standard
func (p *AgnosticState) Setup() error {
	if p.Validators == nil {
		return fmt.Errorf("validator list not provided, cannot create")
	}
	arrayLen := len(p.Validators)
	if p.PrevAttestations == nil {
		p.PrevAttestations = make([]*phase0.PendingAttestation, 0)
	}

	p.AttestingBalance = make([]phase0.Gwei, 3)
	p.AttestingVals = make([]bool, arrayLen)
	p.CorrectFlags = make([][]uint, 3)
	p.ValAttestationInclusion = make(map[phase0.ValidatorIndex]ValVote)
	p.AttestedValsPerSlot = make(map[phase0.Slot][]uint64)

	for i := range p.CorrectFlags {
		p.CorrectFlags[i] = make([]uint, arrayLen)
	}

	p.TotalActiveBalance = p.GetTotalActiveEffBalance()
	p.TotalActiveRealBalance = p.GetTotalActiveRealBalance()
	return nil
}

// the length of the valList = number of validators
// each position represents a valIdx
// if the item has a number > 0, count it
func (p AgnosticState) ValsEffectiveBalance(valList []phase0.Gwei) phase0.Gwei {

	resultBalance := phase0.Gwei(0)

	for valIdx, item := range valList { // loop over validators
		if item > 0 && valIdx < len(p.Validators) {
			resultBalance += p.Validators[valIdx].EffectiveBalance
		}
	}

	return resultBalance
}

func (p AgnosticState) Balance(valIdx phase0.ValidatorIndex) (phase0.Gwei, error) {
	if uint64(len(p.Balances)) < uint64(valIdx) {
		err := fmt.Errorf("phase0 - validator index %d wasn't activated in slot %d", valIdx, p.Slot)
		return 0, err
	}
	balance := p.Balances[valIdx]

	return balance, nil
}

// Edit NumActiveVals
func (p *AgnosticState) GetTotalActiveEffBalance() phase0.Gwei {

	val_array := make([]phase0.Gwei, len(p.Validators))
	p.NumActiveVals = 0 // any time we calculate total effective balance, the number of active vals is refreshed and recalculated
	for idx := range val_array {
		if IsActive(*p.Validators[idx], phase0.Epoch(p.Epoch)) {
			val_array[idx] += 1
			p.NumActiveVals++
		}

	}

	return p.ValsEffectiveBalance(val_array)
}

// Not effective balance, but balance
func (p AgnosticState) GetTotalActiveRealBalance() phase0.Gwei {
	totalBalance := phase0.Gwei(0)

	for idx := range p.Validators {
		if IsActive(*p.Validators[idx], phase0.Epoch(p.Epoch)) {
			totalBalance += p.Balances[idx]
		}

	}
	return totalBalance
}

func (p AgnosticState) GetMissingBlocks() []phase0.Slot {

	result := make([]phase0.Slot, 0)
	for _, block := range p.BlockList {
		if !block.Proposed {
			result = append(result, block.Slot)
		}
	}

	return result
}

// List of validators that were active in the epoch of the state
// Lenght of the list is variable, each position containing the valIdx
func (p AgnosticState) GetActiveVals() []uint64 {
	result := make([]uint64, 0)

	for i, val := range p.Validators {
		if IsActive(*val, phase0.Epoch(p.Epoch)) {
			result = append(result, uint64(i))
		}

	}
	return result
}

// List of validators that were in the epoch of the state
// Length of the list is variable, each position containing the valIdx
func (p AgnosticState) GetAllVals() []phase0.ValidatorIndex {
	result := make([]phase0.ValidatorIndex, 0)

	for i := range p.Validators {
		result = append(result, phase0.ValidatorIndex(i))

	}
	return result
}

// Returns a list of missing flags for the corresponding valIdx
func (p AgnosticState) MissingFlags(valIdx phase0.ValidatorIndex) []bool {
	result := []bool{false, false, false}

	if int(valIdx) >= len(p.CorrectFlags[0]) {
		return result
	}

	for i, item := range p.CorrectFlags {
		if IsActive(*p.Validators[valIdx], phase0.Epoch(p.Epoch-1)) && item[valIdx] == 0 {
			if item[valIdx] == 0 {
				// no missing flag
				result[i] = true
			}
		}

	}
	return result
}

// Argument: 0 for source, 1 for target and 2 for head
// Return the count of missing flag in the previous epoch participation / attestations
func (p AgnosticState) GetMissingFlagCount(flagIndex int) uint64 {
	result := uint64(0)
	for idx, item := range p.CorrectFlags[flagIndex] {
		// if validator was active and no correct flag
		if IsActive(*p.Validators[idx], phase0.Epoch(p.Epoch-1)) && item == 0 {
			result += 1
		}
	}

	return result
}

func (p AgnosticState) GetValStatus(valIdx phase0.ValidatorIndex) ValidatorStatus {

	if p.Validators[valIdx].ExitEpoch <= phase0.Epoch(p.Epoch) {
		return EXIT_STATUS
	}

	if p.Validators[valIdx].Slashed {
		return SLASHED_STATUS
	}

	if p.Validators[valIdx].ActivationEpoch <= phase0.Epoch(p.Epoch) {
		return ACTIVE_STATUS
	}

	return QUEUE_STATUS

}

// This Wrapper is meant to include all necessary data from the Phase0 Fork
func NewPhase0State(bstate spec.VersionedBeaconState) AgnosticState {

	balances := make([]phase0.Gwei, 0)

	for _, item := range bstate.Phase0.Balances {
		balances = append(balances, phase0.Gwei(item))
	}

	phase0Obj := AgnosticState{
		Version:          bstate.Version,
		Balances:         balances,
		Validators:       bstate.Phase0.Validators,
		Epoch:            phase0.Epoch(bstate.Phase0.Slot / SlotsPerEpoch),
		Slot:             phase0.Slot(bstate.Phase0.Slot),
		BlockRoots:       bstate.Phase0.BlockRoots,
		PrevAttestations: bstate.Phase0.PreviousEpochAttestations,
	}

	phase0Obj.Setup()

	return phase0Obj

}

// This Wrapper is meant to include all necessary data from the Altair Fork
func NewAltairState(bstate spec.VersionedBeaconState) AgnosticState {

	altairObj := AgnosticState{
		Version:       bstate.Version,
		Balances:      bstate.Altair.Balances,
		Validators:    bstate.Altair.Validators,
		Epoch:         phase0.Epoch(bstate.Altair.Slot / SlotsPerEpoch),
		Slot:          bstate.Altair.Slot,
		BlockRoots:    RootToByte(bstate.Altair.BlockRoots),
		SyncCommittee: *bstate.Altair.CurrentSyncCommittee,
	}

	altairObj.Setup()

	ProcessAltairAttestations(&altairObj, bstate.Altair.PreviousEpochParticipation)

	return altairObj
}

func ProcessAltairAttestations(customState *AgnosticState, participation []altair.ParticipationFlags) {
	// calculate attesting vals only once
	flags := []altair.ParticipationFlag{
		altair.TimelySourceFlagIndex,
		altair.TimelyTargetFlagIndex,
		altair.TimelyHeadFlagIndex}

	for participatingFlag := range flags {

		flag := altair.ParticipationFlags(math.Pow(2, float64(participatingFlag)))

		for valIndex, item := range participation {
			// Here we have one item per validator
			// Item is a 3-bit string
			// each bit represents a flag

			if (item & flag) == flag {
				// The attestation has a timely flag, therefore we consider it correct flag
				customState.CorrectFlags[participatingFlag][valIndex] += uint(1)

				// we sum the attesting balance in the corresponding flag index
				customState.AttestingBalance[participatingFlag] += customState.Validators[valIndex].EffectiveBalance

				// if this validator was not counted as attesting before, count it now
				if !customState.AttestingVals[valIndex] {
					customState.NumAttestingVals++
					customState.MaxAttestingBalance = customState.Validators[valIndex].EffectiveBalance
				}
				customState.AttestingVals[valIndex] = true
			}
		}
	}
}

// This Wrapper is meant to include all necessary data from the Bellatrix Fork
func NewBellatrixState(bstate spec.VersionedBeaconState) AgnosticState {

	bellatrixObj := AgnosticState{
		Version:       bstate.Version,
		Balances:      bstate.Bellatrix.Balances,
		Validators:    bstate.Bellatrix.Validators,
		Epoch:         phase0.Epoch(bstate.Bellatrix.Slot / SlotsPerEpoch),
		Slot:          bstate.Bellatrix.Slot,
		BlockRoots:    RootToByte(bstate.Bellatrix.BlockRoots),
		SyncCommittee: *bstate.Bellatrix.CurrentSyncCommittee,
	}

	bellatrixObj.Setup()

	ProcessAltairAttestations(&bellatrixObj, bstate.Bellatrix.PreviousEpochParticipation)

	return bellatrixObj
}

// This Wrapper is meant to include all necessary data from the Capella Fork
func NewCapellaState(bstate spec.VersionedBeaconState) AgnosticState {

	capellaObj := AgnosticState{
		Version:       bstate.Version,
		Balances:      bstate.Capella.Balances,
		Validators:    bstate.Capella.Validators,
		Epoch:         phase0.Epoch(bstate.Capella.Slot / SlotsPerEpoch),
		Slot:          bstate.Capella.Slot,
		BlockRoots:    RootToByte(bstate.Capella.BlockRoots),
		SyncCommittee: *bstate.Capella.CurrentSyncCommittee,
	}

	capellaObj.Setup()

	ProcessAltairAttestations(&capellaObj, bstate.Capella.PreviousEpochParticipation)

	return capellaObj
}
