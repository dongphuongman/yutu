# Copyright 2025 eat-pray-ai & OpenWaygate
# SPDX-License-Identifier: Apache-2.0

FROM alpine:latest AS binary
ARG TARGETARCH
ARG arm64_binary
ARG amd64_binary

COPY dist /app/
RUN if [ "${TARGETARCH}" = "arm64" ]; then \
      mv "/app/${arm64_binary}" /app/yutu; \
    elif [ "${TARGETARCH}" = "amd64" ]; then \
      mv "/app/${amd64_binary}" /app/yutu; \
    else \
      echo "unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1; \
    fi && \
    chmod +x /app/yutu

FROM gcr.io/distroless/static-debian13:nonroot AS yutu
WORKDIR /app
EXPOSE 8216/tcp
COPY --from=binary /app/yutu /yutu
ENTRYPOINT ["/yutu"]
CMD ["--help"]
