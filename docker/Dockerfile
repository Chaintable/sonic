FROM ubuntu:23.04

RUN apt-get update && apt-get install -y ca-certificates wget libsnappy-dev libjemalloc-dev libc6-dev

WORKDIR /app

# Using pre-built binaries before docker image building.
COPY /build/sonicd /app
COPY /build/sonictool /app

EXPOSE 5050 8545 8546 8547 9090

ENTRYPOINT ["/app/sonicd"]
