package workloadid

import (
	"math/big"
	"sync"
)

// RevocationList — источник сведений об отозванных сертификатах по их
// серийным номерам. Отзыв одной идентичности не требует замены сертификатов
// остальных рабочих нагрузок (инвариант ADR-0004).
type RevocationList interface {
	// Revoked сообщает, отозван ли сертификат с данным серийным номером.
	// Серийный номер передаётся в канонической форме SerialString.
	Revoked(serial string) bool
}

// SerialString приводит серийный номер сертификата к канонической строчной
// hex-форме без разделителей. Nil даёт пустую строку.
func SerialString(serial *big.Int) string {
	if serial == nil {
		return ""
	}

	return serial.Text(16)
}

// InMemoryRevocationList — простая потокобезопасная реализация RevocationList
// в памяти процесса. Подходит как минимальный механизм отзыва и как эталон
// для реализаций поверх внешних хранилищ.
type InMemoryRevocationList struct {
	mu      sync.RWMutex
	serials map[string]struct{}
}

// NewInMemoryRevocationList создаёт список отзыва, опционально сразу
// содержащий переданные серийные номера в форме SerialString.
func NewInMemoryRevocationList(serials ...string) *InMemoryRevocationList {
	list := &InMemoryRevocationList{serials: make(map[string]struct{}, len(serials))}
	for _, serial := range serials {
		list.serials[serial] = struct{}{}
	}

	return list
}

// Revoke отзывает сертификат с данным серийным номером. Повторный отзыв
// того же номера безвреден.
func (l *InMemoryRevocationList) Revoke(serial string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.serials[serial] = struct{}{}
}

// Revoked сообщает, отозван ли сертификат с данным серийным номером.
func (l *InMemoryRevocationList) Revoked(serial string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, revoked := l.serials[serial]

	return revoked
}
