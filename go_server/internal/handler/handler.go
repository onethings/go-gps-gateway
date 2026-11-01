package handler

import (
	"fmt"
)

// Config is what the chain needs from the rest of the gateway. Assembly lives
// here rather than in cmd so that the order is decided in one place.
type Config struct {
	// Alerts is the rule stage: the alert pipeline. Nil leaves the stage as a
	// placeholder.
	Alerts Processor
	// Store receives the position after the last stage. Nil ends the chain.
	Store Processor
	// Terminal replaces Store when set. It is how a deployment writes a
	// position together with the events the stages raised for it.
	Terminal Next
	// Stages replaces the placeholder of a stage by name. It is how a later
	// milestone fills in motion, geofence or an event handler without touching
	// the order.
	Stages map[string]PositionHandler
}

// New assembles the chain in the ProcessingHandler order.
func New(config Config) (*Chain, error) {
	for name := range config.Stages {
		if !declared(name) {
			return nil, fmt.Errorf("handler: stage %q is not declared in Order", name)
		}
	}

	stages := make([]PositionHandler, 0, len(Order))
	for _, name := range Order {
		if stage := config.Stages[name]; stage != nil {
			stages = append(stages, stage)
			continue
		}
		if name == StageBusiness && config.Alerts != nil {
			stages = append(stages, BusinessHandler{Stage: name, Processor: config.Alerts})
			continue
		}
		stages = append(stages, Placeholder{Stage: name})
	}

	terminal := config.Terminal
	if terminal == nil {
		terminal = StoreOf(config.Store)
	}
	return NewChain(terminal, stages...), nil
}

// declared reports whether name is one of the stage names in Order.
func declared(name string) bool {
	for _, stage := range Order {
		if stage == name {
			return true
		}
	}
	return false
}
