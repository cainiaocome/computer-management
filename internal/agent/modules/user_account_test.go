package modules

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cainiaocome/computer-management/internal/policy"
	"github.com/cainiaocome/computer-management/internal/protocol"
)

func userItem(t *testing.T, cfg string) protocol.Item {
	t.Helper()
	return protocol.Item{ID: "u", Type: policy.TypeUserAccount, Class: string(policy.Enforce),
		Enabled: true, Config: json.RawMessage(cfg)}
}

func newUserRunner(isAdmin bool) *fakeRunner {
	return &fakeRunner{handler: func(name string, args []string) (string, error) {
		switch {
		case name == "id" && len(args) == 2 && args[0] == "-u":
			return "501\n", nil
		case name == "id" && len(args) == 2 && args[0] == "-nG":
			if isAdmin {
				return "staff sudo\n", nil
			}
			return "staff\n", nil
		case name == "gpasswd":
			isAdmin = false
			return "", nil
		}
		return "", errors.New("unexpected command")
	}}
}

func TestUserAccountCompliantWhenStandard(t *testing.T) {
	e := testEnv("linux", t.TempDir(), newUserRunner(false))
	out := UserAccount{}.Apply(ctxWith(e), userItem(t, `{"username":"mom"}`))
	if out.Status != protocol.StatusCompliant {
		t.Fatalf("want compliant, got %s (%s)", out.Status, out.Message)
	}
}

func TestUserAccountReportsAdminWithoutDemote(t *testing.T) {
	r := newUserRunner(true)
	e := testEnv("linux", t.TempDir(), r)
	out := UserAccount{}.Apply(ctxWith(e), userItem(t, `{"username":"mom"}`))
	if out.Status != protocol.StatusDrifted {
		t.Fatalf("want drifted, got %s (%s)", out.Status, out.Message)
	}
	if !strings.Contains(out.Message, "demote") {
		t.Fatalf("message should explain the opt-in: %q", out.Message)
	}
	if r.called(t, "gpasswd") {
		t.Fatal("must not demote without explicit opt-in")
	}
}

func TestUserAccountDemotesWhenAsked(t *testing.T) {
	r := newUserRunner(true)
	e := testEnv("linux", t.TempDir(), r)
	out := UserAccount{}.Apply(ctxWith(e), userItem(t, `{"username":"mom","demote":true}`))
	if out.Status != protocol.StatusChanged {
		t.Fatalf("want changed, got %s (%s)", out.Status, out.Message)
	}
	if !r.called(t, "gpasswd", "-d", "mom") {
		t.Fatalf("gpasswd not called: %v", r.calls)
	}
}

func TestUserAccountMissingUser(t *testing.T) {
	r := &fakeRunner{handler: func(name string, args []string) (string, error) {
		return "", errors.New("no such user")
	}}
	e := testEnv("linux", t.TempDir(), r)
	out := UserAccount{}.Apply(ctxWith(e), userItem(t, `{"username":"mom"}`))
	if out.Status != protocol.StatusActionRequired {
		t.Fatalf("want action_required, got %s", out.Status)
	}
}

func TestContainsUserLine(t *testing.T) {
	if !containsUserLine("admin\nmom\n", "mom") {
		t.Error("should find user on its own line")
	}
	if containsUserLine("admin\nmomm\n", "mom") {
		t.Error("must not prefix-match")
	}
}

func TestMacConsentIsUnsupportedOffMac(t *testing.T) {
	e := testEnv("linux", t.TempDir(), &fakeRunner{})
	item := protocol.Item{ID: "c", Type: policy.TypeMacConsent, Class: string(policy.BootstrapOnce),
		Enabled: true, Config: json.RawMessage(`{}`)}
	out := MacConsent{}.Apply(ctxWith(e), item)
	if out.Status != protocol.StatusUnsupported {
		t.Fatalf("want unsupported, got %s", out.Status)
	}
}

func TestContainsUserLineCaseAndDomainPrefix(t *testing.T) {
	// Real "net localgroup Administrators" output shape, members listed between
	// the rule lines, one per line, in stored case.
	windowsOut := `
Alias name     Administrators
Comment        Administrators have complete and unrestricted access



Members

-------------------------------------------------------------------------------
Administrator
jean-luc
AzureAD\Alice
The command completed successfully.
`
	for _, u := range []string{"jean-luc", "Alice", "AzureAD\\Alice", "administrator"} {
		if !containsUserLine(windowsOut, u) {
			t.Fatalf("%q should be recognised as an administrator", u)
		}
	}
	// A prefix of a real member must not match: this is how "alice" used to
	// match "jean-luc" via the hyphen split, or "luc" match at all.
	for _, u := range []string{"jean", "luc", "Ali"} {
		if containsUserLine(windowsOut, u) {
			t.Fatalf("%q must not match", u)
		}
	}
}

func TestUserIsAdminReadsTheAnswerNotTheTokens(t *testing.T) {
	cases := []struct {
		out  string
		want bool
	}{
		{"yes jean-luc is a member of admin\n", true},
		{"no jean-luc is NOT a member of admin\n", false},
	}
	for _, c := range cases {
		r := &fakeRunner{handler: func(name string, args []string) (string, error) {
			return c.out, nil
		}}
		e := testEnv("darwin", t.TempDir(), r)
		got, err := userIsAdmin(context.Background(), e, "jean-luc")
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Fatalf("%q: got %v, want %v", strings.TrimSpace(c.out), got, c.want)
		}
	}
}
