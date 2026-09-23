package postgressecurity

import (
	"context"

	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/security"
)

// All calls run under WithAccount's credential lock and use its transaction.
func (u *unit) ReplaceRecoveryCodes(ctx context.Context, digests []domain.Digest) error {
	if u.account == nil || len(digests) != 8 {
		return domain.Unavailable
	}
	if _, err := u.executor.Exec(ctx, `DELETE FROM auth.recovery_codes WHERE subject_id=$1`, u.account.Subject.Bytes()); err != nil {
		return err
	}
	for _, digest := range digests {
		if _, err := u.executor.Exec(ctx, `INSERT INTO auth.recovery_codes(subject_id,secret_digest,revision) VALUES($1,$2,$3)`, u.account.Subject.Bytes(), digest[:], u.account.Revision); err != nil {
			return err
		}
	}
	return nil
}

func (u *unit) ConsumeRecoveryCode(ctx context.Context, digest domain.Digest) (bool, error) {
	if u.account == nil || !u.account.CodeEnabled {
		return false, domain.Unavailable
	}
	tag, err := u.executor.Exec(ctx, `DELETE FROM auth.recovery_codes WHERE subject_id=$1 AND secret_digest=$2 AND revision=$3`, u.account.Subject.Bytes(), digest[:], u.account.Revision)
	return tag.RowsAffected() == 1, err
}

func (u *unit) RecoveryCodesRemaining(ctx context.Context) (int, error) {
	if u.account == nil || !u.account.CodeEnabled {
		return 0, nil
	}
	var count int
	err := u.executor.QueryRow(ctx, `SELECT count(*) FROM auth.recovery_codes WHERE subject_id=$1 AND revision=$2`, u.account.Subject.Bytes(), u.account.Revision).Scan(&count)
	return count, err
}
