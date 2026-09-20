//! Топ товаров категории — вычисление на стороне кластера.
//!
//! Смысл ровно тот же, что у Lua-хранимки `top_products_by_category` в стенде
//! Tarantool (`performance/inmemory/tarantool/init.lua`): отбор и сортировка
//! выполняются там, где лежат данные, а наружу уходит `limit` строк вместо всей
//! категории. Механизм при этом другой, и подменять одно другим нечестно:
//! хранимка Tarantool идёт по вторичному индексу `(category, views)` локально в
//! одном процессе, а здесь плагин выполняет распределённый SQL — запрос
//! планируется sbroad, исполняется на всех хранилищах и агрегируется. Общее у
//! них одно: клиент не тянет категорию целиком.
//!
//! Тай-брейк для равных `views` — по убыванию `id`, как и в стенде Tarantool:
//! там обратный обход неуникального индекса отдаёт кортежи по PK по убыванию.
//! Без явного `ORDER BY ... , id DESC` две реализации разошлись бы на строках с
//! одинаковым числом просмотров.

use picodata_plugin::error_code::ErrorCode;
use picodata_plugin::sql::query;
use picodata_plugin::system::tarantool::error::BoxError;
use picodata_plugin::transport::rpc;
use picodata_plugin::{plugin::prelude::*, transport::rpc::RouteBuilder};
use serde::{Deserialize, Serialize};

#[derive(Serialize, Deserialize, Debug)]
pub struct TopRequest {
    pub category: String,
    pub limit: u32,
}

#[derive(Serialize, Deserialize, Debug)]
pub struct ProductRow {
    pub id: u64,
    pub title: String,
    pub views: u64,
}

#[derive(Serialize, Deserialize, Debug)]
pub struct TopResponse {
    pub items: Vec<ProductRow>,
    /// Версия плагина, обслужившая запрос. Нужна, чтобы при blue-green-переходе
    /// было видно не «команды выполнились», а какая версия реально отвечает.
    pub served_by_version: String,
}

/// Считает топ категории. Общая точка для RPC- и HTTP-входа: два транспорта
/// обязаны отдавать один и тот же результат, поэтому логика ровно одна.
pub fn top_products(request: &TopRequest) -> anyhow::Result<TopResponse> {
    // LIMIT подставляется в текст запроса, а не биндится: sbroad принимает
    // параметр в WHERE, но на `LIMIT ?` отвечает
    //   "sbroad: rule parsing error" с указанием позиции этого знака вопроса.
    // Подстановка безопасна ровно потому, что limit — u32: строку сюда не
    // подсунуть, форматируется число.
    let sql = format!(
        "SELECT id, title, views FROM products
         WHERE category = ?
         ORDER BY views DESC, id DESC
         LIMIT {}",
        request.limit
    );

    let rows: Vec<ProductRow> = query(&sql)
        .bind(request.category.as_str())
        .fetch::<ProductRow>()
        .map_err(|e| {
            // Без явного лога ошибка теряется: HTTP-транспорт отдаёт наружу
            // только "internal server error", причина видна лишь в журнале
            // инстанса.
            log::error!("запрос топа категории {}: {e}", request.category);
            anyhow::anyhow!("запрос топа категории {}: {e}", request.category)
        })?;

    Ok(TopResponse {
        items: rows,
        served_by_version: env!("CARGO_PKG_VERSION").to_string(),
    })
}

/// Регистрирует RPC-путь `/top_products`.
pub fn register_top_products_handle(context: &PicoContext) {
    RouteBuilder::from_pico_context(context)
        .path("/top_products")
        // RPC-обработчик обязан возвращать BoxError, а не anyhow::Error, поэтому
        // ошибки переводятся явно. Шаблон pike в этом месте ставит unwrap() —
        // для стенда это не годится: паника внутри процесса базы данных хуже
        // внятной ошибки, доехавшей до вызывающего.
        .register(move |req, _ctx| {
            let request: TopRequest = rmp_serde::from_slice(req.as_bytes())
                .map_err(|e| BoxError::new(ErrorCode::PluginError, e.to_string()))?;
            let response = top_products(&request)
                .map_err(|e| BoxError::new(ErrorCode::PluginError, e.to_string()))?;
            rpc::Response::encode_rmp(&response)
                .map_err(|e| BoxError::new(ErrorCode::PluginError, e.to_string()))
        })
        .unwrap();
}
