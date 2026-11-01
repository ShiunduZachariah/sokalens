package versions

func nextState(before State, action Action, candidate *Artifact) (State, error) {
	after := before
	switch action {
	case SetCandidate:
		if candidate == nil || candidate.ID <= 0 {
			return State{}, ErrMissingCandidate
		}
		if sameArtifact(before.Current, candidate) || sameArtifact(before.Candidate, candidate) {
			return State{}, ErrNoStateChange
		}
		after.Candidate = cloneArtifact(candidate)
	case PromoteCandidate:
		if before.Candidate == nil {
			return State{}, ErrMissingCandidate
		}
		after.Previous = cloneArtifact(before.Current)
		after.Current = cloneArtifact(before.Candidate)
		after.Candidate = nil
		// Bootstrap is immediately recoverable. Later promotions preserve the
		// prior LKG until an explicit successful validation marks the new one.
		if after.LastKnownGood == nil {
			after.LastKnownGood = cloneArtifact(after.Current)
		}
	case MarkLKG:
		if before.Current == nil {
			return State{}, ErrMissingLKG
		}
		if sameArtifact(before.Current, before.LastKnownGood) {
			return State{}, ErrNoStateChange
		}
		after.LastKnownGood = cloneArtifact(before.Current)
	case RollbackToLKG:
		if before.LastKnownGood == nil {
			return State{}, ErrMissingLKG
		}
		if sameArtifact(before.Current, before.LastKnownGood) {
			return State{}, ErrNoStateChange
		}
		after.Previous = cloneArtifact(before.Current)
		after.Current = cloneArtifact(before.LastKnownGood)
		after.Candidate = nil
	default:
		return State{}, ErrInvalidCommand
	}
	after.Revision = before.Revision + 1
	return after, nil
}

func sameArtifact(left, right *Artifact) bool {
	return left != nil && right != nil && left.ID == right.ID
}

func cloneArtifact(value *Artifact) *Artifact {
	if value == nil {
		return nil
	}
	copy := *value
	if value.Metadata != nil {
		copy.Metadata = make(map[string]string, len(value.Metadata))
		for key, item := range value.Metadata {
			copy.Metadata[key] = item
		}
	}
	return &copy
}
