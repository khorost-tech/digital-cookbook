cmake_minimum_required(VERSION 3.25)
project(stand_service CXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

find_package(fmt CONFIG REQUIRED)

add_executable(stand_service main.cpp)
target_link_libraries(stand_service PRIVATE fmt::fmt)

# Статическая линковка включается снаружи, флагом. Держать её здесь
# значило бы иметь два разных проекта вместо одного.
option(STAND_STATIC "Слинковать статически" OFF)
if(STAND_STATIC)
    target_link_options(stand_service PRIVATE -static)
endif()
