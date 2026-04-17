cmake_minimum_required(VERSION 3.20)
project(forge-cpp-worker-example
    VERSION 0.1.0
    LANGUAGES CXX
)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

# The SDK is in ../../sdk/cpp — add it as a subdirectory so its targets are
# available without installing first.
add_subdirectory(${CMAKE_CURRENT_SOURCE_DIR}/../../sdk/cpp
                 ${CMAKE_CURRENT_BINARY_DIR}/forge-sdk
)

add_executable(forge-cpp-worker main.cpp)

target_link_libraries(forge-cpp-worker
    PRIVATE
        forge_worker_sdk
)
