package durable

import "github.com/hazcod/blur/internal/storage/physical/bolt"

// chronicleProjectionWriter reuses small encrypted metadata reads during one
// closed hourly projection transaction. Source reverse links and calendar
// ancestors otherwise reread the same records for decoding and manifest updates.
// The existing admission cache bounds entries and bytes and excludes bodies.
// Every mutation invalidates its key before reaching the physical writer, so
// later reads and manifest updates always observe the latest transaction value.
type chronicleProjectionWriter struct {
	bolt.Writer
	cache chronicleAdmissionReader
}

func newChronicleProjectionWriter(writer bolt.Writer) *chronicleProjectionWriter {
	return &chronicleProjectionWriter{Writer: writer, cache: chronicleAdmissionReader{Reader: writer}}
}

func (w *chronicleProjectionWriter) Get(key []byte) ([]byte, bool, error) {
	return w.cache.Get(key)
}

func (w *chronicleProjectionWriter) invalidate(key []byte) {
	if entry, found := w.cache.entries[string(key)]; found {
		w.cache.bytes -= len(key) + len(entry.value)
		clear(entry.value)
		delete(w.cache.entries, string(key))
	}
}

func (w *chronicleProjectionWriter) Put(key, value []byte) error {
	w.invalidate(key)
	return w.Writer.Put(key, value)
}

func (w *chronicleProjectionWriter) Delete(key []byte) error {
	w.invalidate(key)
	return w.Writer.Delete(key)
}
