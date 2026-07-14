package tech.khorost.redis;

import io.lettuce.core.ClientOptions;
import io.lettuce.core.RedisClient;
import io.lettuce.core.RedisURI;
import io.lettuce.core.TimeoutOptions;
import io.lettuce.core.api.StatefulRedisConnection;
import io.lettuce.core.api.sync.RedisCommands;
import io.lettuce.core.cluster.ClusterClientOptions;
import io.lettuce.core.cluster.ClusterTopologyRefreshOptions;
import io.lettuce.core.cluster.RedisClusterClient;
import io.lettuce.core.cluster.api.StatefulRedisClusterConnection;
import io.lettuce.core.cluster.api.sync.RedisAdvancedClusterCommands;

import java.time.Duration;
import java.time.LocalTime;
import java.time.temporal.ChronoUnit;
import java.util.Arrays;
import java.util.List;

/**
 * Демонстрационный клиент Lettuce: read-through кэш профиля в бесконечном цикле.
 * REDIS_MODE=cluster|sentinel. Клиент не падает на ошибке — логирует и продолжает,
 * чтобы было видно окно недоступности и reconnect при failover.
 */
public class App {

    private static final String KEY = "user:42";

    public static void main(String[] args) throws InterruptedException {
        String mode = env("REDIS_MODE", "cluster");
        System.out.println("[java] mode=" + mode + " — read-through цикл, Ctrl+C для выхода");
        if (mode.equals("sentinel")) {
            runSentinel();
        } else {
            runCluster();
        }
    }

    private static void runCluster() throws InterruptedException {
        List<RedisURI> seeds = Arrays.stream(env("REDIS_ADDRS", "127.0.0.1:6379").split(","))
                .map(App::uri)
                .toList();

        RedisClusterClient client = RedisClusterClient.create(seeds);
        // Без явного refresh Lettuce после failover ходит по устаревшей карте слотов.
        ClusterTopologyRefreshOptions topology = ClusterTopologyRefreshOptions.builder()
                .enablePeriodicRefresh(Duration.ofSeconds(10))
                .enableAllAdaptiveRefreshTriggers()
                .build();
        client.setOptions(ClusterClientOptions.builder()
                .topologyRefreshOptions(topology)
                .timeoutOptions(TimeoutOptions.enabled(Duration.ofSeconds(1)))
                .build());

        StatefulRedisClusterConnection<String, String> conn = client.connect();
        RedisAdvancedClusterCommands<String, String> cmd = conn.sync();

        while (true) {
            try {
                String val = cmd.get(KEY); // null == промах кэша
                if (val == null) {
                    String nv = "profile@" + LocalTime.now().truncatedTo(ChronoUnit.SECONDS);
                    cmd.setex(KEY, 30, nv);
                    System.out.println("[java] MISS -> set " + nv);
                } else {
                    System.out.println("[java] HIT " + val);
                }
            } catch (Exception e) {
                System.out.println("[java] ERROR: " + e.getMessage());
            }
            Thread.sleep(1000);
        }
    }

    private static void runSentinel() throws InterruptedException {
        RedisURI.Builder builder = RedisURI.builder();
        for (String s : env("REDIS_SENTINELS", "127.0.0.1:26379").split(",")) {
            String[] hp = s.split(":");
            builder.withSentinel(hp[0], Integer.parseInt(hp[1]));
        }
        builder.withSentinelMasterId(env("REDIS_MASTER", "mymaster"));
        RedisURI uri = builder.build();

        RedisClient client = RedisClient.create(uri);
        client.setOptions(ClientOptions.builder()
                .timeoutOptions(TimeoutOptions.enabled(Duration.ofSeconds(1)))
                .build());

        // Lettuce сам резолвит primary через Sentinel и переключается при failover.
        StatefulRedisConnection<String, String> conn = client.connect();
        RedisCommands<String, String> cmd = conn.sync();

        while (true) {
            try {
                String val = cmd.get(KEY);
                if (val == null) {
                    String nv = "profile@" + LocalTime.now().truncatedTo(ChronoUnit.SECONDS);
                    cmd.setex(KEY, 30, nv);
                    System.out.println("[java] MISS -> set " + nv);
                } else {
                    System.out.println("[java] HIT " + val);
                }
            } catch (Exception e) {
                System.out.println("[java] ERROR: " + e.getMessage());
            }
            Thread.sleep(1000);
        }
    }

    private static RedisURI uri(String hostPort) {
        String[] hp = hostPort.split(":");
        return RedisURI.create(hp[0], Integer.parseInt(hp[1]));
    }

    private static String env(String key, String def) {
        String v = System.getenv(key);
        return (v == null || v.isBlank()) ? def : v;
    }
}
