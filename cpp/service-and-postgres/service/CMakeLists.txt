cmake_minimum_required(VERSION 3.25)
project(stand_service CXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

# libpqxx подключается через pkg-config, а не find_package(libpqxx CONFIG):
# в Debian пакет libpqxx-dev не несёт файла libpqxx-config.cmake, и
# CONFIG-режим там не находит ничего. Файл libpqxx.pc пакет несёт, и
# берётся версия ровно та, что лежит в образе.
find_package(PkgConfig REQUIRED)
pkg_check_modules(libpqxx REQUIRED IMPORTED_TARGET libpqxx)

# Общая логика трёх ручек и общий разбор запроса — одна библиотека на все
# слои HTTP. Различаться обязан только слой; всё остальное — один и тот же
# объектный код. Разбор запроса (dispatch.cpp) здесь по той же причине, по
# какой здесь ручки: три собственных разбора строки запроса развели бы
# варианты молча, и ось мерила бы разницу разборов, а не разницу моделей.
add_library(stand_common STATIC main.cpp dispatch.cpp)
target_include_directories(stand_common PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
target_link_libraries(stand_common PUBLIC PkgConfig::libpqxx)

# Точки входа слоёв: каждая своим *_main.cpp, линкующимся с stand_common.
# Собирается всегда ровно один слой: имя приходит снаружи, как и имя
# варианта, и по умолчанию не подставляется. Пустое значение обязано
# ронять настройку сборки, а не собирать что-нибудь: образ, собравший
# чужой слой, выглядел бы обычным вариантом.
set(STAND_LAYER "" CACHE STRING "Слой HTTP, собираемый в этом образе")

if(NOT STAND_LAYER)
  message(FATAL_ERROR
    "STAND_LAYER не задан: слой HTTP выбирается при сборке образа "
    "(-DSTAND_LAYER=beast|drogon|crow) и значения по умолчанию не имеет")
endif()

set(STAND_ENTRY "${CMAKE_CURRENT_SOURCE_DIR}/${STAND_LAYER}_main.cpp")
if(NOT EXISTS "${STAND_ENTRY}")
  message(FATAL_ERROR "нет точки входа слоя ${STAND_LAYER}: ${STAND_ENTRY}")
endif()

add_executable(stand_service "${STAND_ENTRY}")
target_link_libraries(stand_service PRIVATE stand_common)

# Библиотека слоя — единственное, чем различаются три сборки. Всё
# остальное здесь общее: стандарт языка, флаги, клиент базы, объектный код
# stand_common.
if(STAND_LAYER STREQUAL "beast")
  find_package(Boost REQUIRED)
  find_package(Threads REQUIRED)
  target_link_libraries(stand_service PRIVATE Boost::headers Threads::Threads)
elseif(STAND_LAYER STREQUAL "drogon")
  find_package(Drogon CONFIG REQUIRED)
  target_link_libraries(stand_service PRIVATE Drogon::Drogon)
elseif(STAND_LAYER STREQUAL "crow")
  find_package(Threads REQUIRED)
  # Crow в дистрибутиве не упакован: слой приезжает одним заголовком
  # crow_all.h, закреплённым суммой (images/install-layer.sh).
  target_link_libraries(stand_service PRIVATE Threads::Threads)
else()
  message(FATAL_ERROR "неизвестный слой HTTP: ${STAND_LAYER}")
endif()
