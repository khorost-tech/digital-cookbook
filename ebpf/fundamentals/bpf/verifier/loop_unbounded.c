// expect: rejected — граница цикла из карты (не больше LOOP_MAX), в теле вызов помощника: свернуть цикл компилятор не может
#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>
char LICENSE[] SEC("license") = "GPL";
#define LOOP_MAX (1u << 20) /* та же граница, что в bpf_loop.c; меньше предела помощника bpf_loop (1 << 23) */
enum { IN_N, OUT_ITERS, OUT_RET, OUT_ACC, SLOTS };
struct { __uint(type, BPF_MAP_TYPE_ARRAY); __uint(max_entries, SLOTS); __type(key, __u32); __type(value, __u64); } m SEC(".maps");
SEC("tracepoint/syscalls/sys_enter_getppid")
int prog(void *ctx)
{
	__u32 k = IN_N;
	__u64 *v = bpf_map_lookup_elem(&m, &k);
	if (!v)
		return 0;
	__u32 n = *v > LOOP_MAX ? LOOP_MAX : (__u32)*v;
	__u64 acc = 0, iters = 0;
	for (__u32 i = 0; i < n; i++) {
		acc ^= bpf_get_prandom_u32();
		iters++;
	}
	k = OUT_ITERS;
	bpf_map_update_elem(&m, &k, &iters, BPF_ANY);
	k = OUT_ACC;
	bpf_map_update_elem(&m, &k, &acc, BPF_ANY);
	return 0;
}
