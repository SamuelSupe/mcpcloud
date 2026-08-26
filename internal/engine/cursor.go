package engine

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"

	"mcpcloud/internal/model"
)

type cursorState struct {
	expires  time.Time
	rows     []map[string]any
	columns  []string
	base     model.QueryResult
	pageSize int
}

type CursorStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	maxRows int
	entries map[string]cursorState
	clock   func() time.Time
}

func NewCursorStore(ttl time.Duration, max int) *CursorStore {
	if max < 1 {
		max = 1
	}
	return &CursorStore{ttl: ttl, max: max, maxRows: 50_000, entries: map[string]cursorState{}, clock: time.Now}
}

func (s *CursorStore) Put(state cursorState) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	for len(s.entries) >= s.max || s.storedRowsLocked()+len(state.rows) > s.maxRows {
		var oldestKey string
		var oldest time.Time
		for key, entry := range s.entries {
			if oldestKey == "" || entry.expires.Before(oldest) {
				oldestKey, oldest = key, entry.expires
			}
		}
		if oldestKey == "" {
			break
		}
		delete(s.entries, oldestKey)
	}
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		panic(err)
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	state.expires = s.clock().Add(s.ttl)
	s.entries[token] = state
	return token
}

func (s *CursorStore) storedRowsLocked() int {
	total := 0
	for _, entry := range s.entries {
		total += len(entry.rows)
	}
	return total
}

func (s *CursorStore) Next(token string) (model.QueryResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	state, ok := s.entries[token]
	if !ok {
		return model.QueryResult{}, false
	}
	delete(s.entries, token)
	count := state.pageSize
	if count <= 0 || count > len(state.rows) {
		count = len(state.rows)
	}
	result := state.base
	result.Rows = append([]map[string]any(nil), state.rows[:count]...)
	result.Columns = state.columns
	result.Stats.Returned = len(result.Rows)
	result.NextCursor = ""
	if count < len(state.rows) {
		state.rows = state.rows[count:]
		result.NextCursor = s.putLocked(state)
	}
	return result, true
}

func (s *CursorStore) putLocked(state cursorState) string {
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		panic(err)
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	state.expires = s.clock().Add(s.ttl)
	s.entries[token] = state
	return token
}

func (s *CursorStore) pruneLocked() {
	now := s.clock()
	for key, entry := range s.entries {
		if !now.Before(entry.expires) {
			delete(s.entries, key)
		}
	}
}
