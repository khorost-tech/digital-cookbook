// expect: rejected — граница цикла из карты (гипотеза до пробы; clang свернул цикл в «*v <<= 1», verifier цикла не видит)
#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>
char LICENSE[] SEC("license") = "GPL";
struct { __uint(type, BPF_MAP_TYPE_ARRAY); __uint(max_entries, 1); __type(key, __u32); __type(value, __u64); } m SEC(".maps");
SEC("tracepoint/syscalls/sys_enter_getppid")
int prog(void *ctx)
{
	__u32 k = 0;
	__u64 *v = bpf_map_lookup_elem(&m, &k);
	if (!v)
		return 0;
	__u64 n = *v;
	for (__u64 i = 0; i < n; i++)
		*v += 1;
	return 0;
}
