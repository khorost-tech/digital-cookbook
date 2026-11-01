// expect: rejected — ключ на стеке не инициализирован
#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>
char LICENSE[] SEC("license") = "GPL";
struct { __uint(type, BPF_MAP_TYPE_HASH); __uint(max_entries, 16); __type(key, __u64); __type(value, __u64); } m SEC(".maps");
SEC("tracepoint/syscalls/sys_enter_getppid")
int prog(void *ctx)
{
	__u64 k;
	__u64 one = 1;
	bpf_map_update_elem(&m, &k, &one, BPF_ANY);
	return 0;
}
