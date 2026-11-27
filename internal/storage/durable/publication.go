package durable

// BeginPublicationRead captures the committed-write epoch without storage IO.
// All updateLocked callers hold state.mu before taking publicationMu.
func (s *Store) BeginPublicationRead() (uint64, bool) {
	return s.beginPublicationRead(false)
}

func (s *Store) BeginVoicePublicationRead() (uint64, bool) {
	return s.beginPublicationRead(true)
}

func (s *Store) beginPublicationRead(voice bool) (uint64, bool) {
	if s == nil || s.state == nil || !s.state.mu.TryRLock() {
		return 0, false
	}
	defer s.state.mu.RUnlock()
	if s.state.closed || s.state.deletionPending.Load() || !s.state.publicationMu.TryRLock() {
		return 0, false
	}
	defer s.state.publicationMu.RUnlock()
	if voice {
		return s.state.voicePublicationEpoch, true
	}
	return s.state.publicationEpoch, true
}

// PublishRead serializes only the result assignment with durable commits.
// Conservatively reject any intervening committed write, including mutations
// made by background workers without going through the Client deletion fence.
func (s *Store) PublishRead(epoch uint64, publish func()) bool {
	return s.publishRead(epoch, false, publish)
}

func (s *Store) PublishVoiceRead(epoch uint64, publish func()) bool {
	return s.publishRead(epoch, true, publish)
}

func (s *Store) publishRead(epoch uint64, voice bool, publish func()) bool {
	if s == nil || s.state == nil || publish == nil || !s.state.mu.TryRLock() {
		return false
	}
	defer s.state.mu.RUnlock()
	if s.state.closed || s.state.deletionPending.Load() || !s.state.publicationMu.TryRLock() {
		return false
	}
	defer s.state.publicationMu.RUnlock()
	current := s.state.publicationEpoch
	if voice {
		current = s.state.voicePublicationEpoch
	}
	if current != epoch {
		return false
	}
	publish()
	return true
}
