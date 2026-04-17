// Copyright 2024 Forge Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

#include "grpc_client.h"

#include <iostream>

#include <google/protobuf/timestamp.pb.h>
#include <google/protobuf/util/time_util.h>

namespace forge {
namespace internal {

GrpcClient::GrpcClient(const std::string& coordinator_addr)
    : coordinator_addr_(coordinator_addr) {
    channel_ = grpc::CreateChannel(
        coordinator_addr, grpc::InsecureChannelCredentials());
    stub_ = forgev1::WorkerService::NewStub(channel_);
}

GrpcClient::~GrpcClient() = default;

bool GrpcClient::register_worker(
    const std::string& worker_id,
    const std::string& worker_addr,
    const std::map<std::string, std::string>& labels,
    int capacity,
    const std::vector<std::string>& handlers) {

    forgev1::RegisterRequest request;
    auto* reg = request.mutable_registration();
    reg->set_id(worker_id);
    reg->set_addr(worker_addr);
    reg->set_capacity(capacity);

    for (const auto& [key, value] : labels) {
        (*reg->mutable_labels())[key] = value;
    }
    for (const auto& handler : handlers) {
        reg->add_handlers(handler);
    }

    forgev1::RegisterResponse response;
    grpc::ClientContext context;
    grpc::Status status = stub_->Register(&context, request, &response);

    if (!status.ok()) {
        std::cerr << "[ERROR] registration failed: "
                  << status.error_message() << "\n";
        return false;
    }

    if (!response.accepted()) {
        std::cerr << "[ERROR] coordinator rejected registration: "
                  << response.message() << "\n";
        return false;
    }

    std::cerr << "[INFO] registered with coordinator: "
              << response.message() << "\n";
    return true;
}

std::unique_ptr<grpc::ClientReaderWriter<
    forgev1::HeartbeatPing, forgev1::HeartbeatPong>>
GrpcClient::open_heartbeat_stream(grpc::ClientContext* context) {
    return stub_->Heartbeat(context);
}

}  // namespace internal
}  // namespace forge
