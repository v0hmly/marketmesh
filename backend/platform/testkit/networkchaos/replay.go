package networkchaos

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const replaySchemaVersion = "marketmesh.network-chaos-replay/v1"

// WriteReplayManifest сохраняет seed и фактически выполненную ordered fault
// sequence. Immutable Docker IDs намеренно исключены: новый запуск обязан
// заново разрешить точные disposable resources через Driver.Inspect.
func WriteReplayManifest(
	destination io.Writer,
	config Config,
	plan Plan,
) error {
	if destination == nil {
		return errors.New("networkchaos: replay destination must not be nil")
	}
	if err := validateConfig(config); err != nil {
		return err
	}
	if err := validatePlan(config, plan); err != nil {
		return err
	}

	manifest := replayManifest{
		SchemaVersion: replaySchemaVersion,
		RunID:         config.RunID,
		Seed:          plan.Seed,
		Steps:         make([]replayStep, 0, len(plan.Steps)),
	}
	for stepIndex, step := range plan.Steps {
		replayStep := replayStep{
			Index:           stepIndex,
			Name:            step.Name,
			HoldNanoseconds: step.Hold.Nanoseconds(),
			Faults:          make([]replayFault, 0, len(step.Faults)),
		}
		for faultIndex, fault := range step.Faults {
			peerNetworks := make([]string, 0, len(fault.PeerNetworks))
			for _, peer := range fault.PeerNetworks {
				peerNetworks = append(peerNetworks, peer.Name)
			}
			replayStep.Faults = append(replayStep.Faults, replayFault{
				Index:         faultIndex,
				Name:          fault.Name,
				Kind:          fault.Kind,
				Container:     fault.Container.Name,
				Network:       fault.Network.Name,
				PeerNetworks:  peerNetworks,
				Interface:     fault.Interface,
				DelayMicros:   fault.Delay.Microseconds(),
				JitterMicros:  fault.Jitter.Microseconds(),
				LossPercent:   fault.LossPercent,
				BandwidthKbit: fault.BandwidthKbit,
				CapacityLoss:  fault.CapacityLoss,
			})
		}
		manifest.Steps = append(manifest.Steps, replayStep)
	}

	encoder := json.NewEncoder(destination)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		return fmt.Errorf("networkchaos: writing replay manifest: %w", err)
	}

	return nil
}

type replayManifest struct {
	SchemaVersion string       `json:"schema_version"`
	RunID         string       `json:"run_id"`
	Seed          int64        `json:"seed"`
	Steps         []replayStep `json:"steps"`
}

type replayStep struct {
	Index           int           `json:"index"`
	Name            string        `json:"name"`
	HoldNanoseconds int64         `json:"hold_nanoseconds"`
	Faults          []replayFault `json:"faults"`
}

type replayFault struct {
	Index         int      `json:"index"`
	Name          string   `json:"name"`
	Kind          Kind     `json:"kind"`
	Container     string   `json:"container"`
	Network       string   `json:"network"`
	PeerNetworks  []string `json:"peer_networks"`
	Interface     string   `json:"interface"`
	DelayMicros   int64    `json:"delay_microseconds"`
	JitterMicros  int64    `json:"jitter_microseconds"`
	LossPercent   float64  `json:"loss_percent"`
	BandwidthKbit uint32   `json:"bandwidth_kbit"`
	CapacityLoss  uint     `json:"capacity_loss"`
}
