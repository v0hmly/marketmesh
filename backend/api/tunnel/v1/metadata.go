package tunnelv1

const (
	// InternalIdempotencyKeyMetadata is the only internal gRPC metadata name
	// derived from tunnel Open.idempotency_key. It is binary, bounded by the
	// v1 protocol limit, and must never be logged or returned to gateway-in.
	InternalIdempotencyKeyMetadata = "marketmesh-idempotency-key-bin"
)
