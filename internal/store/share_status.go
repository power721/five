package store

import "context"

// shelvedRetryAfterSeconds parks a persistently-failing share this far in the
// future so the scheduler stops re-queuing it, without marking it DEAD (DEAD
// shares and their files are pruned at export time). ~10 years == effectively
// never, yet recoverable by resetting retry_after_unix (see ReactivateShare).
// Package-level var so tests can shrink it.
var shelvedRetryAfterSeconds int64 = 10 * 365 * 24 * 60 * 60

// MarkShareShelved parks a share that fails persistently (e.g. the "empty data
// with nonzero count" error) so the scheduler stops re-queueing it. Unlike
// MarkShareDead it keeps status QUARANTINE and preserves any previously-crawled
// files: DEAD shares are pruned (with their files) when the index is exported,
// and a share that 115 currently serves empty may still hold files worth
// keeping. The share stays QUARANTINE with a far-future retry_after so it is
// effectively never retried.
func (s *Store) MarkShareShelved(ctx context.Context, shareCode, errText string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE share
		SET status='QUARANTINE',
			last_error=?,
			retry_after_unix=?
		WHERE share_code = ?`, errText, timeNowUnix()+shelvedRetryAfterSeconds, shareCode)
	return err
}

// ReactivateShare resets a shelved/quarantined/dead share back to ACTIVE so the
// scheduler picks it up again on its next pass: clears the failure count, last
// error, and any far-future retry_after. Returns false if no share matched
// shareCode. Use it to retry a share that was shelved for a persistent error
// once the underlying 115 condition may have cleared.
// MarkShareDuplicate records that shareCode is a duplicate of canonical (same
// file_size, above the dedup threshold) and parks it: DUPLICATE status is
// excluded from scheduling and export. Clears failure bookkeeping.
func (s *Store) MarkShareDuplicate(ctx context.Context, shareCode, canonical string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE share
		SET status='DUPLICATE',
			duplicate_of=?,
			retry_after_unix=0,
			failure_count=0
		WHERE share_code = ?`, canonical, shareCode)
	return err
}

func (s *Store) ReactivateShare(ctx context.Context, shareCode string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE share
		SET status='ACTIVE',
			last_error='',
			duplicate_of='',
			failure_count=0,
			retry_after_unix=0
		WHERE share_code = ?`, shareCode)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
