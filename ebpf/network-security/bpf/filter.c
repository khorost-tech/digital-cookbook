// Ось XDP/tc: список блокировки адресов источника в hash-карте; одна логика
// на двух точках — XDP (до сетевого стека) и tc ingress (после разбора в skb).
// Счётчики пропущенных и отброшенных IPv4-пакетов — per-CPU, сумму считает загрузчик.
#include <linux/bpf.h>
#include <linux/if_ether.h>
#include <linux/ip.h>
#include <linux/pkt_cls.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_endian.h>

char LICENSE[] SEC("license") = "GPL";

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 1024);
	__type(key, __u32);   /* адрес источника, порядок байт сети */
	__type(value, __u8);
} blocklist SEC(".maps");

enum { PASSED, DROPPED, SLOTS };

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(max_entries, SLOTS);
	__type(key, __u32);
	__type(value, __u64);
} counters SEC(".maps");

static __always_inline void count(__u32 slot)
{
	__u64 *v = bpf_map_lookup_elem(&counters, &slot);
	if (v)
		*v += 1;
}

/* 1 — отбросить, 0 — пропустить; не IPv4 — пропустить без счёта */
static __always_inline int verdict(void *data, void *data_end)
{
	struct ethhdr *eth = data;
	if ((void *)(eth + 1) > data_end || eth->h_proto != bpf_htons(ETH_P_IP))
		return -1;
	struct iphdr *ip = (void *)(eth + 1);
	if ((void *)(ip + 1) > data_end)
		return -1;
	__u32 src = ip->saddr;
	if (bpf_map_lookup_elem(&blocklist, &src)) {
		count(DROPPED);
		return 1;
	}
	count(PASSED);
	return 0;
}

SEC("xdp")
int xdp_filter(struct xdp_md *ctx)
{
	return verdict((void *)(long)ctx->data, (void *)(long)ctx->data_end) == 1 ? XDP_DROP : XDP_PASS;
}

SEC("tc")
int tc_filter(struct __sk_buff *skb)
{
	return verdict((void *)(long)skb->data, (void *)(long)skb->data_end) == 1 ? TC_ACT_SHOT : TC_ACT_OK;
}
