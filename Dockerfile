FROM registry.k8s.io/build-image/go-runner:v2.3.1-go1.22.0-bookworm.0

COPY bin/iscsiplugin /iscsiplugin

RUN apt-get update && apt-get install -y --no-install-recommends \
    open-iscsi e2fsprogs xfsprogs && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

ENTRYPOINT ["/iscsiplugin"]
