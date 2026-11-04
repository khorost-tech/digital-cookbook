using Microsoft.AspNetCore.Mvc;

[ApiController]
public class ItemsController : ControllerBase
{
    // Nullable включён: string Name без «?» для проверки модели — неявный [Required]
    public record Item(string Name, int Qty);

    [HttpGet("items/{id:int}")]
    public IActionResult Get(int id) => Ok(new { id, name = "item" });

    [HttpPost("items")]
    public IActionResult Create(Item item) => Created("/items/1", item);

    [HttpGet("boom")]
    public string Boom() => throw new InvalidOperationException("boom: секрет в сообщении marker-7f3a9c");
}
