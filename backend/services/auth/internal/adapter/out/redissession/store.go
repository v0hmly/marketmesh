// Package redissession хранит только access digest и краткоживущее состояние в Auth Redis.
package redissession

import (
	"context"
	"encoding/hex"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"
	platformredis "github.com/v0hmly/marketmesh/platform/redis"
	application "github.com/v0hmly/marketmesh/services/auth/internal/application/session"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
)

// Executor выполняет команды один раз; конфигурация клиента обязана иметь RoleAuth.
// backend/platform/redis не раскрывает Role существующего клиента, поэтому выбор роли
// проверяется composition root при создании отдельного Auth Redis client.
type Executor interface {
	Execute(context.Context, platformredis.Operation) error
}
type Store struct{ client Executor }

func New(client Executor) (*Store, error) {
	if client == nil {
		return nil, domain.ErrUnavailable
	}
	return &Store{client: client}, nil
}

// Сравнение десятичных строк сохраняет точность bigint выше 2^53.
// Равная версия не может заменить digest или продлить срок действия.
const putScript = `
local old = redis.call('HGET', KEYS[1], 'version')
if old then
 if string.len(old) > string.len(ARGV[1]) or (string.len(old) == string.len(ARGV[1]) and old > ARGV[1]) then return 0 end
 if old == ARGV[1] then
  if redis.call('HGET', KEYS[1], 'digest') == ARGV[2] and redis.call('HGET', KEYS[1], 'expires_at') == ARGV[3] then return 1 end
  return 0
 end
end
redis.call('HSET', KEYS[1], 'version', ARGV[1], 'digest', ARGV[2], 'expires_at', ARGV[3])
redis.call('PEXPIREAT', KEYS[1], ARGV[4])
return 1
`

func (s *Store) Put(ctx context.Context, record domain.Record, digest domain.Digest, now time.Time) error {
	if !record.Active(now) || !now.Before(record.AccessExpiresAt) || record.AccessExpiresAt.After(record.ExpiresAt) {
		return domain.ErrInvalidSession
	}
	result := make(chan int64, 1)
	err := s.client.Execute(ctx, func(ctx context.Context, commands goredis.Cmdable) error {
		n, err := commands.Eval(ctx, putScript, []string{key(record.ID)}, strconv.FormatInt(record.Version, 10), hex.EncodeToString(digest[:]), record.AccessExpiresAt.UTC().Format(time.RFC3339Nano), record.AccessExpiresAt.UnixMilli()).Int64()
		if err == nil {
			result <- n
		}
		return err
	})
	if err != nil {
		return domain.ErrUnavailable
	}
	select {
	case n := <-result:
		if n != 1 {
			return domain.ErrInvalidSession
		}
		return nil
	default:
		return domain.ErrUnavailable
	}
}
func (s *Store) Get(ctx context.Context, id domain.ID) (domain.Access, error) {
	if id == (domain.ID{}) {
		return domain.Access{}, domain.ErrInvalidSession
	}
	result := make(chan map[string]string, 1)
	err := s.client.Execute(ctx, func(ctx context.Context, commands goredis.Cmdable) error {
		value, err := commands.HGetAll(ctx, key(id)).Result()
		if err == nil {
			result <- value
		}
		return err
	})
	if err != nil {
		return domain.Access{}, domain.ErrUnavailable
	}
	var value map[string]string
	select {
	case value = <-result:
	default:
		return domain.Access{}, domain.ErrUnavailable
	}
	if len(value) == 0 {
		return domain.Access{}, domain.ErrInvalidSession
	}
	if len(value) != 3 {
		return domain.Access{}, domain.ErrUnavailable
	}
	version, err := strconv.ParseInt(value["version"], 10, 64)
	if err != nil || version <= 0 {
		return domain.Access{}, domain.ErrUnavailable
	}
	digest, err := hex.DecodeString(value["digest"])
	if err != nil || len(digest) != 32 {
		return domain.Access{}, domain.ErrUnavailable
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, value["expires_at"])
	if err != nil || expiresAt.IsZero() {
		return domain.Access{}, domain.ErrUnavailable
	}
	access := domain.Access{Version: version, ExpiresAt: expiresAt}
	copy(access.Digest[:], digest)
	return access, nil
}
func key(id domain.ID) string { return "auth:session:access:" + id.String() }

var _ application.AccessStore = (*Store)(nil)
