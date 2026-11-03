package stand;

import static io.gatling.javaapi.core.CoreDsl.*;
import static io.gatling.javaapi.http.HttpDsl.*;

import io.gatling.javaapi.core.*;
import java.time.Duration;

/**
 * Ось model. mode=closed — constantConcurrentUsers(users): новый запрос только
 * после ответа; mode=open — constantUsersPerSec(rate): приход по расписанию.
 */
public class Model extends Simulation {
  String target = System.getProperty("target", "http://svc:8080");
  String path = System.getProperty("path", "/work");
  String mode = System.getProperty("mode", "open");
  int rate = Integer.getInteger("rate", 200);
  int users = Integer.getInteger("users", 2);
  Duration duration = Duration.ofSeconds(Integer.getInteger("seconds", 60));

  ScenarioBuilder open = scenario("work").exec(http("work").get(path));
  ScenarioBuilder closed = scenario("work").forever().on(exec(http("work").get(path)));

  {
    PopulationBuilder p = mode.equals("closed")
        ? closed.injectClosed(constantConcurrentUsers(users).during(duration))
        : open.injectOpen(constantUsersPerSec(rate).during(duration));
    setUp(p).protocols(http.baseUrl(target).shareConnections()).maxDuration(duration);
  }
}
