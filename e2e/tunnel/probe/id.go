package probe

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"sync/atomic"
)

const (
	randomPrefixSize = 8
	requestIDSize    = 16
)

// IDGenerator создаёт безопасные идентификаторы попыток. Реализация должна
// быть конкурентно-безопасной; duplicate всё равно выявляется journal.
type IDGenerator interface {
	Next() string
}

type randomIDGenerator struct {
	prefix  [randomPrefixSize]byte
	current atomic.Uint64
}

func newRandomIDGenerator(reader io.Reader) (*randomIDGenerator, error) {
	var randomPrefix [randomPrefixSize]byte
	if _, err := io.ReadFull(reader, randomPrefix[:]); err != nil {
		return nil, fmt.Errorf("probe: generating request id prefix: %w", err)
	}

	return &randomIDGenerator{
		prefix: randomPrefix,
	}, nil
}

func defaultIDGenerator() (*randomIDGenerator, error) {
	return newRandomIDGenerator(rand.Reader)
}

func (generator *randomIDGenerator) Next() string {
	sequence := generator.current.Add(1)
	var requestID [requestIDSize]byte
	copy(requestID[:], generator.prefix[:])
	binary.BigEndian.PutUint64(requestID[randomPrefixSize:], sequence)

	return hex.EncodeToString(requestID[:])
}
