# libparse и тесты к ней — пример GoogleTest и CTest для статьи 9 и предмет
# оси 7. Покрытие задаётся снаружи через CMAKE_CXX_FLAGS, чтобы сборки оси 7
# различались только флагами, а не описанием сборки.
cmake_minimum_required(VERSION 3.28)
project(correctness_and_operations CXX)

set(CMAKE_CXX_STANDARD 23)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

option(PLANTED "Включить внесённую ошибку P1 (lib/planted.hpp)" OFF)

add_library(parse lib/parse.cpp lib/port.cpp)
target_include_directories(parse PUBLIC lib)
target_compile_definitions(parse PUBLIC PLANTED=$<BOOL:${PLANTED}>)

enable_testing()
find_package(GTest REQUIRED)
find_package(Threads REQUIRED)
include(GoogleTest)

add_executable(parse_test tests/gtest/parse_test.cpp)
target_link_libraries(parse_test PRIVATE parse GTest::gtest_main Threads::Threads)
gtest_discover_tests(parse_test)

# Контроль оси 7 — отдельный исполняемый файл, не часть основного набора.
add_executable(port_boundary_test tests/gtest/boundary_test.cpp)
target_link_libraries(port_boundary_test PRIVATE parse GTest::gtest_main Threads::Threads)
