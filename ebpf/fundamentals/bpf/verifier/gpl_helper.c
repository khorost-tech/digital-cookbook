// expect: rejected — помощник только для GPL-совместимых программ, лицензия программы другая
#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>
char LICENSE[] SEC("license") = "Proprietary";
SEC("tracepoint/syscalls/sys_enter_getppid")
int prog(void *ctx)
{
	char buf[8];
	bpf_probe_read_kernel(buf, sizeof(buf), ctx);
	return buf[0];
}
