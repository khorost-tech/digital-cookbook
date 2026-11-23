# Сервис осей 3, 4, 5: один бинарник, варианты — переменными окружения.
cmake_minimum_required(VERSION 3.28)
project(stand_service CXX)
set(CMAKE_CXX_STANDARD 23)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
find_package(Threads REQUIRED)
add_executable(service main.cpp config.cpp)
target_link_libraries(service PRIVATE Threads::Threads)
