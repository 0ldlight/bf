# Supply a statically linked linux/amd64 binary as binflow.
# 将静态链接的 linux/amd64 二进制作为 binflow 放入构建上下文。
FROM scratch
COPY --chmod=0555 binflow /binflow
USER 65532:65532
ENV BINFLOW_LISTEN_ADDR=0.0.0.0:8080
ENTRYPOINT ["/binflow"]
