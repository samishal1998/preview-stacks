package api

import (
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/loki"
)

// lokiEntry is one pending Loki save: a section's patch and who sent it. gen orders the saves; a
// job takes every entry with gen <= its own, so a superseded save is carried, never lost.
type lokiEntry struct {
	gen     uint64
	by      string
	chunks  *loki.ChunksPatch
	storage *loki.StoragePatch
}

// lokiLead is the period guard's lead: one restart plus a ready wait, with slack.
func (s *Server) lokiLead() time.Duration {
	return loki.Lead(time.Duration(s.opts.LokiReadyTimeoutMs) * time.Millisecond)
}
