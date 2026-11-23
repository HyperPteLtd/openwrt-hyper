package subscription

func confirmedLoss(p Profile) bool {
	if p.Final == Reject {
		return true
	}
	for _, r := range p.Rules {
		if r.Target == Reject {
			return true
		}
	}
	return false
}
