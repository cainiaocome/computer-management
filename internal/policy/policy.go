// Package policy holds the policy vocabulary shared by the server (validation,
// resolution) and the agent (module dispatch).
package policy

import (
	"fmt"

	"github.com/cainiaocome/computer-management/internal/protocol"
)

// Class describes how a policy item is enforced. See docs/design.md §4.1.
type Class string

const (
	// Enforce is fully controllable by the root/SYSTEM tier; drift is reverted.
	Enforce Class = "enforce"
	// ConvergeUser is applied by the per-user session tier.
	ConvergeUser Class = "converge_user"
	// Nudge cannot be applied programmatically (identity gate); detect and prompt.
	Nudge Class = "nudge"
	// BootstrapOnce needs a human GUI step (consent gate).
	BootstrapOnce Class = "bootstrap_once"
	// ReportOnly observes without changing anything.
	ReportOnly Class = "report_only"
)

// Valid reports whether c is a known class.
func (c Class) Valid() bool {
	switch c {
	case Enforce, ConvergeUser, Nudge, BootstrapOnce, ReportOnly:
		return true
	}
	return false
}

// Tier is the execution tier that owns a class.
type Tier string

const (
	TierRoot    Tier = "root"
	TierSession Tier = "session"
	TierHuman   Tier = "human"
)

// Tier maps a class to the tier that must apply it.
func (c Class) Tier() Tier {
	switch c {
	case Enforce:
		return TierRoot
	case ConvergeUser:
		return TierSession
	case Nudge, BootstrapOnce:
		return TierHuman
	default:
		return TierSession
	}
}

// Module types. Kept in one place so validation and the agent registry agree.
const (
	TypeFirefoxPolicy  = "firefox.policy"
	TypePackages       = "packages"
	TypeFile           = "file"
	TypeBrowserDefault = "browser.default"
	TypeMacConsent     = "macos.consent"
	TypeUserAccount    = "user.account"
	TypeInventory      = "inventory"
)

// TypeTier is the only tier allowed to own a module type.
var TypeTier = map[string]Tier{
	TypeFirefoxPolicy:  TierRoot,
	TypePackages:       TierRoot,
	TypeFile:           TierRoot,
	TypeBrowserDefault: TierHuman,
	TypeMacConsent:     TierHuman,
	TypeUserAccount:    TierRoot,
	TypeInventory:      TierSession,
}

// Validate checks a bundle's items for internal consistency.
func Validate(items []protocol.Item) error {
	seen := make(map[string]struct{}, len(items))
	for i, it := range items {
		if it.ID == "" {
			return fmt.Errorf("policy item %d: id is required", i)
		}
		if _, dup := seen[it.ID]; dup {
			return fmt.Errorf("policy item %q: duplicate id", it.ID)
		}
		seen[it.ID] = struct{}{}

		if it.Type == "" {
			return fmt.Errorf("policy item %q: type is required", it.ID)
		}
		if _, ok := TypeTier[it.Type]; !ok {
			return fmt.Errorf("policy item %q: unknown type %q", it.ID, it.Type)
		}
		c := Class(it.Class)
		if !c.Valid() {
			return fmt.Errorf("policy item %q: unknown class %q", it.ID, it.Class)
		}
		if got, want := TypeTier[it.Type], c.Tier(); got != want {
			return fmt.Errorf("policy item %q: type %q must use tier %q, class %q maps to tier %q",
				it.ID, it.Type, got, it.Class, want)
		}
		if it.Enabled && len(it.Config) == 0 {
			return fmt.Errorf("policy item %q: config is required when enabled", it.ID)
		}
	}
	return nil
}

// Merge resolves a group policy and a device override into a flat item list.
// Device overrides win by ID; an override with Enabled=false removes the item.
func Merge(group, override []protocol.Item) []protocol.Item {
	byID := make(map[string]protocol.Item, len(group)+len(override))
	order := make([]string, 0, len(group)+len(override))
	for _, it := range group {
		if _, ok := byID[it.ID]; !ok {
			order = append(order, it.ID)
		}
		byID[it.ID] = it
	}
	for _, it := range override {
		if _, ok := byID[it.ID]; !ok {
			order = append(order, it.ID)
		}
		byID[it.ID] = it
	}
	out := make([]protocol.Item, 0, len(order))
	for _, id := range order {
		it := byID[id]
		if !it.Enabled {
			continue
		}
		out = append(out, it)
	}
	return out
}
