// Полный: NestJS — модули, контроллеры, внедрение зависимостей поверх Express. Валидация тела — ValidationPipe
// из отдельного пакета class-validator; без него DTO — только тип для компилятора.
import "reflect-metadata";
import { Body, Controller, Get, Module, Param, ParseIntPipe, Post } from "@nestjs/common";
import { NestFactory } from "@nestjs/core";

class ItemDto {
  name!: string;
  qty?: number;
}

@Controller()
class ItemsController {
  @Get("items/:id")
  get(@Param("id", ParseIntPipe) id: number) {
    return { id, name: "item" };
  }

  @Post("items")
  create(@Body() item: ItemDto) {
    return { name: item.name, qty: item.qty ?? 0 };
  }

  @Get("boom")
  boom() {
    throw new Error("boom: секрет в сообщении marker-7f3a9c");
  }
}

@Module({ controllers: [ItemsController] })
class AppModule {}

async function bootstrap() {
  const app = await NestFactory.create(AppModule, { logger: ["error"] });
  await app.listen(8080);
}
bootstrap();
