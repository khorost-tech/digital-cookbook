//go:build integration

package locking

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	clientv3 "go.etcd.io/etcd/client/v3"
)

func startEtcd(t *testing.T) *clientv3.Client {
	t.Helper()
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        "gcr.io/etcd-development/etcd:v3.5.16",
		ExposedPorts: []string{"2379/tcp"},
		Cmd: []string{
			"/usr/local/bin/etcd",
			"--advertise-client-urls", "http://0.0.0.0:2379",
			"--listen-client-urls", "http://0.0.0.0:2379",
		},
		WaitingFor: wait.ForLog("ready to serve client requests"),
	}
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req, Started: true,
	})
	if err != nil {
		t.Fatalf("старт etcd: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	endpoint, err := ctr.PortEndpoint(ctx, "2379/tcp", "http")
	if err != nil {
		t.Fatalf("endpoint: %v", err)
	}
	cli, err := clientv3.New(clientv3.Config{Endpoints: []string{endpoint}, DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("клиент etcd: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// TestEtcdLeaderElectionFencing — два кандидата по очереди становятся лидером;
// revision ключа лидера монотонно растёт и служит fencing-токеном.
func TestEtcdLeaderElectionFencing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cli := startEtcd(t)
	res := &FencedResource{}

	// Кандидат 1 выигрывает выборы и получает токен-revision.
	l1, err := NewEtcdLeader(cli, "/svc/leader", 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := l1.Campaign(ctx, "c1"); err != nil {
		t.Fatalf("c1 campaign: %v", err)
	}
	tok1, err := l1.FencingToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Write(tok1, "by-c1"); err != nil {
		t.Fatalf("запись лидера c1 (токен %d): %v", tok1, err)
	}

	// c1 слагает полномочия; c2 становится лидером с большим токеном.
	if err := l1.Resign(ctx); err != nil {
		t.Fatalf("c1 resign: %v", err)
	}
	l2, err := NewEtcdLeader(cli, "/svc/leader", 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := l2.Campaign(ctx, "c2"); err != nil {
		t.Fatalf("c2 campaign: %v", err)
	}
	tok2, err := l2.FencingToken(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if tok2 <= tok1 {
		t.Fatalf("revision-токен не вырос: c1=%d c2=%d", tok1, tok2)
	}
	// Свежий лидер пишет; запоздавшая запись c1 со старым токеном отвергается.
	if err := res.Write(tok2, "by-c2"); err != nil {
		t.Fatalf("запись лидера c2: %v", err)
	}
	if err := res.Write(tok1, "c1-stale"); err == nil {
		t.Fatal("устаревшая запись c1 должна быть отвергнута fencing")
	}
	if got := res.Value(); got != "by-c2" {
		t.Fatalf("ресурс: %q вместо by-c2", got)
	}
	t.Logf("etcd leader election: c1 revision=%d → c2 revision=%d (fencing-токены)", tok1, tok2)

	_ = l2.Resign(ctx)
	_ = l1.Close()
	_ = l2.Close()
}
