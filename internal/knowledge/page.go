package knowledge

// PageRequest bounds a stable entity listing by row count.
type PageRequest struct {
	Limit int
	After string
}
