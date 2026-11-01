// Ось CO-RE: поля task_struct читаются через релокации — смещение поля берётся
// из BTF ядра при загрузке, а не из заголовка при компиляции.
// vmlinux.h генерируется из BTF ядра стенда (bpftool btf dump) при сборке.
#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_core_read.h>

char LICENSE[] SEC("license") = "GPL";

volatile const __u32 target_tgid = 0;

/* task_struct ядер до 5.14: поле state, позже переименовано в __state */
struct task_struct___old {
	long state;
} __attribute__((preserve_access_index));

enum { SLOT_TGID, SLOT_STATE, SLOT_BRANCH, SLOTS };

struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__uint(max_entries, SLOTS);
	__type(key, __u32);
	__type(value, __s64);
} out SEC(".maps");

static __always_inline void put(__u32 slot, __s64 val)
{
	bpf_map_update_elem(&out, &slot, &val, BPF_ANY);
}

SEC("tracepoint/syscalls/sys_enter_getppid")
int core_tgid(void *ctx)
{
	if ((bpf_get_current_pid_tgid() >> 32) != target_tgid)
		return 0;
	struct task_struct *t = (void *)bpf_get_current_task();
	put(SLOT_TGID, BPF_CORE_READ(t, tgid));
	return 0;
}

/* переносимо: какое из двух полей есть в этом ядре, решается при загрузке */
SEC("tracepoint/syscalls/sys_enter_getppid")
int core_state_guarded(void *ctx)
{
	if ((bpf_get_current_pid_tgid() >> 32) != target_tgid)
		return 0;
	struct task_struct *t = (void *)bpf_get_current_task();
	if (bpf_core_field_exists(((struct task_struct___old *)t)->state)) {
		put(SLOT_STATE, BPF_CORE_READ((struct task_struct___old *)t, state));
		put(SLOT_BRANCH, 0);
	} else {
		put(SLOT_STATE, BPF_CORE_READ(t, __state));
		put(SLOT_BRANCH, 1);
	}
	return 0;
}
