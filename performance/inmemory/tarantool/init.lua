box.cfg{ listen = 3301, memtx_memory = 512 * 1024 * 1024 }

box.once('schema', function()
    box.schema.user.create('app', { password = 'app', if_not_exists = true })
    box.schema.user.grant('app', 'read,write,execute', 'universe', nil, { if_not_exists = true })
    -- create,drop,alter — сверх дословного брифа: нужны сценарию memtx-vinyl
    -- (Step 5), который создаёт два сравнительных space на лету поверх
    -- фиксированной схемы products. Без этого DDL от имени 'app' падает с
    -- AccessDeniedError (живой прогон, см. task-3-report.md).
    box.schema.user.grant('app', 'create,drop,alter', 'universe', nil, { if_not_exists = true })

    local products = box.schema.space.create('products', { if_not_exists = true })
    products:format({
        { name = 'id',          type = 'unsigned' },
        { name = 'sku',         type = 'string'   },
        { name = 'title',       type = 'string'   },
        { name = 'price_cents', type = 'unsigned' },
        { name = 'category',    type = 'string'   },
        { name = 'views',       type = 'unsigned' },
    })
    products:create_index('primary',  { parts = { 'id' }, if_not_exists = true })
    products:create_index('category', { parts = { 'category', 'views' },
                                        unique = false, if_not_exists = true })
end)

-- Хранимка: топ товаров категории по просмотрам. Выполняется РЯДОМ с данными —
-- в приложение уезжает только результат, а не весь набор категории.
-- Именно она сравнивается с вычиткой всей категории в клиент (сценарий 2
-- бенчмарка). ВАЖНО: оба пути делают по ОДНОМУ round-trip — наивный один
-- SELECT всей категории, локальный один CALL. Разница не в числе обращений,
-- а в том, что едет в ответе и где выполняется отбор.
function top_products_by_category(category, limit)
    local result = {}
    local idx = box.space.products.index.category
    for _, tuple in idx:pairs({ category }, { iterator = 'REQ' }) do
        if tuple.category ~= category then break end
        table.insert(result, { id = tuple.id, title = tuple.title, views = tuple.views })
        if #result >= limit then break end
    end
    return result
end

-- Атомарный инкремент просмотров: демонстрация вычисления на сервере.
function bump_views(product_id, delta)
    return box.space.products:update(product_id, { { '+', 'views', delta } })
end
