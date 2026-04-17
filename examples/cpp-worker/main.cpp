// Copyright 2024 Forge Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

/// @file main.cpp
/// Example C++ Worker — VideoRenderHandler that simulates a compute-heavy
/// video rendering task, demonstrating the Forge C++ Worker SDK.

#include <chrono>
#include <cstdlib>
#include <iostream>
#include <string>
#include <thread>

#include <forge/worker.h>
#include <forge/task_handler.h>

/// VideoRenderHandler simulates a compute-intensive video rendering pipeline.
class VideoRenderHandler : public forge::TaskHandler {
public:
    std::string name() const override { return "video.render"; }

    forge::TaskResult execute(const forge::TaskContext& ctx) override {
        auto input_path = ctx.param<std::string>("input_path");
        auto format = ctx.param_or<std::string>("format", "mp4");
        auto quality = ctx.param_or<int>("quality", 80);

        ctx.log("info", "starting video render: " + input_path +
                        " format=" + format +
                        " quality=" + std::to_string(quality));
        ctx.set_progress(0, "initializing pipeline");

        // Simulate compute-heavy rendering stages.
        ctx.set_progress(25, "decoding input");
        std::this_thread::sleep_for(std::chrono::milliseconds(200));

        ctx.set_progress(50, "rendering frames");
        std::this_thread::sleep_for(std::chrono::milliseconds(300));

        ctx.set_progress(75, "encoding output");
        std::this_thread::sleep_for(std::chrono::milliseconds(200));

        std::string output_path = "/tmp/rendered_" +
                                  ctx.task_id() + "." + format;

        ctx.set_progress(100, "complete");
        ctx.log("info", "render complete: " + output_path);

        return forge::TaskResult::ok({
            {"output_path", output_path},
            {"format", format},
            {"quality", quality},
            {"frames_rendered", 240},
        });
    }
};

/// VideoThumbnailHandler generates a thumbnail from a video.
class VideoThumbnailHandler : public forge::TaskHandler {
public:
    std::string name() const override { return "video.thumbnail"; }

    forge::TaskResult execute(const forge::TaskContext& ctx) override {
        auto input_path = ctx.param<std::string>("input_path");
        auto timestamp_sec = ctx.param_or<double>("timestamp_sec", 1.0);

        ctx.log("info", "generating thumbnail at " +
                        std::to_string(timestamp_sec) + "s from " + input_path);

        // Simulate thumbnail extraction.
        std::this_thread::sleep_for(std::chrono::milliseconds(50));

        std::string thumbnail_path = "/tmp/thumb_" + ctx.task_id() + ".jpg";

        return forge::TaskResult::ok({
            {"thumbnail_path", thumbnail_path},
            {"width", 320},
            {"height", 180},
        });
    }
};

int main() {
    const char* coordinator = std::getenv("FORGE_COORDINATOR");
    std::string coordinator_addr = coordinator ? coordinator : "localhost:8080";

    forge::Worker worker(coordinator_addr);
    worker.set_capacity(4);
    worker.set_labels({{"type", "compute"}, {"language", "cpp"}, {"gpu", "false"}});

    worker.register_handler(std::make_unique<VideoRenderHandler>());
    worker.register_handler(std::make_unique<VideoThumbnailHandler>());

    std::cerr << "[INFO] starting C++ video worker (coordinator="
              << coordinator_addr << ")\n";
    worker.start();  // blocks until shutdown

    return 0;
}
