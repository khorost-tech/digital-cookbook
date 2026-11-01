// expect: rejected — указатель из array-карты разыменован без проверки на NULL
#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>
char LICENSE[] SEC("license") = "GPL";
struct { __uint(type, BPF_MAP_TYPE_ARRAY); __uint(max_entries, 1); __type(key, __u32); __type(value, __u64); } m SEC(".maps");
SEC("tracepoint/syscalls/sys_enter_getppid")
int prog(void *ctx)
{
	__u32 k = 0;
	__u64 *v = bpf_map_lookup_elem(&m, &k);
	*v += 1;
	return 0;
}
