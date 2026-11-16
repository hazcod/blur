package client

import (
	"fmt"

	"github.com/hazcod/blur/internal/semantic"
)

func memoryIndexStatusCopy(status semantic.Status) string {
	coverage := fmt.Sprintf("%d sources indexed", status.IndexedSources)
	switch status.State {
	case "disabled":
		return "Off. Exact search uses its own local index."
	case "ready":
		return "Ready. " + coverage + "."
	case "loading":
		return "Opening encrypted search index. " + coverage + "."
	case "indexing":
		return "Indexing saved memory in the background. " + coverage + "."
	case "paused":
		return "Indexing paused by power settings. " + coverage + "; saved matches remain searchable."
	}
	switch status.ErrorCode {
	case "model_missing":
		return "The embedding model is not installed in Ollama. Download it, then retry."
	case "local_model_required":
		return "Select a local embedding model. Ollama cloud models cannot index your memory."
	case "embedding_model_required":
		return "This model does not support embeddings. Select an embedding model such as embeddinggemma."
	case "model_changed":
		return "The model changed. Blur is rebuilding compatible search data. Exact search uses its own local index."
	case "settings_invalid":
		return "Saved memory search settings need repair. Save a local endpoint and embedding model."
	case "memory_changing":
		return "Memory search is refreshing after a change."
	default:
		return "Search by meaning is unavailable. Start local Ollama and check the installed embedding model. Only exact-word matches are available until it is ready."
	}
}
