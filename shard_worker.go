package kinesumer

import (
	"time"

	"github.com/aws/aws-sdk-go/service/kinesis"
)

type ShardWorker struct {
	kinesis         Kinesis
	shard           *kinesis.Shard
	checkpointer    Checkpointer
	stream          *string
	pollTime        int
	sequence        *string
	stop            <-chan Unit
	stopped         chan<- Unit
	c               chan *KinesisRecord
	GetRecordsLimit int64
}

func (s *ShardWorker) GetShardIterator(iteratorType string, sequence *string) (*string, error) {
	iter, err := s.kinesis.GetShardIterator(&kinesis.GetShardIteratorInput{
		ShardID:                s.shard.ShardID,
		ShardIteratorType:      &iteratorType,
		StartingSequenceNumber: sequence,
		StreamName:             s.stream,
	})
	if err != nil {
		return nil, err
	}
	return iter.ShardIterator, nil
}

func (s *ShardWorker) TryGetShardIterator(iteratorType string, sequence *string) *string {
	it, err := s.GetShardIterator(iteratorType, sequence)
	if err != nil {
		panic(err)
	}
	return it
}

func (s *ShardWorker) GetRecords(it *string) ([]*kinesis.Record, *string, int64, error) {
	resp, err := s.kinesis.GetRecords(&kinesis.GetRecordsInput{
		Limit:         &s.GetRecordsLimit,
		ShardIterator: it,
	})
	if err != nil {
		return nil, nil, 0, err
	}
	return resp.Records, resp.NextShardIterator, *resp.MillisBehindLatest, nil
}

func (s *ShardWorker) GetRecordsAndProcess(it, sequence *string) (cont bool, nextIt *string, nextSeq *string) {
	records, nextIt, lag, err := s.GetRecords(it)
	if err != nil || len(records) == 0 {
		if err != nil {
			s.c <- &KinesisRecord{
				ShardID:            s.shard.ShardID,
				MillisBehindLatest: lag,
				Err:                err,
			}
			nextIt = s.TryGetShardIterator("AFTER_SEQUENCE_NUMBER", sequence)
		}
		// GetRecords is not guaranteed to return records even if there are records to be read.
		// However, if our lag time behind the shard head is less than 3 seconds then there's probably
		// no records.
		if lag < 30000 /* milliseconds */ {
			select {
			case <-time.NewTimer(time.Duration(s.pollTime) * time.Millisecond).C:
			case <-s.stop:
				return true, nil, sequence
			}
		}
	} else {
		for _, rec := range records {
			s.c <- &KinesisRecord{
				Record:             *rec,
				ShardID:            s.shard.ShardID,
				CheckpointC:        s.checkpointer.DoneC(),
				MillisBehindLatest: lag,
			}
		}
		sequence = records[len(records)-1].SequenceNumber
	}
	return false, nextIt, sequence
}

func (s *ShardWorker) RunWorker() {
	defer func() {
		s.stopped <- Unit{}
	}()

	sequence := s.checkpointer.GetStartSequence(s.shard.ShardID)
	end := s.shard.SequenceNumberRange.EndingSequenceNumber
	var it *string
	if sequence == nil || len(*sequence) == 0 {
		sequence = s.shard.SequenceNumberRange.StartingSequenceNumber

		s.c <- &KinesisRecord{
			ShardID: s.shard.ShardID,
			Err: &KinesumerError{
				Severity: "info",
				message:  "Using TRIM_HORIZON",
			},
		}
		it = s.TryGetShardIterator("TRIM_HORIZON", nil)
	} else {
		it = s.TryGetShardIterator("AFTER_SEQUENCE_NUMBER", sequence)
	}

loop:
	for {
		if end != nil && *sequence == *end {
			s.c <- &KinesisRecord{
				ShardID: s.shard.ShardID,
				Err: &KinesumerError{
					Severity: "info",
					message:  "Shard has reached its end",
				},
			}
			break loop
		}

		select {
		case <-s.stop:
			break loop
		default:
			if brk, nextIt, seq := s.GetRecordsAndProcess(it, sequence); brk {
				break loop
			} else {
				it = nextIt
				sequence = seq
			}
		}
	}
}
