package tech.khorost.springvsquarkus.quarkus;

import jakarta.ws.rs.GET;
import jakarta.ws.rs.Path;
import jakarta.ws.rs.Produces;
import jakarta.ws.rs.core.MediaType;

/**
 * Quarkus сторона сравнения spring-vs-quarkus: минимальный REST-ресурс,
 * эквивалентный spring/App.java. GET /hello -> "hello".
 * GET /health отдаёт smallrye-health (см. application.properties, root-path).
 */
@Path("/hello")
public class HelloResource {

    @GET
    @Produces(MediaType.TEXT_PLAIN)
    public String hello() {
        return "hello";
    }
}
