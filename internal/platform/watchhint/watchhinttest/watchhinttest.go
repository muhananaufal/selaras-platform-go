// Package watchhinttest records watch hints in tests.
package watchhinttest

import (
	"context"
	"sync"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/watchhint"
)

// Recorder is a watchhint.Announcer that keeps what it was told.
type Recorder struct {
	mu   sync.Mutex
	keys []watchhint.Key
}

var _ watchhint.Announcer = (*Recorder)(nil)

func (r *Recorder) Announce(_ context.Context, key watchhint.Key) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, key)
}

// Keys returns a copy of the announced keys, in order.
func (r *Recorder) Keys() []watchhint.Key {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]watchhint.Key(nil), r.keys...)
}
