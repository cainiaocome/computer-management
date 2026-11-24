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
	if admin, err := userIsAdmin(ctx, e, cfg.Username); err == nil && admin {
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
		out, err := e.Run.Run(ctx, "dscl", ".", "-read", "/Groups/admin", "GroupMembership")
		if err != nil {
			return false, fmt.Errorf("dscl: %w", err)
		}
		return containsUserLine(out, user), nil
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

// containsUserLine reports whether user appears as a whitespace-separated token.
func containsUserLine(out, user string) bool {
	for _, f := range strings.FieldsFunc(out, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == ',' || r == ':' || r == '-'
	}) {
		if f == user {
			return true
		}
	}
	return false
}
