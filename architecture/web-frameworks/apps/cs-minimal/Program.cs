// Минимальный: ASP.NET Core Minimal API — маршруты-лямбды, привязка параметров, JSON через System.Text.Json.
var app = WebApplication.CreateBuilder(args).Build();
app.MapGet("/items/{id:int}", (int id) => Results.Ok(new { id, name = "item" }));
app.MapPost("/items", (Item item) => Results.Created("/items/1", item));
app.MapGet("/boom", string () => throw new InvalidOperationException("boom: секрет в сообщении marker-7f3a9c"));
app.Run("http://0.0.0.0:8080");

record Item(string Name, int Qty);
