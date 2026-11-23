package subscription

import "sort"

type Changes struct {
	Added          []Member `json:"added,omitempty"`
	Removed        []Member `json:"removed,omitempty"`
	Groups         []string `json:"affected_groups,omitempty"`
	InvalidTargets []string `json:"missing_active_targets,omitempty"`
}

func changes(before *Profile, after Profile, refs []string) Changes {
	old := map[string]Node{}
	newIDs := map[string]bool{}
	groups := map[string]string{}
	affected := map[string]bool{}
	if before != nil {
		for _, n := range before.Nodes {
			old[n.ID] = n
		}
		for _, g := range before.Groups {
			groups[g.ID] = digest(g)
		}
	}
	var c Changes
	for _, n := range after.Nodes {
		if _, ok := old[n.ID]; !ok {
			c.Added = append(c.Added, n.Summary())
		}
		newIDs[n.ID] = true
		delete(old, n.ID)
	}
	for _, n := range old {
		c.Removed = append(c.Removed, n.Summary())
	}
	sort.Slice(c.Removed, func(i, j int) bool { return c.Removed[i].ID < c.Removed[j].ID })
	for _, g := range after.Groups {
		newIDs[g.ID] = true
		if groups[g.ID] != digest(g) {
			affected[g.ID] = true
		}
		delete(groups, g.ID)
	}
	for id := range groups {
		affected[id] = true
	}
	for id := range affected {
		c.Groups = append(c.Groups, id)
	}
	sort.Strings(c.Groups)
	for _, id := range refs {
		if !newIDs[id] && id != Direct && id != Reject {
			c.InvalidTargets = append(c.InvalidTargets, id)
		}
	}
	return c
}
