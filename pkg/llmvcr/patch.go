package llmvcr

import (
	"errors"
	"fmt"
)

// Scenario defines a patching run from a base cassette to a target cassette.
type Scenario struct {
	BaseCassette   string // path to source cassette
	TargetCassette string // path to write patched cassette
	Patches        []Patch
}

// Patch pairs a request InteractionMatcher with a response/interaction Action.
type Patch struct {
	Match  InteractionMatcher
	Action Action
}

// ErrInvalidPatch is returned when a patch has a nil Match or Action.
var ErrInvalidPatch = errors.New("llmvcr: invalid patch")

// Execute loads the BaseCassette, applies the first matching Patch to each interaction, and saves the result to TargetCassette.
func (s *Scenario) Execute() error {
	for i, p := range s.Patches {
		if p.Match == nil {
			return fmt.Errorf("%w: patch %d has nil Match", ErrInvalidPatch, i)
		}
		if p.Action == nil {
			return fmt.Errorf("%w: patch %d has nil Action", ErrInvalidPatch, i)
		}
	}

	c, err := LoadCassette(s.BaseCassette)
	if err != nil {
		return fmt.Errorf("loading base cassette %q: %w", s.BaseCassette, err)
	}

	var newInteractions []Interaction

	for idx := range c.Interactions {
		interaction := &c.Interactions[idx]
		var appliedPatch *Patch
		for i, p := range s.Patches {
			if p.Match.Match(&interaction.Request) {
				appliedPatch = &s.Patches[i]
				break
			}
		}

		if appliedPatch == nil {
			newInteractions = append(newInteractions, *interaction)
			continue
		}

		// Apply the patch
		replacement, err := appliedPatch.Action.Apply(interaction, c.Interactions)
		if err != nil {
			return fmt.Errorf("applying action on interaction matching request %s: %w", interaction.Request.URL, err)
		}

		// Automatically sync content length and format for all replaced/injected responses
		syncAct := SyncContentLength()
		var syncedReplacement []Interaction
		for idx := range replacement {
			res, err := syncAct.Apply(&replacement[idx], c.Interactions)
			if err != nil {
				return fmt.Errorf("syncing content length: %w", err)
			}
			syncedReplacement = append(syncedReplacement, res...)
		}
		replacement = syncedReplacement

		newInteractions = append(newInteractions, replacement...)
	}

	c.Interactions = newInteractions
	if err := SaveCassette(s.TargetCassette, c); err != nil {
		return fmt.Errorf("saving target cassette %q: %w", s.TargetCassette, err)
	}

	return nil
}

// ExecuteAll executes a list of scenarios sequentially.
func ExecuteAll(scenarios []Scenario) error {
	for _, s := range scenarios {
		if err := s.Execute(); err != nil {
			return err
		}
	}
	return nil
}
