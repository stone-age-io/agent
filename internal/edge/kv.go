package edge

import (
	"context"
	"errors"

	"github.com/nats-io/nats.go/jetstream"
)

// openOrCreateKV returns an existing bucket untouched, creating it from cfg only
// if it is absent.
//
// Deliberately NOT CreateOrUpdateKeyValue: the twin buckets are shared with
// operators — the console can create and tune them too — and an agent that
// reasserts its own retention on every startup silently reverts whatever an
// admin set in the UI, with no error and no log line anywhere. Whoever creates
// the bucket first defines it; the agent only fills in the gap.
func openOrCreateKV(ctx context.Context, js jetstream.JetStream, cfg jetstream.KeyValueConfig) (jetstream.KeyValue, error) {
	kv, err := js.KeyValue(ctx, cfg.Bucket)
	if err == nil {
		return kv, nil
	}
	if !errors.Is(err, jetstream.ErrBucketNotFound) {
		return nil, err
	}
	return js.CreateKeyValue(ctx, cfg)
}
