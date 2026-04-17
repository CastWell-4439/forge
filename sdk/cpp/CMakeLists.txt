cmake_minimum_required(VERSION 3.20)
project(forge-worker-sdk
    VERSION 0.1.0
    LANGUAGES CXX
)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

# --------------------------------------------------------------------------
# Dependencies via vcpkg / system packages
# --------------------------------------------------------------------------
find_package(Protobuf CONFIG REQUIRED)
find_package(gRPC CONFIG REQUIRED)
find_package(nlohmann_json CONFIG REQUIRED)

# --------------------------------------------------------------------------
# Generated protobuf / gRPC sources
# --------------------------------------------------------------------------
set(PROTO_GENERATED_DIR "${CMAKE_CURRENT_SOURCE_DIR}/generated")

set(PROTO_SRCS
    ${PROTO_GENERATED_DIR}/worker.pb.cc
    ${PROTO_GENERATED_DIR}/common.pb.cc
    ${PROTO_GENERATED_DIR}/coordinator.pb.cc
)
set(PROTO_HDRS
    ${PROTO_GENERATED_DIR}/worker.pb.h
    ${PROTO_GENERATED_DIR}/common.pb.h
    ${PROTO_GENERATED_DIR}/coordinator.pb.h
)
set(GRPC_SRCS
    ${PROTO_GENERATED_DIR}/worker.grpc.pb.cc
    ${PROTO_GENERATED_DIR}/coordinator.grpc.pb.cc
)
set(GRPC_HDRS
    ${PROTO_GENERATED_DIR}/worker.grpc.pb.h
    ${PROTO_GENERATED_DIR}/coordinator.grpc.pb.h
)

# --------------------------------------------------------------------------
# Forge Worker SDK library
# --------------------------------------------------------------------------
add_library(forge_worker_sdk
    src/worker.cpp
    src/grpc_client.cpp
    ${PROTO_SRCS}
    ${GRPC_SRCS}
)

target_include_directories(forge_worker_sdk
    PUBLIC
        $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
        $<INSTALL_INTERFACE:include>
    PRIVATE
        ${CMAKE_CURRENT_SOURCE_DIR}/src
        ${PROTO_GENERATED_DIR}
)

target_link_libraries(forge_worker_sdk
    PUBLIC
        protobuf::libprotobuf
        gRPC::grpc++
        nlohmann_json::nlohmann_json
)

# --------------------------------------------------------------------------
# Install rules
# --------------------------------------------------------------------------
install(TARGETS forge_worker_sdk
    ARCHIVE DESTINATION lib
    LIBRARY DESTINATION lib
)
install(DIRECTORY include/forge DESTINATION include)
