# C++ Worker Example

A high-performance video processing worker built with the Forge C++ Worker SDK.

## Handlers

| Handler           | Description                         | Input                                                              |
|-------------------|-------------------------------------|--------------------------------------------------------------------|
| `video.render`    | Simulates compute-heavy rendering   | `{"input_path": "...", "format": "mp4", "quality": 80}`           |
| `video.thumbnail` | Extracts a thumbnail from a video   | `{"input_path": "...", "timestamp_sec": 1.0}`                     |

## Prerequisites

- CMake 3.20+
- vcpkg with gRPC, Protobuf, and nlohmann-json
- A C++20 compiler (GCC 12+, Clang 14+, MSVC 2022+)
- Generated protobuf sources in `sdk/cpp/generated/` (run `buf generate` from project root)

## Building

```bash
# From the project root, generate proto sources for C++
buf generate

# Build the example using vcpkg toolchain
cd examples/cpp-worker
cmake -B build -S . \
  -DCMAKE_TOOLCHAIN_FILE=$VCPKG_ROOT/scripts/buildsystems/vcpkg.cmake
cmake --build build
```

## Running

1. Start the Forge Coordinator:

```bash
# From the project root
./forge coordinator --embed-etcd \
  --db=postgres://forge:forge@localhost:5432/forge \
  --redis=redis://localhost:6379
```

2. Run the example worker:

```bash
cd examples/cpp-worker
./build/forge-cpp-worker

# Or with a custom coordinator address:
FORGE_COORDINATOR=coordinator.example.com:8080 ./build/forge-cpp-worker
```

The worker will:
- Register with the Coordinator advertising handlers `video.render` and `video.thumbnail`
- Accept and execute tasks dispatched by the Coordinator
- Respond to heartbeat health checks
- Shut down gracefully on stop
