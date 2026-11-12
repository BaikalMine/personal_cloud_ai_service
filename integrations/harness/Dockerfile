FROM node:24-alpine@sha256:e67514e5d0f6c46656005e1b693b2ec9d52e80b641307de684d4a015ba7a4eaf
RUN apk add --no-cache bash git python3 curl ripgrep \
    && mkdir -p /opt/harness /data/dsh /workspace \
    && chown -R node:node /opt/harness /data /workspace
WORKDIR /opt/harness
USER node
RUN npm install --ignore-scripts --no-audit --no-fund --save-exact @deepseek-ai/dsh@0.2.1-alpha.2
COPY --chown=node:node entry.mjs ./entry.mjs
COPY --chown=node:node model-policy.mjs ./model-policy.mjs
COPY --chown=node:node ui ./node_modules/@ai-gateway/harness-ui
ARG HARNESS_MANAGED_SETTINGS_ORIGINS=""
RUN HARNESS_SETTINGS_CLIENT_PATH=/opt/harness/node_modules/@deepseek-ai/dsh-client-ui-settings/lib/client.js \
    node ./node_modules/@ai-gateway/harness-ui/build.mjs
COPY --chown=node:node gateway-ui.patch.yml ./gateway-ui.patch.yml
ENV DSH_HOME=/data/dsh HOME=/workspace
WORKDIR /workspace
ENTRYPOINT ["node", "/opt/harness/entry.mjs"]
