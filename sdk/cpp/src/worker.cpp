// Copyright 2024 Forge Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

#include <forge/worker.h>

#include <algorithm>
#include <atomic>
#include <chrono>
#include <condition_variable>
#include <csignal>
#include <iostream>
#include <map>
#include <mutex>
#include <random>
#include <string>
#include <thread>
#include <vector>

#include <grpcpp/grpcpp.h>
#include <nlohmann/json.hpp>

#include <google/protobuf/timestamp.pb.h>
#include <google/protobuf/util/time_util.h>

#include "grpc_client.h"
#include "worker.grpc.pb.h"
#include "worker.pb.h"

namespace forge {

namespace forgev1 = ::forge::v1;

// ---------------------------------------------------------------------------
// Generate a simple UUID-v4-like identifier.
// ---------------------------------------------------------------------------
static std::string generate_uuid() {
    static std::mt19937 rng(std::random_device{}());
    static std::uniform_int_distribution<int> dist(0, 15);
    static const char* hex = "0123456789abcdef";

    std::string uuid = "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx";
    for (auto& c : uuid) {
        if (c == 'x') {
            c = hex[dist(rng)];
        } else if (c == 'y') {
            c = hex[(dist(rng) & 0x3) | 0x8];
        }
    }
    return uuid;
}

// ---------------------------------------------------------------------------
// WorkerServicer — gRPC server-side implementation of WorkerService.
// ---------------------------------------------------------------------------
class WorkerServicer final : public forgev1::WorkerService::Service {
public:
    WorkerServicer(
        const std::string& worker_id,
        std::map<std::string, std::unique_ptr<TaskHandler>>& handlers,
        std::atomic<int32_t>& active_tasks,
        int capacity)
        : worker_id_(worker_id),
          handlers_(handlers),
          active_tasks_(active_tasks),
          capacity_(capacity) {}

    grpc::Status Register(
        grpc::ServerContext* /*context*/,
        const forgev1::RegisterRequest* /*request*/,
        forgev1::RegisterResponse* response) override {
        response->set_accepted(true);
        return grpc::Status::OK;
    }

    grpc::Status Heartbeat(
        grpc::ServerContext* /*context*/,
        grpc::ServerReaderWriter<forgev1::HeartbeatPong,
                                 forgev1::HeartbeatPing>* stream) override {
        std::cerr << "[INFO] heartbeat stream opened for worker "
                  << worker_id_ << "\n";

        forgev1::HeartbeatPing ping;
        while (stream->Read(&ping)) {
            forgev1::HeartbeatPong pong;
            pong.set_worker_id(worker_id_);
            pong.set_active_tasks(active_tasks_.load());
            pong.set_capacity(capacity_);

            auto* ts = pong.mutable_timestamp();
            auto now = std::chrono::system_clock::now();
            auto epoch = now.time_since_epoch();
            auto seconds = std::chrono::duration_cast<std::chrono::seconds>(epoch);
            auto nanos = std::chrono::duration_cast<std::chrono::nanoseconds>(
                             epoch - seconds)
                             .count();
            ts->set_seconds(seconds.count());
            ts->set_nanos(static_cast<int32_t>(nanos));

            if (!stream->Write(pong)) {
                std::cerr << "[ERROR] failed to send heartbeat pong for worker "
                          << worker_id_ << "\n";
                break;
            }
        }

        std::cerr << "[INFO] heartbeat stream closed for worker "
                  << worker_id_ << "\n";
        return grpc::Status::OK;
    }

    grpc::Status ExecuteTask(
        grpc::ServerContext* /*context*/,
        const forgev1::TaskRequest* request,
        forgev1::TaskResponse* response) override {

        active_tasks_.fetch_add(1);

        response->set_task_id(request->task_id());

        auto it = handlers_.find(request->handler());
        if (it == handlers_.end()) {
            active_tasks_.fetch_sub(1);
            response->set_success(false);
            response->set_error_msg(
                "unknown handler: " + request->handler());
            return grpc::Status::OK;
        }

        // Parse JSON input parameters.
        nlohmann::json params;
        if (!request->input().empty()) {
            try {
                params = nlohmann::json::parse(request->input());
            } catch (const nlohmann::json::exception& e) {
                active_tasks_.fetch_sub(1);
                response->set_success(false);
                response->set_error_msg(
                    std::string("failed to decode input JSON: ") + e.what());
                return grpc::Status::OK;
            }
        }

        // Build TaskContext.
        TaskContext ctx(
            request->task_id(),
            request->workflow_id(),
            request->task_name(),
            request->handler(),
            request->timeout_ms(),
            worker_id_,
            std::move(params));

        // Execute handler.
        try {
            TaskResult result = it->second->execute(ctx);
            response->set_success(result.success());

            if (result.success()) {
                std::string output_json = result.output().dump();
                response->set_output(output_json);
            } else {
                response->set_error_msg(result.error_message());
            }
        } catch (const std::exception& e) {
            response->set_success(false);
            response->set_error_msg(
                std::string("handler exception: ") + e.what());
        }

        active_tasks_.fetch_sub(1);
        return grpc::Status::OK;
    }

private:
    std::string worker_id_;
    std::map<std::string, std::unique_ptr<TaskHandler>>& handlers_;
    std::atomic<int32_t>& active_tasks_;
    int capacity_;
};

// ---------------------------------------------------------------------------
// Worker::Impl — PIMPL implementation.
// ---------------------------------------------------------------------------
class Worker::Impl {
public:
    Impl(const std::string& coordinator_addr, const std::string& worker_id)
        : coordinator_addr_(coordinator_addr),
          worker_id_(worker_id.empty() ? generate_uuid() : worker_id),
          capacity_(5),
          listen_port_(0),
          active_tasks_(0),
          running_(false) {}

    ~Impl() {
        stop();
    }

    void register_handler(std::unique_ptr<TaskHandler> handler) {
        const std::string handler_name = handler->name();
        handlers_[handler_name] = std::move(handler);
    }

    void set_capacity(int capacity) { capacity_ = capacity; }

    void set_labels(const std::map<std::string, std::string>& labels) {
        labels_ = labels;
    }

    void set_listen_port(int port) { listen_port_ = port; }

    const std::string& id() const noexcept { return worker_id_; }

    void start() {
        if (handlers_.empty()) {
            throw std::runtime_error(
                "no task handlers registered — call register_handler() before start()");
        }

        running_.store(true);

        // Start gRPC server.
        start_grpc_server();

        // Register with Coordinator.
        register_with_coordinator();

        // Block until stop() is called.
        {
            std::unique_lock<std::mutex> lock(mu_);
            cv_.wait(lock, [this] { return !running_.load(); });
        }

        shutdown();
    }

    void stop() {
        bool expected = true;
        if (running_.compare_exchange_strong(expected, false)) {
            cv_.notify_all();
        }
    }

private:
    void start_grpc_server() {
        std::string server_addr =
            "0.0.0.0:" + std::to_string(listen_port_);

        grpc::ServerBuilder builder;
        int selected_port = 0;
        builder.AddListeningPort(
            server_addr, grpc::InsecureServerCredentials(), &selected_port);

        servicer_ = std::make_unique<WorkerServicer>(
            worker_id_, handlers_, active_tasks_, capacity_);
        builder.RegisterService(servicer_.get());

        server_ = builder.BuildAndStart();
        if (!server_) {
            throw std::runtime_error("failed to start gRPC server on " + server_addr);
        }

        listen_port_ = selected_port;
        std::cerr << "[INFO] worker " << worker_id_
                  << " listening on port " << listen_port_
                  << " (capacity=" << capacity_ << ", handlers=[";
        bool first = true;
        for (const auto& [name, _] : handlers_) {
            if (!first) std::cerr << ", ";
            std::cerr << name;
            first = false;
        }
        std::cerr << "])\n";
    }

    void register_with_coordinator() {
        internal::GrpcClient client(coordinator_addr_);

        std::vector<std::string> handler_names;
        handler_names.reserve(handlers_.size());
        for (const auto& [name, _] : handlers_) {
            handler_names.push_back(name);
        }

        std::string worker_addr = get_worker_addr();

        if (!client.register_worker(
                worker_id_, worker_addr, labels_, capacity_, handler_names)) {
            throw std::runtime_error(
                "failed to register with coordinator at " + coordinator_addr_);
        }
    }

    std::string get_worker_addr() const {
        char hostname[256];
        if (gethostname(hostname, sizeof(hostname)) != 0) {
            return "localhost:" + std::to_string(listen_port_);
        }
        return std::string(hostname) + ":" + std::to_string(listen_port_);
    }

    void shutdown() {
        std::cerr << "[INFO] shutting down worker " << worker_id_ << "\n";

        if (server_) {
            server_->Shutdown();
            server_->Wait();
        }

        std::cerr << "[INFO] worker " << worker_id_ << " stopped\n";
    }

    std::string coordinator_addr_;
    std::string worker_id_;
    int capacity_;
    int listen_port_;
    std::map<std::string, std::string> labels_;
    std::map<std::string, std::unique_ptr<TaskHandler>> handlers_;

    std::atomic<int32_t> active_tasks_;
    std::atomic<bool> running_;
    std::mutex mu_;
    std::condition_variable cv_;

    std::unique_ptr<WorkerServicer> servicer_;
    std::unique_ptr<grpc::Server> server_;
};

// ---------------------------------------------------------------------------
// Worker public API — delegated to Impl via PIMPL.
// ---------------------------------------------------------------------------

Worker::Worker(const std::string& coordinator_addr,
               const std::string& worker_id)
    : impl_(std::make_unique<Impl>(coordinator_addr, worker_id)) {}

Worker::~Worker() = default;

void Worker::register_handler(std::unique_ptr<TaskHandler> handler) {
    impl_->register_handler(std::move(handler));
}

void Worker::set_capacity(int capacity) {
    impl_->set_capacity(capacity);
}

void Worker::set_labels(const std::map<std::string, std::string>& labels) {
    impl_->set_labels(labels);
}

void Worker::set_listen_port(int port) {
    impl_->set_listen_port(port);
}

const std::string& Worker::id() const noexcept {
    return impl_->id();
}

void Worker::start() {
    impl_->start();
}

void Worker::stop() {
    impl_->stop();
}

}  // namespace forge
