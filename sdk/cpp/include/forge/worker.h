// Copyright 2024 Forge Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

#pragma once

#include <atomic>
#include <map>
#include <memory>
#include <string>
#include <thread>
#include <vector>

#include "task_handler.h"

namespace forge {

/// Worker connects to a Forge Coordinator via gRPC, registers itself, and
/// executes dispatched tasks using registered TaskHandler implementations.
///
/// Example usage:
/// @code
///     forge::Worker worker("localhost:8080");
///     worker.register_handler(std::make_unique<MyHandler>());
///     worker.set_capacity(4);
///     worker.set_labels({{"gpu", "true"}});
///     worker.start();  // blocks until stop() is called
/// @endcode
class Worker {
public:
    /// Construct a Worker that will connect to the given Coordinator address.
    ///
    /// @param coordinator_addr  Coordinator gRPC address (e.g., "localhost:8080").
    /// @param worker_id         Optional unique identifier (auto-generated UUID if empty).
    explicit Worker(const std::string& coordinator_addr,
                    const std::string& worker_id = "");

    ~Worker();

    // Non-copyable, non-movable.
    Worker(const Worker&) = delete;
    Worker& operator=(const Worker&) = delete;
    Worker(Worker&&) = delete;
    Worker& operator=(Worker&&) = delete;

    /// Register a task handler.  Must be called before start().
    void register_handler(std::unique_ptr<TaskHandler> handler);

    /// Set maximum concurrent task capacity (default: 5).
    void set_capacity(int capacity);

    /// Set scheduling labels (e.g., {"gpu": "true"}).
    void set_labels(const std::map<std::string, std::string>& labels);

    /// Set the port for the Worker's gRPC server (0 = auto-pick, default: 0).
    void set_listen_port(int port);

    /// Return the worker's unique identifier.
    [[nodiscard]] const std::string& id() const noexcept;

    /// Start the Worker (blocking).
    ///
    /// 1. Starts a gRPC server for receiving ExecuteTask and Heartbeat RPCs.
    /// 2. Registers with the Coordinator.
    /// 3. Blocks until stop() is called or a signal is received.
    void start();

    /// Signal the Worker to shut down gracefully.
    void stop();

private:
    class Impl;
    std::unique_ptr<Impl> impl_;
};

}  // namespace forge
