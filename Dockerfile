FROM golang:1.24-bookworm AS go-base

FROM ubuntu:24.04

# Avoid interactive prompts during package install
ENV DEBIAN_FRONTEND=noninteractive

# Install system dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    neovim \
    git \
    make \
    gcc \
    unzip \
    curl \
    ripgrep \
    fd-find \
    npm \
    && rm -rf /var/lib/apt/lists/*

# Symlink fd (Debian/Ubuntu names it fdfind)
RUN ln -sf "$(which fdfind)" /usr/local/bin/fd

# Copy Go toolchain from official image
COPY --from=go-base /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:/root/go/bin:${PATH}"
ENV GOPATH="/root/go"

# Copy portago into the image
COPY . /opt/portago

# Set XDG paths for self-contained operation
ENV XDG_CONFIG_HOME="/opt/portago"
ENV XDG_DATA_HOME="/opt/portago/data"
ENV XDG_STATE_HOME="/opt/portago/state"
ENV XDG_CACHE_HOME="/opt/portago/cache"
ENV NVIM_APPNAME="config"

# Run first-time setup (plugins, LSPs, treesitter parsers)
RUN chmod +x /opt/portago/scripts/setup.sh /opt/portago/bin/portago \
    && /opt/portago/scripts/setup.sh

# /work is where users mount their source code
WORKDIR /work

ENTRYPOINT ["/opt/portago/bin/portago"]
