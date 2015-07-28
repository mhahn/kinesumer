package kinesumer

import (
	"errors"
	"math/rand"
	"sync"
	"time"

	"github.com/garyburd/redigo/redis"
	k "github.com/remind101/kinesumer/interface"
)

type RedisCheckpointer struct {
	heads       map[string]string
	acquired    map[string]k.Unit
	c           chan *k.KinesisRecord
	recs        chan<- *k.KinesisRecord
	mut         sync.Mutex
	pool        *redis.Pool
	redisPrefix string
	savePeriod  time.Duration
	alivePeriod time.Duration
	wg          sync.WaitGroup
	modified    bool
	lock        string
}

type RedisCheckpointerOptions struct {
	SavePeriod  time.Duration
	AlivePeriod time.Duration
	RedisURL    string
	RedisPrefix string
}

var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

func randString(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func NewRedisCheckpointer(opt *RedisCheckpointerOptions) (*RedisCheckpointer, error) {
	redisPool, err := NewRedisPool(opt.RedisURL)
	if err != nil {
		return nil, err
	}
	return &RedisCheckpointer{
		heads:       make(map[string]string),
		acquired:    make(map[string]k.Unit, 0),
		c:           make(chan *k.KinesisRecord),
		mut:         sync.Mutex{},
		pool:        redisPool,
		redisPrefix: opt.RedisPrefix,
		savePeriod:  opt.SavePeriod,
		alivePeriod: opt.AlivePeriod,
		modified:    true,
		lock:        randString(255),
	}, nil
}

func (r *RedisCheckpointer) DoneC() chan<- *k.KinesisRecord {
	return r.c
}

func (r *RedisCheckpointer) Sync() {
	r.mut.Lock()
	defer r.mut.Unlock()
	if len(r.heads) > 0 && r.modified {
		conn := r.pool.Get()
		defer conn.Close()
		if _, err := conn.Do("HMSET", redis.Args{r.redisPrefix + ":sequence"}.AddFlat(r.heads)...); err != nil {
			r.recs <- &k.KinesisRecord{
				Err: err,
			}
		}
		r.modified = false
	}
}

func (r *RedisCheckpointer) RunShardSync() {
	saveTicker := time.NewTicker(r.savePeriod).C
	ttlTicker := time.NewTicker(r.alivePeriod * 4 / 5).C
loop:
	for {
		select {
		case <-saveTicker:
			r.Sync()
		case <-ttlTicker:
			r.Reacquire()
		case state, ok := <-r.c:
			if !ok {
				break loop
			}
			r.mut.Lock()
			r.heads[*state.ShardID] = *state.Record.SequenceNumber
			r.modified = true
			r.mut.Unlock()
		}
	}
	r.Sync()
	r.wg.Done()
}

func (r *RedisCheckpointer) Begin(recs chan<- *k.KinesisRecord) error {
	r.recs = recs
	conn := r.pool.Get()
	defer conn.Close()
	res, err := conn.Do("HGETALL", r.redisPrefix+":sequence")
	r.heads, err = redis.StringMap(res, err)
	if err != nil {
		return err
	}

	r.wg.Add(1)
	go r.RunShardSync()
	return nil
}

func (r *RedisCheckpointer) End() {
	close(r.c)
	r.wg.Wait()
}

func (r *RedisCheckpointer) GetStartSequence(shardID *string) *string {
	val, ok := r.heads[*shardID]
	if ok {
		return &val
	} else {
		return nil
	}
}

func (r *RedisCheckpointer) TryAcquire(shardID *string) error {
	conn := r.pool.Get()
	defer conn.Close()
	if _, exists := r.acquired[*shardID]; exists {
		return errors.New("Lock already acquired by this process")
	}
	res, err := conn.Do("SET", r.redisPrefix+".lock."+*shardID, r.lock, "PX", r.alivePeriod/time.Millisecond, "NX")
	if err != nil {
		return err
	}
	if res != "OK" {
		return errors.New("Failed to acquire lock")
	}
	r.acquired[*shardID] = k.Unit{}
	return nil
}

func (r *RedisCheckpointer) Reacquire() {
	conn := r.pool.Get()
	defer conn.Close()
	for shardID := range r.acquired {
		res, err := conn.Do("PEXPIRE", r.redisPrefix+".lock."+shardID, r.alivePeriod/time.Millisecond, "NX")
		if err != nil || res != "OK" {
			if err != nil {
				r.recs <- &k.KinesisRecord{
					Err: err,
				}
			}
			err = r.TryAcquire(&shardID)
			if err != nil {
				r.recs <- &k.KinesisRecord{
					Err: err,
				}
			}
		}
	}
}

func (r *RedisCheckpointer) Release(shardID *string) error {
	conn := r.pool.Get()
	defer conn.Close()
	delete(r.acquired, *shardID)
	key := r.redisPrefix + ".lock." + *shardID
	res, err := redis.String(conn.Do("GET", key))
	if err != nil {
		return err
	}
	if res != r.lock {
		return errors.New("Bad lock")
	}
	_, err = conn.Do("DEL", key)
	if err != nil {
		return err
	}
	return nil
}
