// Демо opensearch-java 3.9.0: типизированный index + bulk + разбор partial failures.
// Проверено на OpenSearch 3.5.0. demo-креды — только для локального стенда.
import org.apache.hc.client5.http.auth.AuthScope;
import org.apache.hc.client5.http.auth.UsernamePasswordCredentials;
import org.apache.hc.client5.http.impl.auth.BasicCredentialsProvider;
import org.apache.hc.client5.http.impl.nio.PoolingAsyncClientConnectionManagerBuilder;
import org.apache.hc.client5.http.ssl.ClientTlsStrategyBuilder;
import org.apache.hc.client5.http.ssl.NoopHostnameVerifier;
import org.apache.hc.core5.http.HttpHost;
import org.apache.hc.core5.ssl.SSLContextBuilder;
import org.apache.hc.core5.reactor.ssl.TlsDetails;
import org.opensearch.client.json.jackson.JacksonJsonpMapper;
import org.opensearch.client.opensearch.OpenSearchClient;
import org.opensearch.client.opensearch.core.BulkRequest;
import org.opensearch.client.opensearch.core.BulkResponse;
import org.opensearch.client.opensearch.core.IndexResponse;
import org.opensearch.client.opensearch.core.bulk.BulkOperation;
import org.opensearch.client.transport.OpenSearchTransport;
import org.opensearch.client.transport.httpclient5.ApacheHttpClient5TransportBuilder;

import javax.net.ssl.SSLContext;
import java.util.ArrayList;
import java.util.List;

public class Main {
  public record LogDoc(String ts, String level, String service, String message, int status) {}

  public static void main(String[] args) throws Exception {
    // По умолчанию — localhost (клиент запускается на хосте рядом со стендом). Для запуска
    // ИЗ контейнера, которому нужен хостовый стенд, задайте OS_HOST=host.docker.internal.
    String osHost = System.getenv().getOrDefault("OS_HOST", "localhost");
    HttpHost host = new HttpHost("https", osHost, 9214);
    BasicCredentialsProvider cp = new BasicCredentialsProvider();
    cp.setCredentials(new AuthScope(host), new UsernamePasswordCredentials("admin", "IngDemo#2026".toCharArray()));
    SSLContext ssl = SSLContextBuilder.create().loadTrustMaterial(null, (chain, authType) -> true).build(); // demo only

    OpenSearchTransport transport = ApacheHttpClient5TransportBuilder.builder(host)
        .setMapper(new JacksonJsonpMapper())
        .setHttpClientConfigCallback(hc -> {
          var tls = ClientTlsStrategyBuilder.create().setSslContext(ssl)
              .setHostnameVerifier(NoopHostnameVerifier.INSTANCE)
              .setTlsDetailsFactory(sslEngine -> new TlsDetails(sslEngine.getSession(), sslEngine.getApplicationProtocol()))
              .build();
          var cm = PoolingAsyncClientConnectionManagerBuilder.create().setTlsStrategy(tls).build();
          return hc.setDefaultCredentialsProvider(cp).setConnectionManager(cm);
        }).build();
    OpenSearchClient client = new OpenSearchClient(transport);

    // single index (типизированный объект)
    IndexResponse ir = client.index(i -> i.index("app-logs-java")
        .document(new LogDoc("2026-07-03T10:00:00Z", "error", "service-a", "disk full", 500)));
    System.out.println("single index -> result=" + ir.result() + " id=" + ir.id());

    // bulk
    List<LogDoc> docs = List.of(
        new LogDoc("2026-07-03T10:01:00Z", "info", "service-a", "ok", 200),
        new LogDoc("2026-07-03T10:02:00Z", "warn", "service-b", "retry", 429));
    List<BulkOperation> ops = new ArrayList<>();
    for (LogDoc d : docs) {
      ops.add(BulkOperation.of(op -> op.index(idx -> idx.index("app-logs-java").document(d))));
    }
    BulkResponse br = client.bulk(new BulkRequest.Builder().operations(ops).build());
    // HTTP-успех != все записаны: проверяем errors() и каждый item.
    System.out.println("bulk -> errors=" + br.errors() + ", items=" + br.items().size());
    for (int i = 0; i < br.items().size(); i++) {
      var it = br.items().get(i);
      System.out.println("  item " + i + " status=" + it.status() + (it.error() != null ? " ERROR " + it.error().type() : " ok"));
    }
    transport.close();
  }
}
