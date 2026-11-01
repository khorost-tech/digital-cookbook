// expect: accepted — то же чтение после проверки границы пакета
#include <linux/bpf.h>
#include <bpf/bpf_helpers.h>
char LICENSE[] SEC("license") = "GPL";
SEC("xdp")
int prog(struct xdp_md *ctx)
{
	unsigned char *data = (void *)(long)ctx->data;
	unsigned char *end = (void *)(long)ctx->data_end;
	if (data + 13 > end)
		return XDP_PASS;
	return data[12] == 0x08 ? XDP_PASS : XDP_DROP;
}
