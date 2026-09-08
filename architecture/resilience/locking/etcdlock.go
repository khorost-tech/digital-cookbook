package locking

import (
	"context"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

// EtcdLeader — лидерство через etcd: сессия на lease с TTL плюс выборы (Election).
// Кандидат, выигравший кампанию, — лидер, пока жива сессия; упал владелец или
// пропала сеть — lease истекает и лидерство переходит. В отличие от самодельного
// Redlock, здесь fencing даётся даром: ключ лидера несёт revision etcd, а она
// глобально монотонна — то есть уже готовый fencing-токен.
type EtcdLeader struct {
	session  *concurrency.Session
	election *concurrency.Election
}

// NewEtcdLeader открывает сессию с lease на ttlSeconds и создаёт выборы на prefix.
func NewEtcdLeader(cli *clientv3.Client, prefix string, ttlSeconds int) (*EtcdLeader, error) {
	s, err := concurrency.NewSession(cli, concurrency.WithTTL(ttlSeconds))
	if err != nil {
		return nil, err
	}
	return &EtcdLeader{session: s, election: concurrency.NewElection(s, prefix)}, nil
}

// Campaign блокируется, пока кандидат id не станет лидером (или не истечёт ctx).
func (e *EtcdLeader) Campaign(ctx context.Context, id string) error {
	return e.election.Campaign(ctx, id)
}

// FencingToken возвращает revision ключа текущего лидера — монотонный токен,
// который лидер несёт к ресурсу.
func (e *EtcdLeader) FencingToken(ctx context.Context) (uint64, error) {
	resp, err := e.election.Leader(ctx)
	if err != nil {
		return 0, err
	}
	return uint64(resp.Kvs[0].CreateRevision), nil
}

// Resign слагает лидерство, отдавая его следующему кандидату.
func (e *EtcdLeader) Resign(ctx context.Context) error {
	return e.election.Resign(ctx)
}

// Close закрывает сессию (освобождает lease).
func (e *EtcdLeader) Close() error {
	return e.session.Close()
}
