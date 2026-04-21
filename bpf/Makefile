# eBPF Build Targets
#
# Prerequisites:
#   - Linux with kernel 5.8+
#   - clang 12+
#   - bpftool (for generating vmlinux.h)
#   - linux-headers-$(uname -r)
#
# On Ubuntu/Debian:
#   apt install clang llvm libbpf-dev linux-tools-common bpftool
#
# Generate vmlinux.h (one-time):
#   bpftool btf dump file /sys/kernel/btf/vmlinux format c > vmlinux.h

CLANG ?= clang
ARCH ?= x86

BPF_CFLAGS := -O2 -g -target bpf -D__TARGET_ARCH_$(ARCH)

SOURCES := $(wildcard *.c)
OBJECTS := $(SOURCES:.c=.o)

.PHONY: all clean

all: $(OBJECTS)

%.o: %.c vmlinux.h
	$(CLANG) $(BPF_CFLAGS) -c $< -o $@

vmlinux.h:
	@if [ -f /sys/kernel/btf/vmlinux ]; then \
		bpftool btf dump file /sys/kernel/btf/vmlinux format c > $@; \
	else \
		echo "ERROR: /sys/kernel/btf/vmlinux not found. Run on a BTF-enabled Linux kernel."; \
		exit 1; \
	fi

clean:
	rm -f $(OBJECTS)

# Pre-compiled .o files are checked into git for environments without clang.
# To refresh: make clean && make
