// Ось maps: N вызовов getppid нагрузки из K потоков на разных CPU — счёт в разных картах.
#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>

char LICENSE[] SEC("license") = "GPL";

volatile const __u32 target_tgid = 0;

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 1);
	__type(key, __u32);
	__type(value, __u64);
} hash_counts SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(max_entries, 1);
	__type(key, __u32);
	__type(value, __u64);
} percpu_counts SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_RINGBUF);
	__uint(max_entries, 4096); /* размер задаёт загрузчик до загрузки */
} events SEC(".maps");

/* отказы bpf_ringbuf_reserve: событие не попало в буфер */
struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__uint(max_entries, 1);
	__type(key, __u32);
	__type(value, __u64);
} ring_drops SEC(".maps");

static __always_inline int mine(void)
{
	return (bpf_get_current_pid_tgid() >> 32) == target_tgid;
}

/* прочитать, прибавить, записать — без атомарности: два CPU читают одно значение */
SEC("tracepoint/syscalls/sys_enter_getppid")
int hash_racy(void *ctx)
{
	__u32 k = 0;
	if (!mine())
		return 0;
	__u64 *v = bpf_map_lookup_elem(&hash_counts, &k);
	if (v)
		*v = *v + 1;
	return 0;
}

SEC("tracepoint/syscalls/sys_enter_getppid")
int hash_atomic(void *ctx)
{
	__u32 k = 0;
	if (!mine())
		return 0;
	__u64 *v = bpf_map_lookup_elem(&hash_counts, &k);
	if (v)
		__sync_fetch_and_add(v, 1);
	return 0;
}

/* у каждого CPU своя ячейка — гонки нет, сумму считает user-space */
SEC("tracepoint/syscalls/sys_enter_getppid")
int percpu(void *ctx)
{
	__u32 k = 0;
	if (!mine())
		return 0;
	__u64 *v = bpf_map_lookup_elem(&percpu_counts, &k);
	if (v)
		*v += 1;
	return 0;
}

SEC("tracepoint/syscalls/sys_enter_getppid")
int ring(void *ctx)
{
	__u32 k = 0;
	if (!mine())
		return 0;
	__u64 *e = bpf_ringbuf_reserve(&events, sizeof(__u64), 0);
	if (!e) {
		__u64 *d = bpf_map_lookup_elem(&ring_drops, &k);
		if (d)
			__sync_fetch_and_add(d, 1);
		return 0;
	}
	*e = bpf_get_smp_processor_id();
	bpf_ringbuf_submit(e, 0);
	return 0;
}
