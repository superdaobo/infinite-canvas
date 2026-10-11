package realtime

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// DefaultTicketTTL 是 WS 握手 ticket 的有效期。短时效，仅够完成一次建连。
const DefaultTicketTTL = 30 * time.Second

// Ticket 是一次性 WS 握手凭据，绑定 userId + roomId。
type Ticket struct {
	Value     string    `json:"ticket"`
	RoomID    string    `json:"roomId"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type ticketEntry struct {
	userID   string
	username string
	roomID   string
	expires  time.Time
}

// TicketStore 是进程内的一次性 ticket 存储。ticket 明文只存在于内存，
// 消费后立即删除（单次使用），过期后不可用。不落库、不落日志。
type TicketStore struct {
	mu      sync.Mutex
	entries map[string]ticketEntry
}

// NewTicketStore 创建空的 ticket 存储。
func NewTicketStore() *TicketStore {
	return &TicketStore{entries: make(map[string]ticketEntry)}
}

// Issue 签发一个绑定 (userID, roomID) 的一次性 ticket，TTL 到期后失效。
func (s *TicketStore) Issue(userID, username, roomID string, ttl time.Duration) (Ticket, error) {
	if ttl <= 0 {
		ttl = DefaultTicketTTL
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return Ticket{}, err
	}
	value := base64.RawURLEncoding.EncodeToString(buf)
	expires := time.Now().UTC().Add(ttl)
	s.mu.Lock()
	s.pruneLocked(time.Now().UTC())
	s.entries[value] = ticketEntry{userID: userID, username: username, roomID: roomID, expires: expires}
	s.mu.Unlock()
	return Ticket{Value: value, RoomID: roomID, ExpiresAt: expires}, nil
}

// Consume 原子地校验并消费 ticket：命中且未过期时删除并返回绑定的身份信息。
// 已消费、已过期或不存在统一返回 false，防止重放。
func (s *TicketStore) Consume(value string) (userID, username, roomID string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, found := s.entries[value]
	if !found {
		return "", "", "", false
	}
	delete(s.entries, value)
	if time.Now().UTC().After(entry.expires) {
		return "", "", "", false
	}
	return entry.userID, entry.username, entry.roomID, true
}

// pruneLocked 清理过期条目，调用方需持有锁。
func (s *TicketStore) pruneLocked(now time.Time) {
	for value, entry := range s.entries {
		if now.After(entry.expires) {
			delete(s.entries, value)
		}
	}
}

// DefaultTickets 是进程级 ticket 存储。单实例部署下足够；多实例需替换为共享存储。
var DefaultTickets = NewTicketStore()
