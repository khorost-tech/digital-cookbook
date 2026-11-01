// Ось CO-RE: программа, написанная под ядра до 5.14, — читает task_struct->state
// без проверки, есть ли такое поле. В ядре стенда поля нет.
#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_core_read.h>

char LICENSE[] SEC("license") = "GPL";

volatile const __u32 target_tgid = 0;

struct task_struct___old {
	long state;
} __attribute__((preserve_access_index));

struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__uint(max_entries, 1);
	__type(key, __u32);
	__type(value, __s64);
} out SEC(".maps");

SEC("tracepoint/syscalls/sys_enter_getppid")
int core_state_old(void *ctx)
{
	__u32 k = 0;
	if ((bpf_get_current_pid_tgid() >> 32) != target_tgid)
		return 0;
	struct task_struct___old *t = (void *)bpf_get_current_task();
	__s64 v = BPF_CORE_READ(t, state);
	bpf_map_update_elem(&out, &k, &v, BPF_ANY);
	return 0;
}
