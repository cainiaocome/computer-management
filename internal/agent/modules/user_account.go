package modules

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cainiaocome/computer-management/internal/policy"
	"github.com/cainiaocome/computer-management/internal/protocol"
)

// UserAccountConfig is the config of a "user.account" item.
//
// The daily user must NOT be a local administrator: that is what makes the rest
// of the policy durable (browser policies, TCC grants, the agent itself). This
// module reports admin membership, and only demotes when explicitly asked.
type UserAccountConfig struct {
	Username       string `json:"username"`
	EnsureStandard *bool  `json:"ensure_standard"`
	// Demote must be true to actually remove the user from the admin group.
	// Default false keeps the module report-only in practice.
	Demote bool `json:"demote"`
}

// UserAccount enforces "the daily user is a standard user".
type UserAccount struct{}

func (UserAccount) Type() string { return policy.TypeUserAccount }

func (UserAccount) Apply(ctx context.Context, item protocol.Item) Outcome {
	e := env(ctx)
	var cfg UserAccountConfig
	if err := json.Unmarshal(item.Config, &cfg); err != nil {
		return failed(fmt.Errorf("decode config: %w", err))
	}
	if cfg.Username == "" {
		return failed(fmt.Errorf("username is required"))
	}
	ensureStandard := cfg.EnsureStandard == nil || *cfg.EnsureStandard

	exists, err := userExists(ctx, e, cfg.Username)
	if err != nil {
		return failed(err)
	}
	detail := map[string]any{"username": cfg.Username, "exists": exists}
	if !exists {
		return Outcome{
			Status:  protocol.StatusActionRequired,
			Message: fmt.Sprintf("user %q does not exist; create it as a standard account", cfg.Username),
			Detail:  detail,
		}
	}

	isAdmin, err := userIsAdmin(ctx, e, cfg.Username)
	if err != nil {
		return failed(err)
	}
	detail["admin"] = isAdmin

	if !ensureStandard || !isAdmin {
		return Outcome{Status: protocol.StatusCompliant, Message: fmt.Sprintf("%q is a standard user", cfg.Username), Detail: detail}
	}

	if !cfg.Demote {
		return Outcome{
			Status:  protocol.StatusDrifted,
			Message: fmt.Sprintf("%q is a local administrator (set \"demote\": true to fix)", cfg.Username),
			Detail:  detail,
		}
	}
	if e.DryRun {
		return Outcome{Status: protocol.StatusDrifted, Message: fmt.Sprintf("would remove %q from administrators", cfg.Username), Detail: detail}
	}
	if err := demoteUser(ctx, e, cfg.Username); err != nil {
		return failed(err)
	}
	admin, err := userIsAdmin(ctx, e, cfg.Username)
	if err != nil {
		// Do not claim success when the verification itself failed.
		return failed(fmt.Errorf("could not verify that %q was demoted: %w", cfg.Username, err))
	}
	if admin {
		return Outcome{Status: protocol.StatusError, Message: fmt.Sprintf("could not demote %q", cfg.Username), Detail: detail}
	}
	return Outcome{Status: protocol.StatusChanged, Message: fmt.Sprintf("%q is now a standard user", cfg.Username), Detail: detail}
}

func userExists(ctx context.Context, e *Env, user string) (bool, error) {
	if e.GOOS == "windows" {
		out, err := e.Run.Run(ctx, "net", "user", user)
		return err == nil && !strings.Contains(out, "could not be found"), nil
	}
	out, err := e.Run.Run(ctx, "id", "-u", user)
	return err == nil && strings.TrimSpace(out) != "", nil
}

func userIsAdmin(ctx context.Context, e *Env, user string) (bool, error) {
	switch e.GOOS {
	case "windows":
		out, err := e.Run.Run(ctx, "net", "localgroup", "Administrators")
		if err != nil {
			return false, fmt.Errorf("net localgroup: %w", err)
		}
		return containsUserLine(out, user), nil
	case "darwin":
		// checkmember follows nested groups, which a flat GroupMembership read
		// does not, and its answer is unambiguous ("yes"/"no").
		out, err := e.Run.Run(ctx, "dseditgroup", "-o", "checkmember", "-m", user, "admin")
		answer := strings.ToLower(strings.TrimSpace(out))
		switch {
		case strings.HasPrefix(answer, "yes"):
			return true, nil
		case strings.HasPrefix(answer, "no"):
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("dseditgroup -o checkmember: %w", err)
		}
		return false, fmt.Errorf("unexpected dseditgroup output: %q", strings.TrimSpace(out))
	default:
		out, err := e.Run.Run(ctx, "id", "-nG", user)
		if err != nil {
			return false, fmt.Errorf("id -nG: %w", err)
		}
		for _, g := range strings.Fields(out) {
			switch g {
			case "sudo", "wheel", "admin":
				return true, nil
			}
		}
		return false, nil
	}
}

func demoteUser(ctx context.Context, e *Env, user string) error {
	switch e.GOOS {
	case "windows":
		_, err := e.Run.Run(ctx, "net", "localgroup", "Administrators", user, "/delete")
		return err
	case "darwin":
		_, err := e.Run.Run(ctx, "dscl", ".", "-delete", "/Groups/admin", "GroupMembership", user)
		return err
	default:
		for _, g := range []string{"sudo", "wheel", "admin"} {
			if _, err := e.Run.Run(ctx, "gpasswd", "-d", user, g); err == nil {
				return nil
			}
		}
		return fmt.Errorf("could not remove %q from any admin group", user)
	}
}

// containsUserLine reports whether user appears in a group listing.
//
// Names are compared case-insensitively and with any DOMAIN\ or BUILTIN\ prefix
// removed, because that is how Windows prints members. Hyphens are NOT
// separators: a user called "jean-luc" must not be split into "jean" and "luc",
// which previously made such users look like standard users while they were
// administrators.
func containsUserLine(out, user string) bool {
	want := bareUser(user)
	for _, f := range strings.FieldsFunc(out, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == ',' || r == ';'
	}) {
		if strings.EqualFold(bareUser(f), want) {
			return true
		}
	}
	return false
}

// bareUser strips a DOMAIN\ or BUILTIN\ prefix and surrounding punctuation.
func bareUser(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexAny(s, `\`); i >= 0 {
		s = s[i+1:]
	}
	return strings.Trim(s, `"';,()`)
}
