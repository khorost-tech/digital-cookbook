package main

import (
	"context"
	"fmt"
	"log"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// phasePolicy — Step 1 брифа: MinIO + storage_configuration. Проверяет
// живьём, что ClickHouse увидел s3-диск (../config/storage.xml) и storage
// policy hot_cold (2 тома: hot=default, cold=s3) через system.disks /
// system.storage_policies — не просто "конфиг не уронил сервер при
// старте", а конкретные строки в этих системных таблицах.
func phasePolicy(ctx context.Context, ch clickhouse.Conn) {
	fmt.Println("\n=== MinIO + storage_configuration: system.disks / system.storage_policies (Step 1 брифа) ===")

	disks, err := listDisks(ctx, ch)
	if err != nil {
		log.Fatalf("list disks: %v", err)
	}
	fmt.Println("[policy] system.disks:")
	var s3Disk *diskInfo
	for i := range disks {
		d := disks[i]
		fmt.Printf("[policy]   name=%s type=%s path=%s\n", d.name, d.diskType, d.path)
		if d.name == "s3" {
			s3Disk = &disks[i]
		}
	}
	assertFailFast(s3Disk != nil, "system.disks содержит диск 's3' (MinIO)")

	policies, err := listStoragePolicies(ctx, ch)
	if err != nil {
		log.Fatalf("list storage policies: %v", err)
	}
	fmt.Println("[policy] system.storage_policies:")
	hotColdVolumes := map[string]string{}
	for _, p := range policies {
		fmt.Printf("[policy]   policy_name=%s volume_name=%s volume_priority=%d disks=%s\n", p.policyName, p.volumeName, p.volumePriority, p.disks)
		if p.policyName == "hot_cold" {
			hotColdVolumes[p.volumeName] = p.disks
		}
	}
	assertFailFast(len(hotColdVolumes) == 2, "storage policy 'hot_cold' содержит РОВНО 2 тома (факт: %d)", len(hotColdVolumes))
	hotDisks, hasHot := hotColdVolumes["hot"]
	coldDisks, hasCold := hotColdVolumes["cold"]
	assertFailFast(hasHot && hasCold, "storage policy 'hot_cold' содержит тома 'hot' и 'cold' (факт: hot=%v cold=%v)", hasHot, hasCold)
	assertFailFast(hasCold && coldDisks == "s3", "том 'cold' policy hot_cold указывает на диск 's3' (факт: %s)", coldDisks)
	assertFailFast(hasHot && hotDisks == "default", "том 'hot' policy hot_cold указывает на диск 'default' (факт: %s)", hotDisks)
}

type diskInfo struct {
	name     string
	diskType string
	path     string
}

func listDisks(ctx context.Context, ch clickhouse.Conn) ([]diskInfo, error) {
	rows, err := ch.Query(ctx, "SELECT name, type, path FROM system.disks ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []diskInfo
	for rows.Next() {
		var d diskInfo
		if err := rows.Scan(&d.name, &d.diskType, &d.path); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

type policyInfo struct {
	policyName     string
	volumeName     string
	volumePriority uint64
	disks          string
}

func listStoragePolicies(ctx context.Context, ch clickhouse.Conn) ([]policyInfo, error) {
	rows, err := ch.Query(ctx, "SELECT policy_name, volume_name, volume_priority, arrayStringConcat(disks, ',') FROM system.storage_policies ORDER BY policy_name, volume_priority")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []policyInfo
	for rows.Next() {
		var p policyInfo
		if err := rows.Scan(&p.policyName, &p.volumeName, &p.volumePriority, &p.disks); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
