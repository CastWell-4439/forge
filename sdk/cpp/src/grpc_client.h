// Copyright 2024 Forge Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

#pragma once

#include <map>
#include <memory>
#include <string>
#include <vector>

#include <grpcpp/grpcpp.h>

#include "worker.grpc.pb.h"
#include "worker.pb.h"

namespace forge {

namespace forgev1 = ::forge::v1;

namespace internal {

/// GrpcClient wraps gRPC calls to the Coordinator's WorkerService.
class GrpcClient {
public:
    explicit GrpcClient(const std::string& coordinator_addr);
    ~GrpcClient();

    /// Register this worker with the Coordinator.
    bool register_worker(
        const std::string& worker_id,
        const std::string& worker_addr,
        const std::map<std::string, std::string>& labels,
        int capacity,
        const std::vector<std::string>& handlers);

    /// Open a bidirectional heartbeat stream with the Coordinator.
    std::unique_ptr<grpc::ClientReaderWriter<
        forgev1::HeartbeatPing, forgev1::HeartbeatPong>>
    open_heartbeat_stream(grpc::ClientContext* context);

private:
    std::string coordinator_addr_;
    std::shared_ptr<grpc::Channel> channel_;
    std::unique_ptr<forgev1::WorkerService::Stub> stub_;
};

}  // namespace internal
}  // namespace forge
