// Copyright 2024 Forge Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

#pragma once

#include <cstdint>
#include <iostream>
#include <map>
#include <stdexcept>
#include <string>

#include <nlohmann/json.hpp>

namespace forge {

/// TaskContext provides metadata and parameter access for the currently
/// executing task.  An instance is passed to every TaskHandler::execute call.
class TaskContext {
public:
    TaskContext(std::string task_id,
               std::string workflow_id,
               std::string task_name,
               std::string handler,
               int64_t timeout_ms,
               std::string worker_id,
               nlohmann::json params)
        : task_id_(std::move(task_id)),
          workflow_id_(std::move(workflow_id)),
          task_name_(std::move(task_name)),
          handler_(std::move(handler)),
          timeout_ms_(timeout_ms),
          worker_id_(std::move(worker_id)),
          params_(std::move(params)) {}

    /// Unique task identifier (UUID).
    [[nodiscard]] const std::string& task_id() const noexcept { return task_id_; }

    /// Parent workflow identifier.
    [[nodiscard]] const std::string& workflow_id() const noexcept { return workflow_id_; }

    /// Human-readable task name.
    [[nodiscard]] const std::string& task_name() const noexcept { return task_name_; }

    /// Handler name (e.g., "video.render").
    [[nodiscard]] const std::string& handler() const noexcept { return handler_; }

    /// Task timeout in milliseconds.
    [[nodiscard]] int64_t timeout_ms() const noexcept { return timeout_ms_; }

    /// Task timeout in seconds (convenience).
    [[nodiscard]] double timeout_sec() const noexcept {
        return static_cast<double>(timeout_ms_) / 1000.0;
    }

    /// Executing worker's identifier.
    [[nodiscard]] const std::string& worker_id() const noexcept { return worker_id_; }

    /// Type-safe parameter access.  Throws std::runtime_error if the key is
    /// missing or cannot be converted to T.
    template <typename T>
    T param(const std::string& key) const {
        if (!params_.contains(key)) {
            throw std::runtime_error("missing parameter: " + key);
        }
        try {
            return params_.at(key).get<T>();
        } catch (const nlohmann::json::exception& e) {
            throw std::runtime_error("parameter '" + key + "': " + e.what());
        }
    }

    /// Check whether a parameter key exists.
    [[nodiscard]] bool has_param(const std::string& key) const noexcept {
        return params_.contains(key);
    }

    /// Type-safe parameter access with a default value.
    template <typename T>
    T param_or(const std::string& key, const T& default_value) const {
        if (!params_.contains(key)) {
            return default_value;
        }
        try {
            return params_.at(key).get<T>();
        } catch (const nlohmann::json::exception&) {
            return default_value;
        }
    }

    /// Structured log helper (writes to stderr with task context).
    void log(const std::string& level, const std::string& message) const {
        std::cerr << "[" << level << "] task=" << task_id_
                  << " workflow=" << workflow_id_
                  << " handler=" << handler_
                  << " | " << message << "\n";
    }

    /// Report progress (0-100).
    void set_progress(int percentage, const std::string& message = "") const {
        log("info", "progress " + std::to_string(percentage) + "%: " + message);
    }

private:
    std::string task_id_;
    std::string workflow_id_;
    std::string task_name_;
    std::string handler_;
    int64_t timeout_ms_;
    std::string worker_id_;
    nlohmann::json params_;
};

}  // namespace forge
