ARG BASE_IMAGE=ai-gateway-harness:0.2.1-alpha.2-long-context-r1
FROM ${BASE_IMAGE}
USER root
RUN apk add --no-cache openjdk21-jdk gcompat libstdc++ unzip procps \
    && mkdir -p /opt/rea /opt/ghidra \
    && chown node:node /opt/rea
USER node
RUN npm install --prefix /opt/rea --ignore-scripts --no-audit --no-fund --save-exact \
      --fetch-retries=1 --fetch-timeout=60000 rea-agents@6.3.0
USER root
COPY artifacts/rea-6.3.0-inspection/ghidra_12.1.4_PUBLIC_20260921.zip /tmp/ghidra.zip
RUN echo 'ddac49f903da9d5bac833e5cc79395098b9c33cfd3279be5f31bd00387d2d4db  /tmp/ghidra.zip' | sha256sum -c - \
    && unzip -q /tmp/ghidra.zip -d /opt/ghidra && rm /tmp/ghidra.zip
ENV GHIDRA_INSTALL_DIR=/opt/ghidra/ghidra_12.1.4_PUBLIC JAVA_HOME=/usr/lib/jvm/java-21-openjdk
COPY integrations/harness/tools/patch-koffi-musl.mjs /opt/harness/patch-koffi-musl.mjs
RUN node /opt/harness/patch-koffi-musl.mjs && node -e "require('/opt/harness/node_modules/koffi')"
USER node
COPY --chown=node:node integrations/harness/entry.mjs /opt/harness/entry.mjs
COPY --chown=node:node integrations/harness/rea.patch.yml /opt/harness/rea.patch.yml
COPY --chown=node:node integrations/harness/rea-mcp.mjs /opt/harness/rea-mcp.mjs
