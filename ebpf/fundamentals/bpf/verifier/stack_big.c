// expect: clang — буфер больше 512 байт стека: отказ ещё при компиляции
#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>
char LICENSE[] SEC("license") = "GPL";
struct { __uint(type, BPF_MAP_TYPE_ARRAY); __uint(max_entries, 1); __type(key, __u32); __type(value, __u64); } m SEC(".maps");
SEC("tracepoint/syscalls/sys_enter_getppid")
int prog(void *ctx)
{
	volatile char buf[600];
	__u32 k = 0;
	for (int i = 0; i < 600; i++)
		buf[i] = i;
	__u64 *v = bpf_map_lookup_elem(&m, &k);
	if (v)
		*v += buf[599];
	return 0;
}
