// Ось hooks: одно событие нагрузки — разные точки подключения. Каждая программа
// атомарно прибавляет 1 в свою ячейку counts, если событие пришло от процесса
// нагрузки (target_tgid задаёт загрузчик до загрузки программы).
#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>

char LICENSE[] SEC("license") = "GPL";

volatile const __u32 target_tgid = 0;

enum { TP_OPENAT, KPROBE_OPENAT2, FENTRY_OPENAT2, UPROBE_TARGET, URETPROBE_TARGET, SLOTS };

struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__uint(max_entries, SLOTS);
	__type(key, __u32);
	__type(value, __u64);
} counts SEC(".maps");

static __always_inline int hit(__u32 slot)
{
	if ((bpf_get_current_pid_tgid() >> 32) != target_tgid)
		return 0;
	__u64 *v = bpf_map_lookup_elem(&counts, &slot);
	if (v)
		__sync_fetch_and_add(v, 1);
	return 0;
}

SEC("tracepoint/syscalls/sys_enter_openat")
int tp_openat(void *ctx) { return hit(TP_OPENAT); }

SEC("kprobe/do_sys_openat2")
int kprobe_openat2(void *ctx) { return hit(KPROBE_OPENAT2); }

SEC("fentry/do_sys_openat2")
int fentry_openat2(void *ctx) { return hit(FENTRY_OPENAT2); }

SEC("uprobe")
int uprobe_target(void *ctx) { return hit(UPROBE_TARGET); }

SEC("uretprobe")
int uretprobe_target(void *ctx) { return hit(URETPROBE_TARGET); }
