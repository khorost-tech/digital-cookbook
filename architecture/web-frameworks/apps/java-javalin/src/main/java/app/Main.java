// Минимальный: Javalin — тонкая обёртка над Jetty, обработчики-лямбды, JSON через Jackson.
package app;

import io.javalin.Javalin;
import java.util.Map;

public class Main {
    public record Item(String name, int qty) {}

    public static void main(String[] args) {
        Javalin.create(cfg -> {
            cfg.routes.get("/items/{id}", ctx -> ctx.json(Map.of("id", Integer.parseInt(ctx.pathParam("id")), "name", "item")));
            // контроль стенда: тот же маршрут со штатным типизированным валидатором Javalin — ошибка преобразования → 400
            cfg.routes.get("/typed/items/{id}", ctx -> ctx.json(Map.of("id", ctx.pathParamAsClass("id", Integer.class).get(), "name", "item")));
            cfg.routes.post("/items", ctx -> ctx.status(201).json(ctx.bodyAsClass(Item.class)));
            cfg.routes.get("/boom", ctx -> { throw new IllegalStateException("boom: секрет в сообщении marker-7f3a9c"); });
        }).start(8080);
    }
}
