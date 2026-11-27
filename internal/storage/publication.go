package storage

// ReadPublicationFence guards a tiny installation of already-loaded results.
// Tokens belong to this open store instance. Methods never wait for a writer;
// callbacks must not access storage or call back into Client.
type ReadPublicationFence interface {
	BeginPublicationRead() (uint64, bool)
	PublishRead(epoch uint64, publish func()) bool
}

// VoiceReadPublicationFence binds the authenticated meeting archive, allowing
// unrelated foreground captures to continue during voice generation/readback.
// Voice additions, metadata changes, erasure and store closure invalidate it.
type VoiceReadPublicationFence interface {
	BeginVoicePublicationRead() (uint64, bool)
	PublishVoiceRead(epoch uint64, publish func()) bool
}
