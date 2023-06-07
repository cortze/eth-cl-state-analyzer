package clientapi

import (
	"context"
	"strconv"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/cortze/eth-cl-state-analyzer/pkg/spec"
)

func (s APIClient) NewEpochDuties(epoch phase0.Epoch) spec.EpochDuties {

	slot := phase0.Slot(epoch*spec.SlotsPerEpoch + (spec.SlotsPerEpoch - 1))
	epochCommittees, err := s.Api.BeaconCommittees(context.Background(), strconv.Itoa(int(slot)))

	if err != nil {
		log.Errorf(err.Error())
	}

	validatorsAttSlot := make(map[phase0.ValidatorIndex]phase0.Slot) // each validator, when it had to attest
	validatorsPerSlot := make(map[phase0.Slot][]phase0.ValidatorIndex)

	for _, committee := range epochCommittees {
		for _, valID := range committee.Validators {
			validatorsAttSlot[valID] = committee.Slot

			if val, ok := validatorsPerSlot[committee.Slot]; ok {
				// the slot exists in the map
				validatorsPerSlot[committee.Slot] = append(val, valID)
			} else {
				// the slot does not exist, create
				validatorsPerSlot[committee.Slot] = []phase0.ValidatorIndex{valID}
			}
		}
	}

	proposerDuties, err := s.Api.ProposerDuties(context.Background(), phase0.Epoch(slot/spec.SlotsPerEpoch), nil)

	if err != nil {
		log.Errorf(err.Error())
	}

	return spec.EpochDuties{
		ProposerDuties:   proposerDuties,
		BeaconCommittees: epochCommittees,
		ValidatorAttSlot: validatorsAttSlot,
	}
}
