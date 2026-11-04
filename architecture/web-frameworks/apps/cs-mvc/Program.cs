// Полный: ASP.NET Core MVC — контроллеры, фильтры, проверка модели [ApiController] и ProblemDetails по умолчанию.
var builder = WebApplication.CreateBuilder(args);
builder.Services.AddControllers();
var app = builder.Build();
app.MapControllers();
app.Run("http://0.0.0.0:8080");
