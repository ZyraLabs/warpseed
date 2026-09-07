package queue

import (
	"encoding/json"
	"fmt"
)

// The four things warpseed can do when a transfer's destination already
// exists. Every one of them is decided BEFORE any bytes move — the whole
// point of this machinery is that the old behaviour re-transferred a 50 GB
// file and only then discovered it was replacing something.
const (
	ActionAsk       = "ask"       // hold the row until the user chooses
	ActionOverwrite = "overwrite" // transfer and replace
	ActionSkip      = "skip"      // do not transfer at all
	ActionRename    = "rename"    // transfer to a free name beside it
)

// ValidActions is the set accepted from settings and from the UI.
var ValidActions = map[string]bool{
	ActionAsk: true, ActionOverwrite: true, ActionSkip: true, ActionRename: true,
}

// Conflict kinds, described from the point of view of the file being
// transferred ("incoming") against the one already at the destination
// ("existing"). Deliberately direction-neutral: an upload's existing file is
// on the server and a download's is on your disk, but the decision has the
// same shape either way.
const (
	KindIdentical   = "identical"    // same size, same timestamp
	KindNewerLarger = "newer_larger" // incoming is newer AND bigger
	KindSmaller     = "smaller"      // incoming is smaller
	KindOlder       = "older"        // incoming is older
	KindOther       = "other"        // anything the rules above do not name
)

// ConflictSettingKeys lists every policy setting, for validation and for the
// Settings UI to render in a stable order.
var ConflictSettingKeys = []struct{ Kind, Key string }{
	{KindNewerLarger, "transfers.conflict_newer_larger"},
	{KindSmaller, "transfers.conflict_smaller"},
	{KindOlder, "transfers.conflict_older"},
	{KindIdentical, "transfers.conflict_identical"},
	{KindOther, "transfers.conflict_other"},
}

// settingForKind maps a conflict to the setting that decides it.
var settingForKind = func() map[string]string {
	m := make(map[string]string, len(ConflictSettingKeys))
	for _, k := range ConflictSettingKeys {
		m[k.Kind] = k.Key
	}
	return m
}()

// DefaultConflictActions is the out-of-the-box policy: take an obviously
// better copy without asking, skip one that is already identical, and ask
// about anything that could be a downgrade. Nothing is replaced silently
// unless the incoming file is both newer and bigger.
var DefaultConflictActions = map[string]string{
	KindIdentical:   ActionSkip,
	KindNewerLarger: ActionOverwrite,
	KindSmaller:     ActionAsk,
	KindOlder:       ActionAsk,
	KindOther:       ActionAsk,
}

// FileFacts is one side of a comparison. Mtime is a Unix second count; 0
// means unknown, which is treated as "cannot claim these match".
type FileFacts struct {
	Size  int64 `json:"size"`
	Mtime int64 `json:"mtime"`
}

// Conflict is the full record of a clash, stored as JSON on the transfer row
// so the queue can explain itself ("the copy you have is newer, 2.1 GB
// against 1.8 GB") without re-statting anything.
type Conflict struct {
	Kind     string    `json:"kind"`
	Incoming FileFacts `json:"incoming"`
	Existing FileFacts `json:"existing"`
}

// ClassifyConflict names the relationship between the incoming file and the
// one already at the destination.
//
// Order matters. Identical is checked first because same-size-same-time is
// the one case where doing nothing is certainly right. "Newer and larger" is
// next because it is the only combination confident enough to replace
// without asking — a bigger file with a later timestamp is a better copy of
// the same thing far more often than it is a mistake. Everything after that
// is a possible downgrade, and the default policy asks.
//
// An unknown timestamp on either side (0) never counts as a match and never
// proves an ordering, so a server that reports no mtime falls through to a
// size comparison rather than silently claiming the files are the same.
func ClassifyConflict(incoming, existing FileFacts) string {
	knownTimes := incoming.Mtime > 0 && existing.Mtime > 0
	if incoming.Size == existing.Size && knownTimes && incoming.Mtime == existing.Mtime {
		return KindIdentical
	}
	if knownTimes && incoming.Mtime > existing.Mtime && incoming.Size > existing.Size {
		return KindNewerLarger
	}
	if incoming.Size < existing.Size {
		return KindSmaller
	}
	if knownTimes && incoming.Mtime < existing.Mtime {
		return KindOlder
	}
	return KindOther
}

// ConflictAction returns the configured action for a kind, falling back to
// the default and then to asking. An unrecognised stored value asks rather
// than guessing: a policy nobody understands must not delete anything.
func (s *Store) ConflictAction(kind string) string {
	key, ok := settingForKind[kind]
	if !ok {
		return ActionAsk
	}
	def := DefaultConflictActions[kind]
	if def == "" {
		def = ActionAsk
	}
	got := s.Setting(key, def)
	if !ValidActions[got] {
		return ActionAsk
	}
	return got
}

// Encode renders a conflict for storage on the transfer row.
func (c Conflict) Encode() (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("encode conflict: %w", err)
	}
	return string(b), nil
}
