package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/v0hmly/marketmesh/services/files/internal/application/processing"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

func logWorkerError(ctx context.Context, logger *slog.Logger, err error) {
	if err == nil {
		return
	}
	var job *processing.JobError
	if !errors.As(err, &job) {
		logger.ErrorContext(ctx, "file worker", "error_class", "unknown")
		return
	}
	if job.Stage == "claim" && errors.Is(err, file.ErrNotFound) {
		return // Empty queue is routine; a missing object during processing is not.
	}
	fields := []any{"error_class", job.Class(), "stage", job.Stage}
	if job.FileID != (file.ID{}) {
		fields = append(fields, "file_id", job.FileID.String(), "state", string(job.State))
	}
	logger.WarnContext(ctx, "file worker", fields...)
}
