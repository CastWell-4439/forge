// Copyright 2024 Forge Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

#pragma once

#include <map>
#include <string>

#include <nlohmann/json.hpp>

#include "task_context.h"

namespace forge {

/// Result returned by a TaskHandler.  Constructed via the static helpers
/// ok() and error().
class TaskResult {
public:
    /// Create a successful result with JSON output.
    static TaskResult ok(const nlohmann::json& output) {
        TaskResult r;
        r.success_ = true;
        r.output_ = output;
        return r;
    }

    /// Create an error result.
    static TaskResult error(const std::string& message) {
        TaskResult r;
        r.success_ = false;
        r.error_msg_ = message;
        return r;
    }

    [[nodiscard]] bool success() const noexcept { return success_; }
    [[nodiscard]] const std::string& error_message() const noexcept { return error_msg_; }
    [[nodiscard]] const nlohmann::json& output() const noexcept { return output_; }

private:
    TaskResult() = default;

    bool success_ = false;
    std::string error_msg_;
    nlohmann::json output_;
};

/// Abstract base class for task handlers.  Subclass this and override
/// name() and execute() to implement a task handler for the Forge Worker.
class TaskHandler {
public:
    virtual ~TaskHandler() = default;

    /// Handler identifier (e.g., "video.render").
    [[nodiscard]] virtual std::string name() const = 0;

    /// Execute the task and return a result.
    virtual TaskResult execute(const TaskContext& ctx) = 0;
};

}  // namespace forge
