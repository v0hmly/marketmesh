package probe

import (
	"cmp"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"

	"connectrpc.com/connect"
	e2ev1 "github.com/v0hmly/marketmesh/api/gen/go/e2e/v1"
	"google.golang.org/grpc"
)

const (
	FakeReadRoute     = "user-get-me"
	FakeMutatingRoute = "user-update-me"
	maxLedgerSources  = 128
	maxLedgerLimit    = 100_000
)

// FakeTrafficClient — минимальный опубликованный MM-29 contract внешних
// read/mutate вызовов. Ledger намеренно читается не через front door.
type FakeTrafficClient interface {
	Read(
		context.Context,
		*connect.Request[e2ev1.ReadRequest],
	) (*connect.Response[e2ev1.ReadResponse], error)
	Mutate(
		context.Context,
		*connect.Request[e2ev1.MutateRequest],
	) (*connect.Response[e2ev1.MutateResponse], error)
}

// FakeLedgerClient — минимальный direct internal gRPC contract MM-29.
type FakeLedgerClient interface {
	Ledger(
		context.Context,
		*e2ev1.LedgerRequest,
		...grpc.CallOption,
	) (*e2ev1.LedgerResponse, error)
}

// Instance описывает безопасное соответствие workload instance логическому
// DC. Оно строится direct ledger discovery до начала внешнего traffic.
type Instance struct {
	Source     string
	DataCenter DataCenter
}

// InstanceDirectory — неизменяемый resolver workload instance → DC.
type InstanceDirectory struct {
	dataCenters map[string]DataCenter
}

// NewInstanceDirectory валидирует и копирует discovery result.
func NewInstanceDirectory(instances []Instance) (InstanceDirectory, error) {
	dataCenters := make(map[string]DataCenter, len(instances))
	for _, instance := range instances {
		if !validateDimension(instance.Source, false) {
			return InstanceDirectory{}, errors.New("probe: invalid workload instance")
		}
		if !validateDataCenter(instance.DataCenter, false) {
			return InstanceDirectory{}, errors.New("probe: invalid workload data center")
		}
		if _, found := dataCenters[instance.Source]; found {
			return InstanceDirectory{}, errors.New("probe: duplicate workload instance")
		}
		dataCenters[instance.Source] = instance.DataCenter
	}
	if len(dataCenters) == 0 {
		return InstanceDirectory{}, errors.New("probe: workload instance directory is empty")
	}

	return InstanceDirectory{dataCenters: dataCenters}, nil
}

// Resolve возвращает логический DC без раскрытия topology address.
func (directory InstanceDirectory) Resolve(source string) (DataCenter, bool) {
	dataCenter, found := directory.dataCenters[source]
	return dataCenter, found
}

// FakeInvoker преобразует MM-29 ConnectRPC contract в безопасный probe
// Invoker. Он выполняет ровно один вызов и не повторяет mutation.
type FakeInvoker struct {
	client    FakeTrafficClient
	directory InstanceDirectory
}

// NewFakeInvoker создаёт adapter к уже сконфигурированному front-door client.
func NewFakeInvoker(
	client FakeTrafficClient,
	directory InstanceDirectory,
) (*FakeInvoker, error) {
	if isNilDependency(client) {
		return nil, errors.New("probe: fake traffic client must not be nil")
	}
	if len(directory.dataCenters) == 0 {
		return nil, errors.New("probe: instance directory must not be empty")
	}

	return &FakeInvoker{client: client, directory: directory}, nil
}

// Invoke реализует один read или mutating request без raw error в результате.
func (invoker *FakeInvoker) Invoke(ctx context.Context, request Request) Response {
	if ctx == nil {
		return Response{Outcome: OutcomeRejected}
	}
	requestID, err := decodeRequestID(request.ID)
	if err != nil {
		return Response{Outcome: OutcomeRejected}
	}

	switch request.Class {
	case TrafficClassRead:
		response, callErr := invoker.client.Read(
			ctx,
			connect.NewRequest(&e2ev1.ReadRequest{RequestId: requestID}),
		)
		if callErr != nil {
			return Response{Outcome: fakeCallOutcome(callErr)}
		}
		if response == nil || response.Msg == nil {
			return Response{Outcome: OutcomeInvalidMetadata}
		}
		return invoker.response(
			response.Msg.GetInstanceId(),
			response.Msg.GetSequence(),
			false,
			FakeReadRoute,
		)
	case TrafficClassMutating:
		if request.IdempotencyKey == "" || request.IdempotencyKey != request.ID {
			return Response{Outcome: OutcomeRejected}
		}
		call := connect.NewRequest(&e2ev1.MutateRequest{RequestId: requestID})
		call.Header().Set("Idempotency-Key", request.IdempotencyKey)
		response, callErr := invoker.client.Mutate(ctx, call)
		if callErr != nil {
			return Response{Outcome: fakeCallOutcome(callErr)}
		}
		if response == nil || response.Msg == nil {
			return Response{Outcome: OutcomeInvalidMetadata}
		}
		return invoker.response(
			response.Msg.GetInstanceId(),
			response.Msg.GetSequence(),
			response.Msg.GetDuplicate(),
			FakeMutatingRoute,
		)
	default:
		return Response{Outcome: OutcomeRejected}
	}
}

func (invoker *FakeInvoker) response(
	source string,
	sequence uint64,
	duplicate bool,
	route string,
) Response {
	dataCenter, found := invoker.directory.Resolve(source)
	if !found || sequence == 0 {
		return Response{Outcome: OutcomeInvalidMetadata}
	}

	return Response{
		Outcome:          OutcomeSuccess,
		RouteID:          route,
		DataCenter:       dataCenter,
		Source:           source,
		InternalSequence: sequence,
		Duplicate:        duplicate,
	}
}

// LedgerSource указывает один direct internal workload client и его
// логический DC. Address, kube context и credentials здесь не сохраняются.
type LedgerSource struct {
	DataCenter DataCenter
	Client     FakeLedgerClient
}

// LedgerCollector выполняет bounded discovery и финальное чтение всех
// настроенных direct internal ledgers.
type LedgerCollector struct {
	sources []LedgerSource
	limit   uint32
}

// NewLedgerCollector проверяет конечное число sources и серверный limit.
func NewLedgerCollector(
	sources []LedgerSource,
	limit uint32,
) (*LedgerCollector, error) {
	if len(sources) == 0 || len(sources) > maxLedgerSources {
		return nil, errors.New("probe: ledger source count is outside bounds")
	}
	if limit == 0 || limit > maxLedgerLimit {
		return nil, errors.New("probe: ledger limit is outside bounds")
	}
	result := slices.Clone(sources)
	for _, source := range result {
		if !validateDataCenter(source.DataCenter, false) {
			return nil, errors.New("probe: invalid ledger data center")
		}
		if isNilDependency(source.Client) {
			return nil, errors.New("probe: ledger client must not be nil")
		}
	}

	return &LedgerCollector{sources: result, limit: limit}, nil
}

// Discover получает instance ID каждого direct workload до запуска traffic.
// Любая неизвестная или конфликтующая identity завершает discovery ошибкой.
func (collector *LedgerCollector) Discover(
	ctx context.Context,
) (InstanceDirectory, error) {
	if ctx == nil {
		return InstanceDirectory{}, errors.New("probe: ledger discovery context must not be nil")
	}
	results := collector.read(ctx, 1)
	instances := make([]Instance, 0, len(results))
	for _, result := range results {
		if result.err != nil || result.response == nil {
			return InstanceDirectory{}, errors.New("probe: ledger discovery failed")
		}
		instances = append(instances, Instance{
			Source:     result.response.GetInstanceId(),
			DataCenter: result.source.DataCenter,
		})
	}

	return NewInstanceDirectory(instances)
}

// Collect читает один bounded snapshot после остановки runner. Он fail closed
// помечает transport, metadata, validation и возможную truncation ошибки.
func (collector *LedgerCollector) Collect(ctx context.Context) InternalSnapshot {
	if ctx == nil {
		return InternalSnapshot{
			Records:           []InternalRecord{},
			IsComplete:        false,
			IncompleteReasons: []string{"ledger_context_invalid"},
		}
	}
	results := collector.read(ctx, collector.limit)
	snapshot := InternalSnapshot{Records: []InternalRecord{}, IsComplete: true}
	seenSources := make(map[string]DataCenter, len(results))
	for _, result := range results {
		if result.err != nil || result.response == nil {
			snapshot.IsComplete = false
			snapshot.IncompleteReasons = append(
				snapshot.IncompleteReasons,
				"ledger_rpc_failed",
			)
			continue
		}
		source := result.response.GetInstanceId()
		if !validateDimension(source, false) {
			snapshot.IsComplete = false
			snapshot.IncompleteReasons = append(
				snapshot.IncompleteReasons,
				"ledger_instance_invalid",
			)
			continue
		}
		if _, found := seenSources[source]; found {
			snapshot.IsComplete = false
			snapshot.IncompleteReasons = append(
				snapshot.IncompleteReasons,
				"ledger_instance_duplicate",
			)
			continue
		}
		seenSources[source] = result.source.DataCenter
		entries := result.response.GetEntries()
		if uint32(len(entries)) >= collector.limit {
			snapshot.IsComplete = false
			snapshot.IncompleteReasons = append(
				snapshot.IncompleteReasons,
				"ledger_limit_reached",
			)
		}
		for _, entry := range entries {
			record, valid := internalRecord(entry, source, result.source.DataCenter)
			if !valid {
				snapshot.IsComplete = false
				snapshot.IncompleteReasons = append(
					snapshot.IncompleteReasons,
					"ledger_entry_invalid",
				)
				continue
			}
			snapshot.Records = append(snapshot.Records, record)
		}
	}
	snapshot.IncompleteReasons = normalizedReasons(snapshot.IncompleteReasons)

	return snapshot
}

type ledgerResult struct {
	index    int
	source   LedgerSource
	response *e2ev1.LedgerResponse
	err      error
}

func (collector *LedgerCollector) read(
	ctx context.Context,
	limit uint32,
) []ledgerResult {
	results := make(chan ledgerResult, len(collector.sources))
	var group sync.WaitGroup
	for index, source := range collector.sources {
		group.Go(func() {
			response, err := readLedgerSafely(
				source.Client,
				ctx,
				&e2ev1.LedgerRequest{Limit: limit},
			)
			results <- ledgerResult{
				index: index, source: source, response: response, err: err,
			}
		})
	}
	group.Wait()
	close(results)

	collected := make([]ledgerResult, 0, len(collector.sources))
	for result := range results {
		collected = append(collected, result)
	}
	slices.SortFunc(collected, func(left, right ledgerResult) int {
		return cmp.Or(
			cmp.Compare(left.source.DataCenter, right.source.DataCenter),
			cmp.Compare(left.index, right.index),
		)
	})

	return collected
}

func readLedgerSafely(
	client FakeLedgerClient,
	ctx context.Context,
	request *e2ev1.LedgerRequest,
) (response *e2ev1.LedgerResponse, resultErr error) {
	defer func() {
		if recover() != nil {
			response = nil
			resultErr = errors.New("probe: ledger client panic")
		}
	}()

	return client.Ledger(ctx, request)
}

func internalRecord(
	entry *e2ev1.LedgerEntry,
	source string,
	dataCenter DataCenter,
) (InternalRecord, bool) {
	if entry == nil || len(entry.GetRequestId()) != requestIDSize {
		return InternalRecord{}, false
	}
	record := InternalRecord{
		RequestID:  hex.EncodeToString(entry.GetRequestId()),
		Sequence:   entry.GetSequence(),
		Attempts:   entry.GetAttempts(),
		Outcome:    OutcomeSuccess,
		DataCenter: dataCenter,
		Source:     source,
	}
	switch entry.GetOperation() {
	case e2ev1.Operation_OPERATION_READ:
		record.Class = TrafficClassRead
		record.RouteID = FakeReadRoute
		if len(entry.GetIdempotencyKeySha256()) != 0 {
			return InternalRecord{}, false
		}
	case e2ev1.Operation_OPERATION_MUTATE:
		record.Class = TrafficClassMutating
		record.RouteID = FakeMutatingRoute
		if len(entry.GetIdempotencyKeySha256()) != 32 {
			return InternalRecord{}, false
		}
		record.IdempotencyKeySHA256 = hex.EncodeToString(
			entry.GetIdempotencyKeySha256(),
		)
	default:
		return InternalRecord{}, false
	}

	return record, validInternalRecord(record)
}

func decodeRequestID(value string) ([]byte, error) {
	if !validateRequestID(value) {
		return nil, errors.New("probe: invalid request id")
	}
	decoded := make([]byte, requestIDSize)
	if _, err := hex.Decode(decoded, []byte(value)); err != nil {
		return nil, fmt.Errorf("probe: decoding request id: %w", err)
	}

	return decoded, nil
}

func fakeCallOutcome(err error) Outcome {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return OutcomeTimeout
	case errors.Is(err, context.Canceled):
		return OutcomeCanceled
	}

	switch connect.CodeOf(err) {
	case connect.CodeCanceled:
		return OutcomeCanceled
	case connect.CodeDeadlineExceeded:
		return OutcomeTimeout
	case connect.CodeUnavailable:
		return OutcomeUnavailable
	case connect.CodeInvalidArgument,
		connect.CodeUnauthenticated,
		connect.CodePermissionDenied,
		connect.CodeNotFound,
		connect.CodeAlreadyExists,
		connect.CodeResourceExhausted,
		connect.CodeFailedPrecondition:
		return OutcomeRejected
	default:
		return OutcomeInternalError
	}
}

func isNilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan,
		reflect.Func,
		reflect.Interface,
		reflect.Map,
		reflect.Pointer,
		reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
