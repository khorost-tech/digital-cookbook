-- Итог wrk/wrk2 одной строкой JSON: число ответов, перцентили и максимум (мс).
done = function(summary, latency, requests)
  io.write(string.format(
    'REPORT {"requests": %d, "errors": %d, "duration_s": %.3f, "p50_ms": %.3f, "p90_ms": %.3f, "p99_ms": %.3f, "max_ms": %.3f}\n',
    summary.requests, summary.errors.connect + summary.errors.read + summary.errors.write
      + summary.errors.status + summary.errors.timeout,
    summary.duration / 1e6, latency:percentile(50) / 1e3, latency:percentile(90) / 1e3, latency:percentile(99) / 1e3, latency.max / 1e3))
end
