package analyzer

import (
	"fmt"
	"math"

	"github.com/cortze/eth2-state-analyzer/pkg/custom_spec"
	"github.com/pkg/errors"
	"github.com/prysmaticlabs/go-bitfield"

	api "github.com/attestantio/go-eth2-client/api/v1"
	"github.com/attestantio/go-eth2-client/spec"
	"github.com/attestantio/go-eth2-client/spec/phase0"
)

const (
	// participationRate   = 0.945 // about to calculate participation rate
	baseRewardFactor    = 64
	baseRewardsPerEpoch = 4
)

func GetValidatorBalance(customBState CustomBeaconState, valIdx uint64) (uint64, error) {

	balance, err := customBState.ObtainBalance(valIdx)

	if err != nil {
		return 0, err
	}

	return balance, nil
}

func GetParticipationRate(customBState CustomBeaconState, s *StateAnalyzer, m map[string]bitfield.Bitlist) (uint64, error) {

	// participationRate := 0.85

	currentSlot := customBState.ObtainCurrentSlot()
	currentEpoch := customBState.ObtainCurrentEpoch()
	totalAttPreviousEpoch := customBState.ObtainPreviousEpochAttestations()
	totalAttestingVals := customBState.ObtainPreviousEpochValNum()

	// TODO: for now we print it but the goal is to store in a DB
	fmt.Println("Current Epoch: ", currentEpoch)
	fmt.Println("Using Block at: ", currentSlot)
	fmt.Println("Attestations in the last Epoch: ", totalAttPreviousEpoch)
	fmt.Println("Total number of Validators: ", totalAttestingVals)

	return 0, nil
}

// https://kb.beaconcha.in/rewards-and-penalties
// https://consensys.net/blog/codefi/rewards-and-penalties-on-ethereum-20-phase-0/
// TODO: -would be nice to incorporate top the max value wheather there were 2-3 consecutive missed blocks afterwards
func GetMaxReward(valIdx uint64, totValStatus *map[phase0.ValidatorIndex]*api.Validator, totalActiveBalance uint64, participationRate float64) (uint64, error) {
	// First iteration just taking 31/8*BaseReward as Max value
	// BaseReward = ( effectiveBalance * (BaseRewardFactor)/(BaseRewardsPerEpoch * sqrt(activeBalance)) )

	idx := phase0.ValidatorIndex(valIdx)

	valStatus, ok := (*totValStatus)[idx]
	if !ok {
		return 0, errors.New("")
	}
	// apply formula
	//baseReward := GetBaseReward(valStatus.Validator.EffectiveBalance, totalActiveBalance)
	maxReward := ((31.0 / 8.0) * participationRate * (float64(uint64(valStatus.Validator.EffectiveBalance)) * baseRewardFactor))
	maxReward = maxReward / (baseRewardsPerEpoch * math.Sqrt(float64(totalActiveBalance)))
	return uint64(maxReward), nil
}

// directly calculated on the MaxReward fucntion
func GetBaseReward(valEffectiveBalance phase0.Gwei, totalActiveBalance uint64) uint64 {
	// BaseReward = ( effectiveBalance * (BaseRewardFactor)/(BaseRewardsPerEpoch * sqrt(activeBalance)) )
	var baseReward uint64

	sqrt := math.Sqrt(float64(totalActiveBalance))

	denom := baseRewardsPerEpoch * sqrt

	bsRewrd := (float64(uint64(valEffectiveBalance)) * baseRewardFactor) / denom

	baseReward = uint64(bsRewrd)
	return baseReward
}

type CustomBeaconState interface {
	ObtainPreviousEpochAttestations() uint64
	ObtainPreviousEpochValNum() uint64
	ObtainCurrentEpoch() uint64
	ObtainCurrentSlot() uint64
	ObtainBalance(valIdx uint64) (uint64, error)
}

func ObtainBStateByForkVersion(bstate *spec.VersionedBeaconState) (CustomBeaconState, error) {
	switch bstate.Version {

	case spec.DataVersionPhase0:
		return custom_spec.NewPhase0Spec(bstate), nil

	case spec.DataVersionAltair:
		return custom_spec.NewAltairSpec(bstate), nil

	case spec.DataVersionBellatrix:
		return custom_spec.NewBellatrixSpec(bstate), nil
	default:
		return nil, fmt.Errorf("could not figure out the Beacon State Fork Version")
	}
}
