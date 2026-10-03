FROM ubuntu:20.04

ARG DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
    && apt-get install --yes --no-install-recommends \
        build-essential \
        ca-certificates \
        curl \
        git \
        libfontconfig1-dev \
        libxkbcommon-x11-0 \
        make \
        pkg-config \
    && rm -rf /var/lib/apt/lists/*

RUN curl --proto '=https' --tlsv1.2 --silent --show-error --fail \
        --output /tmp/rustup-init.sh https://sh.rustup.rs \
    && sh /tmp/rustup-init.sh --yes --profile minimal --default-toolchain stable \
    && rm /tmp/rustup-init.sh

ENV PATH="/root/.cargo/bin:${PATH}"

WORKDIR /workspace

CMD ["bash", "-c", "git config --global --add safe.directory /workspace && make montray-ui && bin/montray-ui --version"]
