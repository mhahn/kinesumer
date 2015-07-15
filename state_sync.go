package kinesumer

import (
	"time"

	"github.com/aws/aws-sdk-go/service/kinesis"
)

type KinesumerError struct {
	// One of "crit", "error", "warn", "info", "debug"
	Severity string
	message  string
}

func (e *KinesumerError) Error() string {
	return e.message
}

// If Err == nil then everything else is set
type KinesisRecord struct {
	// This contains:
	//   Data []byte
	//   PartitionKey *string
	//   SequenceNumber *string
	kinesis.Record
	ShardID            *string
	Sync               chan<- *KinesisRecord
	MillisBehindLatest int64
	// May or may not be a KinesumerError
	Err error
}

func (s *KinesisRecord) Done() {
	s.Sync <- s
}

type ShardStateSync interface {
	DoneC() chan<- *KinesisRecord
	Begin(chan<- *KinesisRecord) error
	End()
	GetStartSequence(shardID *string) *string
	Sync()
}

type ShardStateSyncOptions struct {
	Ticker <-chan time.Time
}
