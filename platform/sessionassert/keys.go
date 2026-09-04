package sessionassert

import (
	"crypto/ed25519"
	"fmt"
	"sync"
)

// KeySource — источник доверенных открытых ключей верификатора.
// Реализации обязаны быть локальными: неизвестный kid не должен вызывать
// загрузку ключей по сети или произвольному адресу (требование ADR-0005).
type KeySource interface {
	// Key возвращает открытый ключ по идентификатору kid.
	// Неизвестный kid должен давать ошибку, совместимую с ErrUnknownKeyID.
	Key(kid string) (ed25519.PublicKey, error)
}

// StaticKeySet — потокобезопасный набор открытых ключей kid→key.
// Поддерживает ротацию с перекрытием: новый ключ добавляется заранее,
// старый удаляется после истечения периода перекрытия.
type StaticKeySet struct {
	mu   sync.RWMutex
	keys map[string]ed25519.PublicKey
}

// NewStaticKeySet создаёт пустой набор ключей.
func NewStaticKeySet() *StaticKeySet {
	return &StaticKeySet{keys: make(map[string]ed25519.PublicKey)}
}

// Add регистрирует открытый ключ под идентификатором kid, заменяя
// существующий с тем же kid.
func (s *StaticKeySet) Add(kid string, key ed25519.PublicKey) error {
	if kid == "" {
		return fmt.Errorf("%w: empty kid", ErrInvalidParams)
	}
	if len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: invalid public key size %d", ErrInvalidParams, len(key))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[kid] = key
	return nil
}

// Remove удаляет ключ по идентификатору kid. Удаление неизвестного kid
// не считается ошибкой.
func (s *StaticKeySet) Remove(kid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.keys, kid)
}

// Key реализует KeySource.
func (s *StaticKeySet) Key(kid string) (ed25519.PublicKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key, ok := s.keys[kid]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownKeyID, kid)
	}
	return key, nil
}
