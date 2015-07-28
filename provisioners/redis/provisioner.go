package redisprovisioner

import (
	"errors"
	"sync"
	"time"

	"github.com/garyburd/redigo/redis"
	"github.com/pborman/uuid" // Exported from code.google.com/p/go-uuid/uuid
	k "github.com/remind101/kinesumer/interface"
)

type Provisioner struct {
	acquired      map[string]bool
	heartbeats    map[string]time.Time
	heartbeatsMut sync.RWMutex
	ttl           time.Duration
	pool          *redis.Pool
	redisPrefix   string
	lock          string
}

func New(ttl time.Duration, redisPool *redis.Pool, prefix string) (*Provisioner, error) {
	return &Provisioner{
		acquired:    make(map[string]bool),
		ttl:         ttl,
		pool:        redisPool,
		redisPrefix: prefix,
		lock:        uuid.New(),
	}, nil
}

func (p *Provisioner) TryAcquire(shardID *string) error {
	conn := p.pool.Get()
	defer conn.Close()

	if p.acquired[*shardID] {
		return errors.New("Lock already acquired by this process")
	}

	res, err := conn.Do("SET", p.redisPrefix+":lock:"+*shardID, p.lock, "PX", p.ttl/time.Millisecond, "NX")
	if err != nil {
		return err
	}
	if res != "OK" {
		return errors.New("Failed to acquire lock")
	}

	p.acquired[*shardID] = true
	return nil
}

func (p *Provisioner) Release(shardID *string) error {
	conn := p.pool.Get()
	defer conn.Close()

	delete(p.acquired, *shardID)

	key := p.redisPrefix + ":lock:" + *shardID
	res, err := redis.String(conn.Do("GET", key))
	if err != nil {
		return err
	}
	if res != p.lock {
		return errors.New("Bad lock")
	}

	_, err = conn.Do("DEL", key)
	if err != nil {
		return err
	}

	return nil
}

func (p *Provisioner) Heartbeat(shardID string) error {
	var lastHeartbeat time.Time
	func() {
		p.heartbeatsMut.RLock()
		defer p.heartbeatsMut.RUnlock()
		lastHeartbeat = p.heartbeats[shardID]
	}()

	now := time.Now()

	if 2*(now.Sub(lastHeartbeat)) < p.ttl {
		return nil
	}

	conn := p.pool.Get()
	defer conn.Close()

	lockKey := p.redisPrefix + ":lock:" + shardID

	res, err := conn.Do("GET", lockKey)
	if err != nil {
		return err
	}

	lock, err := redis.String(res, err)
	if lock != p.lock {
		return k.NewKinesumerError(k.KinesumerEError, "Lock changed", nil)
	}

	res, err = conn.Do("PEXPIRE", lockKey, p.ttl/time.Millisecond)
	if err != nil {
		err := p.TryAcquire(&shardID)
		if err != nil {
			return err
		}
	}

	p.heartbeatsMut.Lock()
	defer p.heartbeatsMut.Unlock()
	p.heartbeats[shardID] = now

	return nil
}

func (p *Provisioner) TTL() time.Duration {
	return p.ttl
}
