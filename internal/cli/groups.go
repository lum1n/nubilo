package cli

import (
	"context"
	"fmt"
	"strings"

	"nubilo/internal/app"
	"nubilo/internal/identity"
)

func runGroups(g global, args []string) int {
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
	userIDs := func(names []string) ([]string, error) {
		out := []string{}
		for _, n := range names {
			u, err := rt.IDs.GetUserByName(ctx, n)
			if err != nil {
				return nil, fmt.Errorf("unknown user %q", n)
			}
			out = append(out, u.ID)
		}
		return out, nil
	}
	byName := func(name string) (*identity.Group, error) {
		gr, err := rt.IDs.GetGroupByName(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("unknown group %q", name)
		}
		return gr, nil
	}
	switch sub {
	case "list":
		groups, err := rt.IDs.ListGroups(ctx)
		if err != nil {
			return fatal(err)
		}
		if g.jsonOut {
			return dumpJSON(groups)
		}
		if len(groups) == 0 {
			fmt.Println("(no groups yet; create one with `nubilo groups add NAME USERNAME...`)")
			return 0
		}
		names := userNames(rt)
		for _, gr := range groups {
			members := make([]string, len(gr.Members))
			for i, id := range gr.Members {
				members[i] = names[id]
			}
			fmt.Printf("%-20s %s\n", gr.Name, strings.Join(members, ", "))
		}
		return 0
	case "add":
		if len(args) < 1 {
			return fatal(fmt.Errorf("groups add NAME [USERNAME...]"))
		}
		ids, err := userIDs(args[1:])
		if err != nil {
			return fatal(err)
		}
		gr, err := rt.IDs.CreateGroup(ctx, args[0], ids)
		if err != nil {
			return fatal(err)
		}
		rt.Audit.Event(ctx, "admin-local", "group.create", map[string]any{"group": gr.ID, "members": gr.Members})
		return printOut(g, gr, fmt.Sprintf("created group %s with %d member(s)\n", gr.Name, len(gr.Members)))
	case "members":
		if len(args) < 1 {
			return fatal(fmt.Errorf("groups members NAME [USERNAME...]"))
		}
		gr, err := byName(args[0])
		if err != nil {
			return fatal(err)
		}
		ids, err := userIDs(args[1:])
		if err != nil {
			return fatal(err)
		}
		gr, err = rt.IDs.UpdateGroup(ctx, gr.ID, "", ids)
		if err != nil {
			return fatal(err)
		}
		rt.Audit.Event(ctx, "admin-local", "group.update", map[string]any{"group": gr.ID, "members": gr.Members})
		return printOut(g, gr, fmt.Sprintf("%s now has %d member(s)\n", gr.Name, len(gr.Members)))
	case "rename":
		if len(args) != 2 {
			return fatal(fmt.Errorf("groups rename NAME NEWNAME"))
		}
		gr, err := byName(args[0])
		if err != nil {
			return fatal(err)
		}
		gr, err = rt.IDs.UpdateGroup(ctx, gr.ID, args[1], nil)
		if err != nil {
			return fatal(err)
		}
		return printOut(g, gr, fmt.Sprintf("renamed to %s\n", gr.Name))
	case "delete":
		if len(args) != 1 {
			return fatal(fmt.Errorf("groups delete NAME"))
		}
		gr, err := byName(args[0])
		if err != nil {
			return fatal(err)
		}
		if err := rt.IDs.DeleteGroup(ctx, gr.ID); err != nil {
			return fatal(err)
		}
		rt.Audit.Event(ctx, "admin-local", "group.delete", map[string]any{"group": gr.ID})
		fmt.Printf("deleted group %s and its shares\n", gr.Name)
		return 0
	}
	return fatal(fmt.Errorf("unknown groups command %q (list, add, members, rename, delete)", sub))
}
