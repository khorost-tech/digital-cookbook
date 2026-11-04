cmake_minimum_required(VERSION 3.25)
project(stand_service CXX)

# СПРАВОЧНОЕ ПЛЕЧО, ВНЕ ОСИ. Отличие от service/CMakeLists.txt ровно
# одно: нет find_package(fmt) и нет линковки с fmt::fmt. Всё остальное —
# стандарт языка, флаги, статическая линковка — совпадает дословно,
# иначе разность байтов между этим плечом и плечом cpp мерила бы не
# только присутствие библиотеки.
set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

add_executable(stand_service main.cpp)

# Статическая линковка включается снаружи, флагом. Держать её здесь
# значило бы иметь два разных проекта вместо одного.
option(STAND_STATIC "Слинковать статически" OFF)
if(STAND_STATIC)
    target_link_options(stand_service PRIVATE -static)
endif()
