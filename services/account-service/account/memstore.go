package account

import (
	"context"
	"sync"
)

// MemStore is an in-memory Store used in unit tests and local development
// without Postgres. It is safe for concurrent use.
type MemStore struct {
	mu       sync.Mutex
	nextID   int64
	users    map[int64]*User
	byEmail  map[string]int64
	sessions map[string]Session
	apiKeys  map[string]APIKey
}

// NewMemStore creates an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{
		users:    make(map[int64]*User),
		byEmail:  make(map[string]int64),
		sessions: make(map[string]Session),
		apiKeys:  make(map[string]APIKey),
	}
}

func (m *MemStore) CreateUser(_ context.Context, email, passwordHash string) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byEmail[email]; ok {
		return nil, ErrEmailTaken
	}
	m.nextID++
	u := &User{ID: m.nextID, Email: email, PasswordHash: passwordHash, Status: "active"}
	m.users[u.ID] = u
	m.byEmail[email] = u.ID
	cp := *u
	return &cp, nil
}

func (m *MemStore) UserByEmail(_ context.Context, email string) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byEmail[email]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *m.users[id]
	return &cp, nil
}

func (m *MemStore) UserByID(_ context.Context, id int64) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *u
	return &cp, nil
}

func (m *MemStore) SetTOTP(_ context.Context, userID int64, secret string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[userID]
	if !ok {
		return ErrNotFound
	}
	u.TOTPSecret, u.TOTPEnabled = secret, enabled
	return nil
}

func (m *MemStore) CreateSession(_ context.Context, s Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.TokenHash] = s
	return nil
}

func (m *MemStore) SessionByTokenHash(_ context.Context, tokenHash string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[tokenHash]
	if !ok {
		return nil, ErrNotFound
	}
	return &s, nil
}

func (m *MemStore) DeleteSession(_ context.Context, tokenHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, tokenHash)
	return nil
}

func (m *MemStore) CreateAPIKey(_ context.Context, k APIKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.apiKeys[k.KeyID] = k
	return nil
}

func (m *MemStore) APIKeysByUser(_ context.Context, userID int64) ([]APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []APIKey
	for _, k := range m.apiKeys {
		if k.UserID == userID {
			out = append(out, k)
		}
	}
	return out, nil
}
