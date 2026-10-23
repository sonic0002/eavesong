package store

import "context"

func (s *Store) NotificationDelivered(ctx context.Context, key, channel string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM notification_deliveries WHERE event_key=? AND channel=?`, key, channel).Scan(&n)
	return n != 0, err
}
func (s *Store) RecordNotificationDelivery(ctx context.Context, key, channel string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO notification_deliveries(event_key,channel,sent_at) VALUES(?,?,?)`, key, channel, now())
	return err
}
