package policy

import (
	"encoding/json"
	"testing"

	"github.com/cainiaocome/computer-management/internal/protocol"
)

func item(id, typ, class string) protocol.Item {
	return protocol.Item{
		ID:      id,
		Type:    typ,
		Class:   class,
		Enabled: true,
		Config:  json.RawMessage(`{}`),
	}
}

func TestValidateAcceptsWellFormedPolicy(t *testing.T) {
	items := []protocol.Item{
		item("ff", TypeFirefoxPolicy, string(Enforce)),
		item("pk", TypePackages, string(Enforce)),
		item("br", TypeBrowserDefault, string(Nudge)),
		item("mac", TypeMacConsent, string(BootstrapOnce)),
		item("inv", TypeInventory, string(ReportOnly)),
		item("usr", TypeUserAccount, string(Enforce)),
	}
	if err := Validate(items); err != nil {
		t.Fatalf("expected valid policy, got %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string][]protocol.Item{
		"duplicate id":  {item("a", TypeInventory, string(ReportOnly)), item("a", TypeInventory, string(ReportOnly))},
		"unknown type":  {item("a", "nope", string(ReportOnly))},
		"unknown class": {item("a", TypeInventory, "sometimes")},
		"class/type tier mismatch": {
			// browser.default must be nudge, not enforce: a root tier cannot apply it.
			item("a", TypeBrowserDefault, string(Enforce)),
		},
		"missing config": {{
			ID: "a", Type: TypeInventory, Class: string(ReportOnly), Enabled: true,
		}},
	}
	for name, items := range cases {
		if err := Validate(items); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestMergePrecedence(t *testing.T) {
	group := []protocol.Item{
		item("a", TypeInventory, string(ReportOnly)),
		item("b", TypeFile, string(Enforce)),
	}
	override := []protocol.Item{
		{ID: "b", Type: TypeFile, Class: string(Enforce), Enabled: false},
		item("c", TypeFirefoxPolicy, string(Enforce)),
	}
	out := Merge(group, override)
	if len(out) != 2 {
		t.Fatalf("want 2 items, got %d: %+v", len(out), out)
	}
	if out[0].ID != "a" || out[1].ID != "c" {
		t.Fatalf("unexpected order/content: %v %v", out[0].ID, out[1].ID)
	}
}

func TestClassTier(t *testing.T) {
	if Enforce.Tier() != TierRoot {
		t.Error("enforce must be root tier")
	}
	if ConvergeUser.Tier() != TierSession {
		t.Error("converge_user must be session tier")
	}
	if Nudge.Tier() != TierHuman || BootstrapOnce.Tier() != TierHuman {
		t.Error("nudge and bootstrap_once must need a human")
	}
}
