package jetstream

import (
	"context"
	"errors"
	"github.com/v0hmly/marketmesh/services/user/internal/adapter/out/registrationwire"
	"github.com/v0hmly/marketmesh/services/user/internal/application/consumeregistration"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/registrationevent"
	"testing"
	"time"
)

type applyFunc func(context.Context, registrationevent.Event) (consumeregistration.Outcome, error)

func (f applyFunc) Apply(c context.Context, e registrationevent.Event) (consumeregistration.Outcome, error) {
	return f(c, e)
}

type fakeMessage struct {
	payload   []byte
	ack, nak  int
	committed *bool
	t         *testing.T
}

func (m *fakeMessage) Data() []byte    { return m.payload }
func (m *fakeMessage) Subject() string { return Subject }
func (m *fakeMessage) DoubleAck(context.Context) error {
	if !*m.committed {
		m.t.Fatal("ACK before commit")
	}
	m.ack++
	return nil
}
func (m *fakeMessage) NakWithDelay(d time.Duration) error {
	if d < time.Second {
		m.t.Fatal("unbounded retry")
	}
	m.nak++
	return nil
}
func TestAckOnlyAfterCommittedApply(t *testing.T) {
	payload, _ := registrationwire.Marshal(registrationevent.Event{ID: [16]byte{1}, SubjectID: profile.SubjectID{2}, OccurredAt: time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)})
	for _, tc := range []struct {
		name    string
		err     error
		invalid bool
		want    Outcome
	}{{"success", nil, false, Applied}, {"duplicate", nil, false, Duplicate}, {"database", errors.New("db"), false, StoreError}, {"conflict", consumeregistration.ErrConflict, false, Conflict}, {"malformed", nil, true, Invalid}} {
		t.Run(tc.name, func(t *testing.T) {
			committed := false
			calls := 0
			var observed Outcome
			c := Consumer{cfg: Config{OperationTimeout: time.Second, RetryDelay: time.Second, Observe: func(o Outcome) { observed = o }}, apply: applyFunc(func(ctx context.Context, e registrationevent.Event) (consumeregistration.Outcome, error) {
				calls++
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("missing apply deadline")
				}
				committed = tc.err == nil
				if tc.name == "duplicate" {
					return consumeregistration.Duplicate, nil
				}
				return consumeregistration.Applied, tc.err
			})}
			m := &fakeMessage{payload: payload, committed: &committed, t: t}
			if tc.invalid {
				m.payload = []byte{1}
			}
			c.handle(context.Background(), m)
			if observed != tc.want {
				t.Fatal(observed)
			}
			if tc.invalid && calls != 0 {
				t.Fatal("malformed reached store")
			}
			if tc.err != nil || tc.invalid {
				if m.ack != 0 || m.nak != 1 {
					t.Fatal("failed event acknowledged")
				}
			} else if m.ack != 1 || m.nak != 0 {
				t.Fatal("success not acknowledged")
			}
		})
	}
}

func TestConstructorBoundsAndClosedRun(t *testing.T) {
	apply := applyFunc(func(context.Context, registrationevent.Event) (consumeregistration.Outcome, error) {
		t.Fatal("unexpected apply")
		return "", nil
	})
	for _, change := range []func(*Config){func(c *Config) { c.URL = "tls://localhost:0" }, func(c *Config) { c.URL = "tls://localhost:65536" }, func(c *Config) { c.URL = "tls://localhost:4222?" }, func(c *Config) { c.TLSConfig.ServerName = "" }, func(c *Config) { c.TLSConfig.MaxVersion = 0x0303 }, func(c *Config) { c.ConnectTimeout = 6 * time.Second }, func(c *Config) { c.FetchTimeout = time.Millisecond }, func(c *Config) { c.URL = "tls://name:password@localhost:4222" }, func(c *Config) { c.URL = "nats://localhost:4222" }, func(c *Config) { c.TLSConfig.InsecureSkipVerify = true }, func(c *Config) { c.OperationTimeout = 31 * time.Second }, func(c *Config) { c.FetchTimeout = 6 * time.Second }, func(c *Config) { c.RetryDelay = time.Millisecond }} {
		c := testConfig(t)
		change(&c)
		if got, err := New(c, apply); err == nil {
			got.Close()
			t.Fatal("unsafe config accepted")
		}
	}
	c, err := New(testConfig(t), apply)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err := c.Run(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCancellationAndUnknownOutcomeNeverAck(t *testing.T) {
	payload, _ := registrationwire.Marshal(registrationevent.Event{ID: [16]byte{1}, SubjectID: profile.SubjectID{2}, OccurredAt: time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)})
	for _, phase := range []string{"before", "during", "unknown"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if phase == "before" {
				cancel()
			}
			calls := 0
			committed := false
			c := Consumer{cfg: Config{OperationTimeout: time.Second, RetryDelay: time.Second}, apply: applyFunc(func(context.Context, registrationevent.Event) (consumeregistration.Outcome, error) {
				calls++
				if phase == "during" {
					cancel()
					return "", context.Canceled
				}
				return "unrecognized", nil
			})}
			m := &fakeMessage{payload: payload, committed: &committed, t: t}
			c.handle(ctx, m)
			if phase == "before" && calls != 0 {
				t.Fatal("canceled handler entered store")
			}
			if m.ack != 0 {
				t.Fatal("unexpected ACK")
			}
			wantNak := 0
			if phase == "unknown" {
				wantNak = 1
			}
			if m.nak != wantNak {
				t.Fatal("unexpected NAK", m.nak)
			}
		})
	}
}
func TestRunIsOneShot(t *testing.T) {
	c, err := New(testConfig(t), applyFunc(func(context.Context, registrationevent.Event) (consumeregistration.Outcome, error) { return "", nil }))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if c.ctx.Err() == nil {
		t.Fatal("Run did not close consumer")
	}
	if err := c.Run(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal("closed consumer restarted", err)
	}
}
