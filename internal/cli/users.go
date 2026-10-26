package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"nubilo/internal/app"
	"nubilo/internal/identity"
	"nubilo/internal/qr"
	"nubilo/internal/server"
)

// cliUser resolves --user (a username) or the default owner admin.
func cliUser(rt *app.Runtime, g global) (*identity.User, error) {
	ctx := context.Background()
	if g.user == "" {
		return rt.IDs.EnsureOwner(ctx)
	}
	u, err := rt.IDs.GetUserByName(ctx, g.user)
	if err != nil {
		return nil, fmt.Errorf("unknown user %q (see nubilo users list)", g.user)
	}
	if !u.Active() {
		return nil, fmt.Errorf("user %q is disabled", g.user)
	}
	return u, nil
}

func userNames(rt *app.Runtime) map[string]string {
	out := map[string]string{"": "-"}
	users, _ := rt.IDs.ListUsers(context.Background())
	for _, u := range users {
		out[u.ID] = u.Username
	}
	return out
}

// serverBaseURL is the URL devices should use: the public domain when set,
// else the listen address.
func serverBaseURL(rt *app.Runtime) string {
	return rt.Cfg.DeviceBaseURL()
}

func runUsers(g global, args []string) int {
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	dir, err := app.ResolveDataDir(g.dataDir)
	if err != nil {
		return fatal(err)
	}
	rt, err := app.Open(dir)
	if err != nil {
		return fatal(err)
	}
	defer rt.Close()
	ctx := context.Background()
	byName := func(name string) (*identity.User, error) {
		u, err := rt.IDs.GetUserByName(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("unknown user %q", name)
		}
		return u, nil
	}
	switch sub {
	case "list":
		users, err := rt.IDs.ListUsers(ctx)
		if err != nil {
			return fatal(err)
		}
		if g.jsonOut {
			return dumpJSON(users)
		}
		if len(users) == 0 {
			fmt.Println("(no users yet; create one with `nubilo users add` or `nubilo users invite --role admin`)")
			return 0
		}
		usage, _ := rt.Engine.UserUsage(ctx)
		for _, u := range users {
			us := usage[u.ID]
			fmt.Printf("%-16s %-7s %-8s %6d objects  %s  %s\n", u.Username, u.Role, u.Status, us.Objects, humanBytes(us.Bytes), u.DisplayName)
		}
		return 0
	case "add":
		fs := flag.NewFlagSet("users add", flag.ContinueOnError)
		name := fs.String("name", "", "display name")
		role := fs.String("role", "member", "admin or member")
		if err := fs.Parse(reorderFlags(args)); err != nil {
			return 2
		}
		if fs.NArg() != 1 {
			return fatal(fmt.Errorf("users add USERNAME [--name NAME] [--role admin|member]"))
		}
		u, err := rt.IDs.CreateUser(ctx, fs.Arg(0), *name, identity.UserRole(*role))
		if err != nil {
			return fatal(err)
		}
		rt.Audit.Event(ctx, "admin-local", "user.create", map[string]any{"user": u.ID})
		return printOut(g, u, fmt.Sprintf("created %s (%s)\n", u.Username, u.Role))
	case "disable", "enable":
		if len(args) != 1 {
			return fatal(fmt.Errorf("users %s USERNAME", sub))
		}
		u, err := byName(args[0])
		if err != nil {
			return fatal(err)
		}
		status := identity.UserActive
		if sub == "disable" {
			status = identity.UserDisabled
		}
		if err := rt.IDs.SetUserStatus(ctx, u.ID, status); err != nil {
			return fatal(err)
		}
		rt.Audit.Event(ctx, "admin-local", "user.update", map[string]any{"user": u.ID, "status": status})
		if sub == "disable" {
			fmt.Printf("disabled %s (devices and app passwords revoked)\n", u.Username)
		} else {
			fmt.Printf("enabled %s (pair devices again; old ones stay revoked)\n", u.Username)
		}
		return 0
	case "role":
		if len(args) != 2 {
			return fatal(fmt.Errorf("users role USERNAME admin|member"))
		}
		u, err := byName(args[0])
		if err != nil {
			return fatal(err)
		}
		if err := rt.IDs.SetUserRole(ctx, u.ID, identity.UserRole(args[1])); err != nil {
			return fatal(err)
		}
		fmt.Printf("%s is now %s\n", u.Username, args[1])
		return 0
	case "invite":
		fs := flag.NewFlagSet("users invite", flag.ContinueOnError)
		role := fs.String("role", "member", "role of the new user")
		forUser := fs.String("for", "", "pair a device for this existing user instead of creating one")
		ttl := fs.Int("ttl", 24, "hours until the invite expires")
		uses := fs.Int("uses", 1, "how many times the invite can be used")
		if err := fs.Parse(args); err != nil {
			return 2
		}
		forID := ""
		if *forUser != "" {
			u, err := byName(*forUser)
			if err != nil {
				return fatal(err)
			}
			forID = u.ID
		}
		code, inv, err := rt.IDs.CreateInvite(ctx, "admin-local", identity.UserRole(*role), forID, time.Duration(*ttl)*time.Hour, *uses)
		if err != nil {
			return fatal(err)
		}
		return printInvite(g, rt, code, inv)
	default:
		return fatal(fmt.Errorf("unknown users subcommand %s", sub))
	}
}

func printInvite(g global, rt *app.Runtime, code string, inv *identity.Invite) int {
	base := serverBaseURL(rt)
	setup := server.SetupURL(base, code, b64url(rt.ServerPub))
	if g.jsonOut {
		return dumpJSON(map[string]any{
			"id": inv.ID, "code": code, "url": server.InviteURL(base, code), "setup_url": setup,
			"expires_at": inv.ExpiresAt, "role": inv.Role, "user_id": inv.UserID,
		})
	}
	if text, err := qr.Terminal(setup); err == nil {
		fmt.Print(text)
	}
	fmt.Printf("Scan with the Nubilo app, or open: %s\n", server.InviteURL(base, code))
	fmt.Printf("Invite code: %s\nRole: %s  Uses: %d  Expires: %s\n", code, inv.Role, inv.MaxUses, time.UnixMilli(inv.ExpiresAt).Format(time.RFC3339))
	if rt.Cfg.Public.Domain == "" {
		fmt.Fprintln(os.Stderr, "note: no public domain configured; phones must reach "+base+" (run `nubilo setup` to configure a domain)")
	}
	return 0
}

// reorderFlags moves flags before positional arguments so `add NAME --role x` parses.
func reorderFlags(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			if i+1 < len(args) && !containsEq(a) && len(args[i+1]) > 0 && args[i+1][0] != '-' {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

func containsEq(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return true
		}
	}
	return false
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
