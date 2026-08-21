package localfs

// Space reports capacity of the volume that holds a path, for the Deck
// view's destination gauge.
type Space struct {
	Free  int64 `json:"free"`
	Total int64 `json:"total"`
}
