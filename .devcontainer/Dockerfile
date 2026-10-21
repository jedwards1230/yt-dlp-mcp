FROM ubuntu:26.04

# Install base tools available in every project container
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    curl \
    jq \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Install yq (YAML processor)
ARG YQ_VERSION=v4.52.4
RUN curl -fsSL "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_amd64" \
    -o /usr/local/bin/yq && chmod +x /usr/local/bin/yq

# ---------------------------------------------------------------------------
# Language-specific toolchains
# Uncomment and customize the section(s) you need.
# ---------------------------------------------------------------------------

# -- Go --
# ARG GO_VERSION=1.23.4
# RUN curl -fsSL https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz \
#     | tar -xz -C /usr/local
# ENV PATH="/usr/local/go/bin:${PATH}"

# -- Node.js --
# ARG NODE_VERSION=22
# RUN curl -fsSL https://deb.nodesource.com/setup_${NODE_VERSION}.x | bash - \
#     && apt-get install -y nodejs \
#     && rm -rf /var/lib/apt/lists/*

# -- Bun --
# RUN curl -fsSL https://bun.sh/install | bash
# ENV PATH="/root/.bun/bin:${PATH}"

# -- Python --
# RUN apt-get update && apt-get install -y --no-install-recommends \
#     python3 python3-pip python3-venv \
#     && rm -rf /var/lib/apt/lists/*

# -- Rust --
# RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y
# ENV PATH="/root/.cargo/bin:${PATH}"
