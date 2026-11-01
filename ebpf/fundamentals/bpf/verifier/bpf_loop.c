// expect: accepted — тот же цикл, что в loop_unbounded.c (граница n из карты, не больше LOOP_MAX; тело — XOR bpf_get_prandom_u32), через помощник bpf_loop
#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>
char LICENSE[] SEC("license") = "GPL";
#define LOOP_MAX (1u << 20) /* та же граница, что в loop_unbounded.c; меньше предела помощника (1 << 23) */
enum { IN_N, OUT_ITERS, OUT_RET, OUT_ACC, SLOTS };
struct { __uint(type, BPF_MAP_TYPE_ARRAY); __uint(max_entries, SLOTS); __type(key, __u32); __type(value, __u64); } m SEC(".maps");
struct loop_ctx { __u64 acc, iters; };
static long step(__u32 i, void *data)
{
	struct loop_ctx *c = data;
	c->acc ^= bpf_get_prandom_u32();
	c->iters++;
	return 0;
}
SEC("tracepoint/syscalls/sys_enter_getppid")
int prog(void *ctx)
{
	__u32 k = IN_N;
	__u64 *v = bpf_map_lookup_elem(&m, &k);
	if (!v)
		return 0;
	__u32 n = *v > LOOP_MAX ? LOOP_MAX : (__u32)*v; /* сужение до u32 — после проверки границы */
	struct loop_ctx c = {};
	long ret = bpf_loop(n, step, &c, 0);
	__u64 r = (__u64)ret;
	k = OUT_RET;
	bpf_map_update_elem(&m, &k, &r, BPF_ANY);
	if (ret < 0) /* ошибка помощника — результат вычисления не публикуется */
		return 0;
	k = OUT_ITERS;
	bpf_map_update_elem(&m, &k, &c.iters, BPF_ANY);
	k = OUT_ACC;
	bpf_map_update_elem(&m, &k, &c.acc, BPF_ANY);
	return 0;
}
