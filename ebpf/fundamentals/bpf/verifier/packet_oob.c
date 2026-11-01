// expect: rejected — чтение байта пакета без сравнения с data_end
#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>
char LICENSE[] SEC("license") = "GPL";
SEC("xdp")
int prog(struct xdp_md *ctx)
{
	unsigned char *data = (void *)(long)ctx->data;
	return data[12] == 0x08 ? XDP_PASS : XDP_DROP;
}
