package subscription

import (
	"errors"
	"slices"
)

type DownloadOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type DownloadGroup struct {
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Selected string           `json:"selected"`
	Total    int              `json:"total"`
	Options  []DownloadOption `json:"options"`
}

type DownloadChoice struct {
	Group  string `json:"group"`
	Member string `json:"member"`
}

func saveDownloadChoice(r *record, p DownloadChoice) error {
	if r.Suspended || r.LoginRequired || r.Revoked {
		return ErrNotReady
	}
	if p.Member == "" && p.Group == "" {
		r.DownloadScope = ""
		r.DownloadMembers = nil
	} else {
		offer := r.LastProvider
		if offer == nil || offer.DownloadGroup == nil || offer.DownloadGroup.ID != p.Group || !slices.ContainsFunc(offer.DownloadGroup.Options, func(o DownloadOption) bool { return o.ID == p.Member }) {
			return errors.New("invalid_provider_choice")
		}
		if r.DownloadScope != r.LastProviderScope {
			r.DownloadMembers = nil
		}
		r.DownloadScope = r.LastProviderScope
		if r.DownloadMembers == nil {
			r.DownloadMembers = map[string]string{}
		}
		if len(r.DownloadMembers) >= maxProviders && r.DownloadMembers[p.Group] == "" {
			return errors.New("provider_limit")
		}
		r.DownloadMembers[p.Group] = p.Member
	}
	r.Providers = nil
	r.Generation++
	return nil
}
