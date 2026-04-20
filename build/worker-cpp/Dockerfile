# ---- Build Stage ----
FROM ubuntu:24.04 AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential cmake git ca-certificates \
    libgrpc++-dev libprotobuf-dev protobuf-compiler-grpc \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY sdk/cpp/ ./sdk/cpp/
COPY api/proto/ ./api/proto/

WORKDIR /src/sdk/cpp
RUN mkdir build && cd build && \
    cmake -DCMAKE_BUILD_TYPE=Release .. && \
    make -j$(nproc)

# ---- Runtime Stage ----
FROM ubuntu:24.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    libgrpc++1.51 libprotobuf32 ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -r forge && useradd -r -g forge forge

COPY --from=builder /src/sdk/cpp/build/forge-worker-cpp /usr/local/bin/forge-worker-cpp

USER forge

EXPOSE 50054
ENTRYPOINT ["forge-worker-cpp"]
